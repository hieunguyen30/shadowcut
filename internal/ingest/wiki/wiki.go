// Package wiki ingests Wikimedia EventStreams "recentchange" events into Postgres.
//
// Etiquette (https://wikitech.wikimedia.org/wiki/Event_Platform/EventStreams):
// a descriptive User-Agent with contact info, a single connection, reconnect with
// exponential backoff, and resume via Last-Event-ID.
package wiki

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"
)

// StreamURL is the public recentchange stream.
const StreamURL = "https://stream.wikimedia.org/v2/stream/recentchange"

// Event is the subset of a recentchange event Shadowcut uses.
type Event struct {
	Meta struct {
		ID     string `json:"id"`
		DT     string `json:"dt"`
		Domain string `json:"domain"`
	} `json:"meta"`
	Type      string `json:"type"`
	Namespace int64  `json:"namespace"`
	Title     string `json:"title"`
	Comment   string `json:"comment"`
	Timestamp int64  `json:"timestamp"`
	User      string `json:"user"`
	Bot       bool   `json:"bot"`
	Minor     *bool  `json:"minor"`
	Wiki      string `json:"wiki"`
	Length    *struct {
		Old *int64 `json:"old"`
		New *int64 `json:"new"`
	} `json:"length"`
	Revision *struct {
		Old *int64 `json:"old"`
		New *int64 `json:"new"`
	} `json:"revision"`
}

// Edit is the normalized row written to the edits table.
type Edit struct {
	EventID     string
	Wiki        string
	Title       string
	Namespace   int64
	UserName    *string // nil for anonymous (IP) and temporary accounts
	IsBot       bool
	IsMinor     *bool
	ChangeType  string
	Comment     *string
	OldLen      *int64
	NewLen      *int64
	SizeDelta   *int64
	GrowthRatio *float64
	RevNew      *int64
	EditTS      time.Time
}

// ToEdit converts an event; ok=false for event types we do not store.
func (e *Event) ToEdit() (Edit, bool) {
	if e.Type != "edit" && e.Type != "new" {
		return Edit{}, false
	}
	if e.Meta.ID == "" || e.Title == "" || e.Wiki == "" {
		return Edit{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, e.Meta.DT)
	if err != nil {
		ts = time.Unix(e.Timestamp, 0).UTC()
	}
	ed := Edit{
		EventID: e.Meta.ID, Wiki: e.Wiki, Title: e.Title, Namespace: e.Namespace,
		IsBot: e.Bot, IsMinor: e.Minor, ChangeType: e.Type, EditTS: ts.UTC(),
	}
	if u := e.User; u != "" && !IsAnonymous(u) {
		ed.UserName = &u
	}
	if c := e.Comment; c != "" {
		ed.Comment = &c
	}
	if e.Length != nil {
		ed.OldLen, ed.NewLen = e.Length.Old, e.Length.New
		if e.Length.New != nil {
			old := int64(0)
			if e.Length.Old != nil {
				old = *e.Length.Old
			}
			d := *e.Length.New - old
			ed.SizeDelta = &d
			if e.Length.Old != nil && *e.Length.Old > 0 {
				r := float64(*e.Length.New) / float64(*e.Length.Old)
				ed.GrowthRatio = &r
			}
		}
	}
	if e.Revision != nil {
		ed.RevNew = e.Revision.New
	}
	return ed, true
}

// IsAnonymous reports IP editors and temporary accounts ("~2025-12345-67").
func IsAnonymous(user string) bool {
	if _, err := netip.ParseAddr(user); err == nil {
		return true
	}
	return strings.HasPrefix(user, "~")
}

// Source yields raw event JSON lines.
type Source interface {
	Run(ctx context.Context, out chan<- []byte) error
}

// SSE is a live EventStreams client with reconnect and backoff.
type SSE struct {
	URL       string
	UserAgent string
	Client    *http.Client
	// MaxBackoff caps the reconnect delay.
	MaxBackoff time.Duration
	// OnReconnect is called on each reconnect (for metrics).
	OnReconnect func(err error, delay time.Duration)
	lastID      string
}

// Run streams until ctx is done. Connection errors trigger reconnects with
// exponential backoff + jitter; it only returns on ctx cancellation.
func (s *SSE) Run(ctx context.Context, out chan<- []byte) error {
	if s.Client == nil {
		s.Client = &http.Client{Timeout: 0}
	}
	if s.MaxBackoff == 0 {
		s.MaxBackoff = time.Minute
	}
	backoff := time.Second
	for {
		start := time.Now()
		err := s.once(ctx, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second // a healthy connection resets the backoff
		}
		delay := backoff + time.Duration(rand.Int63n(int64(backoff/2)+1))
		slog.Warn("eventstreams: disconnected; reconnecting", "err", err, "delay", delay.Round(time.Millisecond))
		if s.OnReconnect != nil {
			s.OnReconnect(err, delay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		backoff *= 2
		if backoff > s.MaxBackoff {
			backoff = s.MaxBackoff
		}
	}
}

func (s *SSE) once(ctx context.Context, out chan<- []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", s.UserAgent)
	if s.lastID != "" {
		req.Header.Set("Last-Event-ID", s.lastID)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("eventstreams: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return ParseSSE(resp.Body, func(id, data string) error {
		if id != "" {
			s.lastID = id
		}
		select {
		case out <- []byte(data):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

// ParseSSE parses a text/event-stream body, calling fn per "message" event.
func ParseSSE(r io.Reader, fn func(id, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var id, event string
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if data.Len() > 0 && (event == "" || event == "message") {
				if err := fn(id, data.String()); err != nil {
					return err
				}
			}
			data.Reset()
			event = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			event = value
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("eventstreams: stream closed by server")
}

// Replay replays recorded JSON lines (fallback when the network is unavailable).
type Replay struct {
	Path string
	// Rate limits events per second (0 = as fast as possible).
	Rate float64
	// Loop restarts the file at EOF, rewriting event ids to keep them unique.
	Loop bool
}

// Run implements Source.
func (r *Replay) Run(ctx context.Context, out chan<- []byte) error {
	var tick <-chan time.Time
	if r.Rate > 0 {
		t := time.NewTicker(time.Duration(float64(time.Second) / r.Rate))
		defer t.Stop()
		tick = t.C
	}
	for pass := 0; ; pass++ {
		f, err := os.Open(r.Path)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			if pass > 0 {
				line = rewriteID(line, pass)
			}
			if tick != nil {
				select {
				case <-tick:
				case <-ctx.Done():
					f.Close()
					return ctx.Err()
				}
			}
			select {
			case out <- line:
			case <-ctx.Done():
				f.Close()
				return ctx.Err()
			}
		}
		f.Close()
		if !r.Loop {
			return nil
		}
	}
}

func rewriteID(line []byte, pass int) []byte {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return line
	}
	if meta, ok := m["meta"].(map[string]any); ok {
		if id, ok := meta["id"].(string); ok && len(id) >= 8 {
			meta["id"] = fmt.Sprintf("%08x", pass) + id[8:]
		}
		meta["dt"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, _ := json.Marshal(m)
	return b
}
