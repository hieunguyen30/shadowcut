// Package gharchive loads hourly GH Archive files (https://www.gharchive.org/)
// into the gh_events table: a second real dataset with a different shape
// (natural bigint ids, jsonb payloads, many NULL orgs).
package gharchive

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is the subset of a GH Archive event we store.
type Event struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Actor struct {
		Login string `json:"login"`
	} `json:"actor"`
	Repo struct {
		Name string `json:"name"`
	} `json:"repo"`
	Org *struct {
		Login string `json:"login"`
	} `json:"org"`
	Public    bool            `json:"public"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// URL returns the archive URL for an hour like "2024-01-01-15".
func URL(hour string) string { return "https://data.gharchive.org/" + hour + ".json.gz" }

// Open returns a reader over a local .json.gz path or a downloaded hour.
func Open(ctx context.Context, src, userAgent string) (io.ReadCloser, error) {
	var raw io.ReadCloser
	if _, err := os.Stat(src); err == nil {
		f, err := os.Open(src)
		if err != nil {
			return nil, err
		}
		raw = f
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL(src), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("gharchive: HTTP %d for %s", resp.StatusCode, URL(src))
		}
		raw = resp.Body
	}
	gz, err := gzip.NewReader(raw)
	if err != nil {
		raw.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{gz, raw}, nil
}

const insertSQL = `INSERT INTO gh_events (id, type, actor_login, repo_name, org_login, is_public, created_at, payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (id) DO NOTHING`

// Load streams events from r into Postgres; max<=0 means all. Returns rows inserted.
func Load(ctx context.Context, pool *pgxpool.Pool, r io.Reader, max int, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	var total int64
	b := &pgx.Batch{}
	flush := func() error {
		if b.Len() == 0 {
			return nil
		}
		br := pool.SendBatch(ctx, b)
		for i := 0; i < b.Len(); i++ {
			tag, err := br.Exec()
			if err != nil {
				br.Close()
				return err
			}
			total += tag.RowsAffected()
		}
		b = &pgx.Batch{}
		return br.Close()
	}
	n := 0
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		id, err := strconv.ParseInt(e.ID, 10, 64)
		if err != nil {
			continue
		}
		var org *string
		if e.Org != nil && e.Org.Login != "" {
			org = &e.Org.Login
		}
		var payload any
		if len(e.Payload) > 0 && len(e.Payload) < 256*1024 {
			payload = string(e.Payload)
		}
		b.Queue(insertSQL, id, e.Type, e.Actor.Login, e.Repo.Name, org, e.Public, e.CreatedAt, payload)
		n++
		if b.Len() >= batchSize {
			if err := flush(); err != nil {
				return total, err
			}
		}
		if max > 0 && n >= max {
			break
		}
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, sc.Err()
}
