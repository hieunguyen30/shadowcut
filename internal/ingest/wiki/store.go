package wiki

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InsertEditSQL inserts one change; duplicates (same event id after a reconnect
// with Last-Event-ID) are ignored.
const InsertEditSQL = `INSERT INTO edits (event_id, wiki, page_title, namespace, user_name, is_bot, is_minor,
  change_type, comment, old_len, new_len, size_delta, growth_ratio, rev_new, edit_ts)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (event_id) DO NOTHING`

// UpsertPageSQL maintains the per-page aggregate (this produces UPDATE CDC events).
// avg_delta is a running mean stored as numeric(14,4): Postgres rounds it to 4
// decimal places on every write -- a behaviour the Spanner schema cannot enforce.
const UpsertPageSQL = `INSERT INTO pages AS p (wiki, title, edit_count, bytes, avg_delta, last_editor, last_edit_ts)
VALUES ($1, $2, 1, $3, $4, $5, $6)
ON CONFLICT (wiki, title) DO UPDATE SET
  edit_count   = p.edit_count + 1,
  bytes        = COALESCE(EXCLUDED.bytes, p.bytes),
  avg_delta    = (COALESCE(p.avg_delta, 0) * p.edit_count + COALESCE(EXCLUDED.avg_delta, 0)) / (p.edit_count + 1),
  last_editor  = EXCLUDED.last_editor,
  last_edit_ts = GREATEST(p.last_edit_ts, EXCLUDED.last_edit_ts)`

// Store writes edits to Postgres in batches.
type Store struct{ Pool *pgxpool.Pool }

// WriteBatch writes edits and page upserts in one transaction. It returns the
// number of new edit rows.
func (s *Store) WriteBatch(ctx context.Context, edits []Edit) (int64, error) {
	if len(edits) == 0 {
		return 0, nil
	}
	var inserted int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		for i := range edits {
			e := &edits[i]
			b.Queue(InsertEditSQL, e.EventID, e.Wiki, e.Title, e.Namespace, e.UserName, e.IsBot, e.IsMinor,
				e.ChangeType, e.Comment, e.OldLen, e.NewLen, e.SizeDelta, e.GrowthRatio, e.RevNew, e.EditTS)
		}
		br := tx.SendBatch(ctx, b)
		newRows := make([]bool, len(edits))
		for i := range edits {
			tag, err := br.Exec()
			if err != nil {
				br.Close()
				return err
			}
			newRows[i] = tag.RowsAffected() == 1
			inserted += tag.RowsAffected()
		}
		if err := br.Close(); err != nil {
			return err
		}
		// Page upserts (the ingestor has a single writer, so no lock-order concerns).
		b = &pgx.Batch{}
		for i := range edits {
			if !newRows[i] {
				continue
			}
			e := &edits[i]
			b.Queue(UpsertPageSQL, e.Wiki, e.Title, e.NewLen, e.SizeDelta, e.UserName, e.EditTS)
		}
		if b.Len() == 0 {
			return nil
		}
		return tx.SendBatch(ctx, b).Close()
	})
	return inserted, err
}
