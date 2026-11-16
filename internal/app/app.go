// Package app is the Shadowcut demo application: a small read/write API over the
// Wikipedia edits dataset. Every SQL statement it runs comes from queries/app.sql.
//
// Routing (read from shadowcut.control, polled):
//
//	postgres: writes -> Postgres; reads -> READ_DSN (the shadow proxy) or Postgres
//	frozen:   writes rejected (503) during cutover/rollback; reads -> Postgres
//	spanner:  reads and writes -> Spanner (PGAdapter); each write is journaled in
//	          sc_reverse_journal in the same transaction for rollback
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/shadowcut/shadowcut/internal/telemetry"
	"github.com/shadowcut/shadowcut/queries"
)

// Mode is the app's routing mode.
type Mode string

const (
	ModePostgres Mode = "postgres"
	ModeFrozen   Mode = "frozen"
	ModeSpanner  Mode = "spanner"
)

// Server is the demo app.
type Server struct {
	PG      *pgxpool.Pool // source of truth before cutover; control plane
	Read    *pgxpool.Pool // read path in postgres mode (shadow proxy); may equal PG
	Spanner *pgxpool.Pool // target (PGAdapter)
	mode    atomic.Value  // Mode
	q       map[string]string

	reqs    metric.Int64Counter
	latency metric.Float64Histogram
	retries metric.Int64Counter
	// RetryCount counts transaction retries (aborts / serialization failures).
	RetryCount atomic.Int64
	// NoRetry disables the retry loop (isolation experiment baseline).
	NoRetry bool
}

// New builds a server. Read may be nil (reads go to PG).
func New(pg, read, sp *pgxpool.Pool, meter metric.Meter) *Server {
	if read == nil {
		read = pg
	}
	s := &Server{PG: pg, Read: read, Spanner: sp, q: map[string]string{}}
	for name, st := range queries.App() {
		s.q[name] = st.Text
	}
	s.mode.Store(ModePostgres)
	s.reqs = telemetry.Must(meter.Int64Counter("shadowcut_app_requests_total"))
	s.latency = telemetry.Must(meter.Float64Histogram("shadowcut_app_request_seconds", metric.WithUnit("s")))
	s.retries = telemetry.Must(meter.Int64Counter("shadowcut_app_txn_retries_total"))
	return s
}

// Mode returns the current routing mode.
func (s *Server) Mode() Mode { return s.mode.Load().(Mode) }

// SetMode overrides the mode (tests).
func (s *Server) SetMode(m Mode) { s.mode.Store(m) }

// PollControl refreshes the mode from shadowcut.control until ctx ends.
func (s *Server) PollControl(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		var m string
		err := s.PG.QueryRow(ctx, `SELECT mode FROM shadowcut.control WHERE id = 1`).Scan(&m)
		if err == nil && Mode(m) != s.Mode() {
			slog.Info("routing mode changed", "from", s.Mode(), "to", m)
			s.mode.Store(Mode(m))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) readPool() *pgxpool.Pool {
	if s.Mode() == ModeSpanner {
		return s.Spanner
	}
	return s.Read
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern, name string, h func(http.ResponseWriter, *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			err := h(w, r)
			status := "ok"
			if err != nil {
				status = "error"
				code := http.StatusInternalServerError
				var he httpErr
				if errors.As(err, &he) {
					code = he.code
				}
				writeJSON(w, code, map[string]string{"error": err.Error()})
			}
			attrs := metric.WithAttributes(attribute.String("route", name), attribute.String("status", status), attribute.String("mode", string(s.Mode())))
			s.reqs.Add(r.Context(), 1, attrs)
			s.latency.Record(r.Context(), time.Since(start).Seconds(), attrs)
		})
	}
	route("GET /healthz", "healthz", func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, 200, map[string]string{"status": "ok", "mode": string(s.Mode())})
		return nil
	})
	route("GET /api/pages/top", "top_pages", s.list("top_pages", strArg("wiki", "enwiki"), intArg("limit", 20)))
	route("GET /api/users/{user}/edits", "recent_edits_by_user", s.list("recent_edits_by_user", pathArg("user")))
	route("GET /api/edits", "edits_in_range", s.list("edits_in_range", timeArg("from", -time.Hour), timeArg("to", 0)))
	route("GET /api/search", "search_titles", s.list("search_titles", strArg("q", "A")))
	route("GET /api/feed", "feed", s.feed)
	route("GET /api/feed/offset", "feed_offset", s.list("feed_offset", intArg("offset", 0)))
	route("GET /api/pages/alpha", "pages_alpha", s.list("pages_alpha", strArg("wiki", "enwiki"), strArg("after", "")))
	route("GET /api/stats/wikis", "wiki_stats", s.list("wiki_stats"))
	route("GET /api/stats/daily", "daily_counts", s.list("daily_counts", strArg("wiki", "enwiki")))
	route("GET /api/editors/recent", "editor_labels", s.list("editor_labels"))
	route("GET /api/editors/top", "top_editors", s.list("top_editors", strArg("wiki", "enwiki")))
	route("GET /api/pages/{wiki}/{title}", "page_detail", s.list("page_detail", pathArg("wiki"), pathArg("title")))
	route("GET /api/gh/repos/top", "gh_top_repos", s.list("gh_top_repos"))
	route("GET /api/gh/repos/{owner}/{name}", "gh_repo_events", s.list("gh_repo_events", func(r *http.Request) (any, error) {
		return r.PathValue("owner") + "/" + r.PathValue("name"), nil
	}))
	route("POST /api/edits", "create_edit", s.createEdit)
	route("POST /api/pages/{wiki}/{title}/touch", "touch_page", s.touchPage)
	return mux
}

type httpErr struct {
	code int
	msg  string
}

func (e httpErr) Error() string { return e.msg }

type argFn func(r *http.Request) (any, error)

func strArg(name, def string) argFn {
	return func(r *http.Request) (any, error) {
		if v := r.URL.Query().Get(name); v != "" {
			return v, nil
		}
		return def, nil
	}
}

func pathArg(name string) argFn {
	return func(r *http.Request) (any, error) { return r.PathValue(name), nil }
}

func intArg(name string, def int64) argFn {
	return func(r *http.Request) (any, error) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return def, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, httpErr{400, "bad " + name}
		}
		return n, nil
	}
}

func timeArg(name string, defOffset time.Duration) argFn {
	return func(r *http.Request) (any, error) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return time.Now().UTC().Add(defOffset), nil
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, httpErr{400, "bad " + name}
		}
		return t, nil
	}
}

func (s *Server) list(name string, args ...argFn) func(http.ResponseWriter, *http.Request) error {
	sql := s.q[name]
	if sql == "" {
		panic("unknown query " + name)
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		vals := make([]any, len(args))
		for i, a := range args {
			v, err := a(r)
			if err != nil {
				return err
			}
			vals[i] = v
		}
		rows, err := s.readPool().Query(r.Context(), sql, vals...)
		if err != nil {
			return err
		}
		out, err := pgx.CollectRows(rows, pgx.RowToMap)
		if err != nil {
			return err
		}
		writeJSON(w, 200, out)
		return nil
	}
}

// feed uses keyset pagination when a cursor is given, else the first page.
func (s *Server) feed(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	var rows pgx.Rows
	var err error
	if bt, bid := q.Get("before_ts"), q.Get("before_id"); bt != "" && bid != "" {
		ts, err1 := time.Parse(time.RFC3339Nano, bt)
		id, err2 := strconv.ParseInt(bid, 10, 64)
		if err1 != nil || err2 != nil {
			return httpErr{400, "bad cursor"}
		}
		rows, err = s.readPool().Query(r.Context(), s.q["feed_page"], ts, id)
	} else {
		rows, err = s.readPool().Query(r.Context(), s.q["feed_first_page"])
	}
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return err
	}
	writeJSON(w, 200, out)
	return nil
}

// EditInput is the body of POST /api/edits.
type EditInput struct {
	Wiki    string  `json:"wiki"`
	Title   string  `json:"title"`
	User    *string `json:"user"`
	Comment *string `json:"comment"`
	OldLen  *int64  `json:"old_len"`
	NewLen  *int64  `json:"new_len"`
}

func (s *Server) createEdit(w http.ResponseWriter, r *http.Request) error {
	var in EditInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Wiki == "" || in.Title == "" {
		return httpErr{400, "need wiki and title"}
	}
	res, err := s.CreateEdit(r.Context(), in)
	if err != nil {
		return err
	}
	writeJSON(w, 201, res)
	return nil
}

// CreateEdit writes an edit and its page aggregate in one transaction on the
// current write target.
func (s *Server) CreateEdit(ctx context.Context, in EditInput) (map[string]any, error) {
	mode := s.Mode()
	if mode == ModeFrozen {
		return nil, httpErr{503, "writes are frozen (cutover/rollback in progress)"}
	}
	var delta *int64
	var ratio *float64
	if in.NewLen != nil {
		d := *in.NewLen
		if in.OldLen != nil {
			d -= *in.OldLen
			if *in.OldLen > 0 {
				x := float64(*in.NewLen) / float64(*in.OldLen)
				ratio = &x
			}
		}
		delta = &d
	}
	eventID := uuid.NewString()
	now := time.Now().UTC()
	pool := s.PG
	if mode == ModeSpanner {
		pool = s.Spanner
	}
	var result map[string]any
	err := s.retryTx(ctx, pool, func(tx pgx.Tx) error {
		var id int64
		insert := s.q["insert_edit"]
		if mode == ModeSpanner {
			// Spanner: ids come from a bit-reversed sequence (no bigserial default).
			insert = insertEditSpanner
		}
		if err := tx.QueryRow(ctx, insert, eventID, in.Wiki, in.Title, in.User, in.Comment, in.OldLen, in.NewLen, delta, ratio, now).Scan(&id); err != nil {
			return fmt.Errorf("insert_edit: %w", err)
		}
		rows, err := tx.Query(ctx, s.q["upsert_page"], in.Wiki, in.Title, in.NewLen, delta, in.User, now)
		if err != nil {
			return fmt.Errorf("upsert_page: %w", err)
		}
		page, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
		if err != nil {
			return fmt.Errorf("upsert_page: %w", err)
		}
		result = map[string]any{"id": id, "event_id": eventID, "mode": mode, "page": page}
		if mode == ModeSpanner {
			edit := map[string]any{"id": id, "event_id": eventID, "wiki": in.Wiki, "page_title": in.Title, "namespace": 0,
				"user_name": in.User, "is_bot": false, "is_minor": false, "change_type": "edit", "comment": in.Comment,
				"old_len": in.OldLen, "new_len": in.NewLen, "size_delta": delta, "growth_ratio": ratio, "rev_new": nil,
				"edit_ts": now, "ingested_at": now}
			if err := Journal(ctx, tx, "edits", edit); err != nil {
				return err
			}
			if err := Journal(ctx, tx, "pages", page); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// insertEditSpanner mirrors queries/app.sql insert_edit with an explicit id.
const insertEditSpanner = `INSERT INTO edits (id, event_id, wiki, page_title, namespace, user_name, is_bot, is_minor, change_type, comment, old_len, new_len, size_delta, growth_ratio, rev_new, edit_ts, ingested_at)
VALUES (nextval('edits_id_seq'), $1, $2, $3, 0, $4, false, false, 'edit', $5, $6, $7, $8, $9, NULL, $10, $10)
RETURNING id`

// Journal records a full row image in the reverse journal (same transaction).
// Values are stored as JSON strings: bit-reversed ids (~4.6e18) and numerics
// would lose precision as JSON numbers; jsonb_populate_record casts the text
// back to each column type exactly during rollback.
func Journal(ctx context.Context, tx pgx.Tx, table string, row map[string]any) error {
	b, err := json.Marshal(TextRow(row))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sc_reverse_journal (id, tbl, row_data, created_at) VALUES ($1, $2, $3::jsonb, spanner.pending_commit_timestamp())`,
		uuid.NewString(), table, string(b))
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	return nil
}

// touchPage is a read-modify-write in one transaction. Under Postgres's default
// READ COMMITTED, concurrent touches can lose updates; Spanner (serializable)
// aborts one transaction instead, which the app must retry.
func (s *Server) touchPage(w http.ResponseWriter, r *http.Request) error {
	wiki, title := r.PathValue("wiki"), r.PathValue("title")
	n, err := s.TouchPage(r.Context(), wiki, title)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"edit_count": n})
	return nil
}

// TouchPage increments edit_count via read-then-write.
func (s *Server) TouchPage(ctx context.Context, wiki, title string) (int64, error) {
	mode := s.Mode()
	if mode == ModeFrozen {
		return 0, httpErr{503, "writes are frozen"}
	}
	pool := s.PG
	if mode == ModeSpanner {
		pool = s.Spanner
	}
	var n int64
	err := s.retryTx(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, s.q["touch_page_read"], wiki, title).Scan(&n); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpErr{404, "no such page"}
			}
			return err
		}
		n++
		if _, err := tx.Exec(ctx, s.q["touch_page_write"], wiki, title, n); err != nil {
			return err
		}
		if mode == ModeSpanner {
			rows, err := tx.Query(ctx, `SELECT wiki, title, edit_count, bytes, avg_delta, last_editor, last_edit_ts FROM pages WHERE wiki = $1 AND title = $2`, wiki, title)
			if err != nil {
				return err
			}
			page, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
			if err != nil {
				return err
			}
			return Journal(ctx, tx, "pages", page)
		}
		return nil
	})
	return n, err
}

// retryTx runs fn in a transaction, retrying serialization failures / aborts.
func (s *Server) retryTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = pgx.BeginFunc(ctx, pool, fn)
		if err == nil || !IsRetryable(err) || s.NoRetry {
			return err
		}
		s.retries.Add(ctx, 1)
		s.RetryCount.Add(1)
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

// IsRetryable reports serialization failures (Postgres 40001) and Spanner
// aborts surfaced by PGAdapter (also 40001, or "ABORTED" text).
func IsRetryable(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "40001" || pe.Code == "40P01"
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// TextRow renders a row image with text values (NULLs and booleans preserved).
func TextRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = textValue(v)
	}
	return out
}

func textValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool:
		return x
	case *string:
		if x == nil {
			return nil
		}
		return *x
	case *int64:
		if x == nil {
			return nil
		}
		return strconv.FormatInt(*x, 10)
	case *float64:
		if x == nil {
			return nil
		}
		return strconv.FormatFloat(*x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case string:
		return x
	case pgtype.Numeric:
		if !x.Valid {
			return nil
		}
		dv, err := x.Value()
		if err != nil || dv == nil {
			return nil
		}
		return fmt.Sprint(dv)
	case Mode:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}
