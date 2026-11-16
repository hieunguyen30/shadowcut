package shadow

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

// Proxy is a PostgreSQL wire-protocol proxy. It relays every byte unchanged
// between the client and Postgres, and in parallel parses both directions to
// capture read statements together with the exact result the client received.
// Captured reads are submitted to the Engine (asynchronously, never blocking).
//
// Supported: simple query protocol (incl. multi-statement strings), extended
// protocol (Parse/Bind/Describe/Execute/Sync, named and unnamed statements and
// portals, text and binary formats). Not shadowed: COPY, portals suspended by a
// row limit, results larger than MaxRows.
type Proxy struct {
	Listen   string
	Upstream string // host:port of Postgres
	Engine   *Engine
	MaxRows  int // results with more rows are not shadowed (default 10000)

	mu    sync.Mutex
	conns int
}

// Serve accepts connections until ctx ends.
func (p *Proxy) Serve(ctx context.Context) error {
	if p.MaxRows <= 0 {
		p.MaxRows = 10000
	}
	ln, err := net.Listen("tcp", p.Listen)
	if err != nil {
		return err
	}
	return p.ServeListener(ctx, ln)
}

// ServeListener serves on an existing listener (tests use 127.0.0.1:0).
func (p *Proxy) ServeListener(ctx context.Context, ln net.Listener) error {
	if p.MaxRows <= 0 {
		p.MaxRows = 10000
	}
	go func() { <-ctx.Done(); ln.Close() }()
	slog.Info("shadow proxy listening", "listen", p.Listen, "upstream", p.Upstream)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("accept", "err", err)
			continue
		}
		go p.handle(ctx, c)
	}
}

// readStartup reads an untyped startup-phase message (length-prefixed).
func readStartup(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[:]))
	if n < 8 || n > 1<<20 {
		return nil, fmt.Errorf("bad startup length %d", n)
	}
	buf := make([]byte, n)
	copy(buf, hdr[:])
	if _, err := io.ReadFull(r, buf[4:]); err != nil {
		return nil, err
	}
	return buf, nil
}

const (
	sslRequestCode    = 80877103
	gssEncRequestCode = 80877104
	cancelRequestCode = 80877102
)

func (p *Proxy) handle(ctx context.Context, client net.Conn) {
	defer client.Close()
	cr := bufio.NewReaderSize(client, 64*1024)
	var startup []byte
	for {
		msg, err := readStartup(cr)
		if err != nil {
			return
		}
		code := binary.BigEndian.Uint32(msg[4:8])
		if code == sslRequestCode || code == gssEncRequestCode {
			if _, err := client.Write([]byte{'N'}); err != nil {
				return
			}
			continue
		}
		startup = msg
		break
	}
	network := "tcp"
	if strings.HasPrefix(p.Upstream, "/") {
		network = "unix" // e.g. a Cloud SQL connector socket
	}
	up, err := net.DialTimeout(network, p.Upstream, 10*time.Second)
	if err != nil {
		slog.Warn("proxy: upstream dial failed", "err", err)
		return
	}
	defer up.Close()
	if _, err := up.Write(startup); err != nil {
		return
	}
	if binary.BigEndian.Uint32(startup[4:8]) == cancelRequestCode {
		return
	}
	p.mu.Lock()
	p.conns++
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.conns--; p.mu.Unlock() }()

	st := newConnState(p)
	done := make(chan struct{}, 2)
	go func() { // client -> server
		defer func() { done <- struct{}{} }()
		relay(cr, up, st.onFrontend)
	}()
	go func() { // server -> client
		defer func() { done <- struct{}{} }()
		relay(bufio.NewReaderSize(up, 64*1024), client, st.onBackend)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// relay copies typed messages from r to w, calling observe(type, body) BEFORE
// forwarding each (so request bookkeeping precedes the server's response).
// observe must not retain body.
func relay(r *bufio.Reader, w io.Writer, observe func(byte, []byte)) {
	hdr := make([]byte, 5)
	buf := make([]byte, 0, 64*1024)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint32(hdr[1:5])) - 4
		if n < 0 {
			return
		}
		if cap(buf) < n {
			buf = make([]byte, n)
		}
		body := buf[:n]
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("proxy: observer panic (traffic unaffected)", "panic", rec)
				}
			}()
			observe(hdr[0], body)
		}()
		if _, err := w.Write(hdr); err != nil {
			return
		}
		if _, err := w.Write(body); err != nil {
			return
		}
	}
}

// ---- protocol state -------------------------------------------------------

type preparedStmt struct {
	sql       string
	info      sqlscan.Info
	paramOIDs []uint32
	fields    []pgproto3.FieldDescription
}

type portal struct {
	stmt          *preparedStmt
	params        [][]byte
	paramFormats  []int16
	resultFormats []int16
	fields        []pgproto3.FieldDescription // from Describe(portal), if any
}

type itemKind int

const (
	kSimple itemKind = iota
	kParse
	kBind
	kDescribeStmt
	kDescribePortal
	kExecute
	kClose
	kSync
)

type item struct {
	kind itemKind
	// simple
	stmts []sqlscan.Statement
	idx   int
	// parse/describe/execute
	stmt   *preparedStmt
	portal *portal
}

// capture accumulates one result set.
type capture struct {
	sql     string
	info    sqlscan.Info
	args    []any
	fields  []pgproto3.FieldDescription
	formats []int16
	rows    []rowset.Row
	tooBig  bool
	decErr  error
	at      time.Time
}

type connState struct {
	p  *Proxy
	mu sync.Mutex
	m  *pgtype.Map

	stmts   map[string]*preparedStmt
	portals map[string]*portal
	queue   []*item
	cur     *capture
	ready   bool // first ReadyForQuery seen (auth finished)
	txn     byte // last ReadyForQuery status: I, T, E
	writes  bool // current transaction has written
	failed  bool // error seen; discard until Sync
}

func newConnState(p *Proxy) *connState {
	return &connState{p: p, m: pgtype.NewMap(), stmts: map[string]*preparedStmt{}, portals: map[string]*portal{}, txn: 'I'}
}

func (s *connState) push(it *item) { s.queue = append(s.queue, it) }

func (s *connState) front() *item {
	if len(s.queue) == 0 {
		return nil
	}
	return s.queue[0]
}

func (s *connState) pop() {
	if len(s.queue) > 0 {
		s.queue = s.queue[1:]
	}
}

// onFrontend observes client -> server messages.
func (s *connState) onFrontend(t byte, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch t {
	case 'Q':
		var q pgproto3.Query
		if q.Decode(body) != nil {
			return
		}
		sts := sqlscan.Split(q.String)
		s.push(&item{kind: kSimple, stmts: sts})
	case 'P':
		var m pgproto3.Parse
		if m.Decode(body) != nil {
			return
		}
		ps := &preparedStmt{sql: m.Query, info: sqlscan.Analyze(sqlscan.Lex(m.Query)), paramOIDs: append([]uint32(nil), m.ParameterOIDs...)}
		s.stmts[m.Name] = ps
		s.push(&item{kind: kParse, stmt: ps})
	case 'B':
		var m pgproto3.Bind
		if m.Decode(body) != nil {
			return
		}
		pt := &portal{stmt: s.stmts[m.PreparedStatement], paramFormats: append([]int16(nil), m.ParameterFormatCodes...), resultFormats: append([]int16(nil), m.ResultFormatCodes...)}
		for _, v := range m.Parameters {
			if v == nil {
				pt.params = append(pt.params, nil)
			} else {
				pt.params = append(pt.params, append([]byte{}, v...))
			}
		}
		s.portals[m.DestinationPortal] = pt
		s.push(&item{kind: kBind, portal: pt})
	case 'D':
		var m pgproto3.Describe
		if m.Decode(body) != nil {
			return
		}
		if m.ObjectType == 'S' {
			s.push(&item{kind: kDescribeStmt, stmt: s.stmts[m.Name]})
		} else {
			s.push(&item{kind: kDescribePortal, portal: s.portals[m.Name]})
		}
	case 'E':
		var m pgproto3.Execute
		if m.Decode(body) != nil {
			return
		}
		s.push(&item{kind: kExecute, portal: s.portals[m.Portal]})
	case 'C':
		var m pgproto3.Close
		if m.Decode(body) == nil {
			if m.ObjectType == 'S' {
				delete(s.stmts, m.Name)
			} else {
				delete(s.portals, m.Name)
			}
		}
		s.push(&item{kind: kClose})
	case 'S':
		s.push(&item{kind: kSync})
	}
}

// onBackend observes server -> client messages.
func (s *connState) onBackend(t byte, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		if t == 'Z' {
			s.ready = true
			if len(body) > 0 {
				s.txn = body[0]
			}
		}
		return
	}
	it := s.front()
	switch t {
	case 'Z': // ReadyForQuery: everything up to here is complete; resync.
		if len(body) > 0 {
			s.txn = body[0]
			if s.txn == 'I' {
				s.writes = false
			}
		}
		for len(s.queue) > 0 {
			k := s.queue[0].kind
			s.pop()
			if k == kSync || k == kSimple {
				break
			}
		}
		s.cur = nil
		s.failed = false
	case 'E': // ErrorResponse
		s.cur = nil
		if it != nil && it.kind == kSimple {
			return // rest of the simple query string is skipped; Z will resync
		}
		// extended protocol: server discards messages until Sync
		for len(s.queue) > 0 && s.queue[0].kind != kSync {
			s.pop()
		}
		s.failed = true
	case '1', '2', '3': // ParseComplete, BindComplete, CloseComplete
		if it != nil && (it.kind == kParse || it.kind == kBind || it.kind == kClose) {
			s.pop()
		}
	case 't': // ParameterDescription
		if it != nil && it.kind == kDescribeStmt && it.stmt != nil {
			var m pgproto3.ParameterDescription
			if m.Decode(body) == nil {
				it.stmt.paramOIDs = append([]uint32(nil), m.ParameterOIDs...)
			}
		}
	case 'n': // NoData
		if it != nil && (it.kind == kDescribeStmt || it.kind == kDescribePortal) {
			s.pop()
		}
	case 'T': // RowDescription
		var m pgproto3.RowDescription
		if m.Decode(body) != nil {
			return
		}
		fields := cloneFields(m.Fields)
		if it == nil {
			return
		}
		switch it.kind {
		case kDescribeStmt:
			if it.stmt != nil {
				it.stmt.fields = fields
			}
			s.pop()
		case kDescribePortal:
			if it.portal != nil {
				it.portal.fields = fields
			}
			s.pop()
		case kSimple:
			if it.idx < len(it.stmts) {
				st := it.stmts[it.idx]
				info := sqlscan.Analyze(st.Tokens)
				formats := make([]int16, len(fields))
				for i, f := range fields {
					formats[i] = f.Format
				}
				s.cur = &capture{sql: st.Text, info: info, fields: fields, formats: formats, at: time.Now().UTC()}
			}
		}
	case 'D': // DataRow
		if s.cur == nil {
			if it != nil && it.kind == kExecute {
				s.cur = s.beginExecute(it)
			}
			if s.cur == nil {
				return
			}
		}
		s.addRow(body)
	case 'C': // CommandComplete
		if it == nil {
			return
		}
		switch it.kind {
		case kSimple:
			if it.idx < len(it.stmts) {
				info := sqlscan.Analyze(it.stmts[it.idx].Tokens)
				s.noteWrite(info)
			}
			s.finish()
			it.idx++
		case kExecute:
			if s.cur == nil {
				s.cur = s.beginExecute(it) // zero-row result
			}
			if it.portal != nil && it.portal.stmt != nil {
				s.noteWrite(it.portal.stmt.info)
			}
			s.finish()
			s.pop()
		}
	case 'I': // EmptyQueryResponse
		if it != nil && it.kind == kSimple {
			it.idx++
		} else if it != nil && it.kind == kExecute {
			s.pop()
		}
		s.cur = nil
	case 's': // PortalSuspended: partial result, don't shadow
		s.cur = nil
		if it != nil && it.kind == kExecute {
			s.pop()
		}
	case 'G', 'H', 'W': // COPY: stop tracking until the next ReadyForQuery
		s.cur = nil
	}
}

func (s *connState) noteWrite(info sqlscan.Info) {
	switch info.Kind {
	case sqlscan.KInsert, sqlscan.KUpdate, sqlscan.KDelete:
		s.writes = true
	}
}

func cloneFields(fs []pgproto3.FieldDescription) []pgproto3.FieldDescription {
	out := make([]pgproto3.FieldDescription, len(fs))
	for i, f := range fs {
		out[i] = f
		out[i].Name = append([]byte(nil), f.Name...)
	}
	return out
}

func (s *connState) beginExecute(it *item) *capture {
	pt := it.portal
	if pt == nil || pt.stmt == nil {
		return nil
	}
	fields := pt.fields
	if fields == nil {
		fields = pt.stmt.fields
	}
	if !IsShadowable(pt.stmt.info) {
		return nil
	}
	c := &capture{sql: pt.stmt.sql, info: pt.stmt.info, fields: fields, at: time.Now().UTC()}
	c.formats = expandFormats(pt.resultFormats, len(fields))
	// decode parameters
	pf := expandFormats(pt.paramFormats, len(pt.params))
	for i, raw := range pt.params {
		var oid uint32
		if i < len(pt.stmt.paramOIDs) {
			oid = pt.stmt.paramOIDs[i]
		}
		v, err := decode(s.m, oid, pf[i], raw)
		if err != nil {
			c.decErr = err
			break
		}
		c.args = append(c.args, v)
	}
	return c
}

func expandFormats(f []int16, n int) []int16 {
	out := make([]int16, n)
	switch len(f) {
	case 0:
	case 1:
		for i := range out {
			out[i] = f[0]
		}
	default:
		copy(out, f)
	}
	return out
}

func decode(m *pgtype.Map, oid uint32, format int16, raw []byte) (any, error) {
	if raw == nil {
		return nil, nil
	}
	if t, ok := m.TypeForOID(oid); ok {
		v, err := t.Codec.DecodeValue(m, oid, format, raw)
		if b, isBytes := v.([]byte); isBytes {
			v = append([]byte(nil), b...) // the relay reuses its buffer
		}
		return v, err
	}
	if format == pgtype.TextFormatCode {
		return string(raw), nil
	}
	return append([]byte(nil), raw...), nil
}

func (s *connState) addRow(body []byte) {
	c := s.cur
	if c.tooBig || c.decErr != nil {
		return
	}
	if len(c.rows) >= s.p.MaxRows {
		c.tooBig = true
		c.rows = nil
		return
	}
	var dr pgproto3.DataRow
	if err := dr.Decode(body); err != nil {
		c.decErr = err
		return
	}
	row := make(rowset.Row, len(dr.Values))
	for i, raw := range dr.Values {
		var oid uint32
		var f int16
		if i < len(c.fields) {
			oid = c.fields[i].DataTypeOID
		}
		if i < len(c.formats) {
			f = c.formats[i]
		}
		v, err := decode(s.m, oid, f, raw)
		if err != nil {
			c.decErr = err
			return
		}
		row[i] = rowset.Normalize(v)
	}
	c.rows = append(c.rows, row)
}

// finish submits the current capture to the engine if it is a shadowable read.
func (s *connState) finish() {
	c := s.cur
	s.cur = nil
	if c == nil || c.tooBig || c.decErr != nil || !IsShadowable(c.info) || s.failed {
		if c != nil && c.decErr != nil {
			slog.Debug("proxy: not shadowed (decode)", "err", c.decErr)
		}
		return
	}
	res := &rowset.Result{Rows: c.rows}
	for _, f := range c.fields {
		res.Columns = append(res.Columns, rowset.Column{Name: string(f.Name), OID: f.DataTypeOID})
	}
	s.p.Engine.Submit(Job{SQL: c.sql, Args: c.args, Primary: res, InTxn: s.txn == 'T', TxnWrites: s.writes, Source: "proxy", At: c.at})
}

// ErrNotShadowed is returned for statements the proxy will not shadow.
var ErrNotShadowed = errors.New("not shadowed")
