Explainer: `mock-rules-v1`. Distinct mismatches (query x category): 37.

| category | mismatches | concrete SQL rewrite offered | rewrite gives parity | parity and Postgres result preserved |
|---|---|---|---|---|
| collation | 10 | 0 | 0 | 0 |
| null_semantics | 1 | 1 | 1 | 1 |
| precision | 3 | 0 | 0 | 0 |
| timezone | 4 | 2 | 2 | 2 |
| nondeterministic_order | 4 | 3 | 3 | 3 |
| unsupported | 14 | 2 | 2 | 2 |
| timeout | 1 | 0 | 0 | 0 |
| **total** | 37 | 8 | 8 | 8 |

| query | category | rewrite | parity | preserved | error |
|---|---|---|---|---|---|
| concat_null | null_semantics | `SELECT id, concat(coalesce(user_name::text, ''), ' / ', coalesce(wiki::text, '')) AS label FROM edits WHERE user_name IS NULL ORDER BY id LIMIT 20` | true | true |  |
| tz_date_trunc | timezone | `SELECT date_trunc('day', edit_ts, 'UTC') AS day, count(*) AS n FROM edits GROUP BY 1 ORDER BY 1` | true | n/a |  |
| limit_without_order | nondeterministic_order | `SELECT id, wiki FROM edits ORDER BY id LIMIT 10` | true | n/a |  |
| unsup_any_array_literal | unsupported | `SELECT count(*) AS n FROM edits WHERE wiki = ANY(ARRAY['enwiki', 'dewiki'])` | true | true |  |
| top_pages | nondeterministic_order | `SELECT wiki, title, edit_count, last_edit_ts FROM pages WHERE wiki = $1 ORDER BY edit_count DESC, wiki, title LIMIT $2` | true | n/a |  |
| feed_page | unsupported | `SELECT id, wiki, page_title, user_name, size_delta, edit_ts FROM edits WHERE (edit_ts < $1 OR (edit_ts = $1 AND id < $2)) ORDER BY edit_ts DESC, id DESC LIMIT 25` | true | true |  |
| feed_offset | nondeterministic_order | `SELECT id, wiki, page_title, edit_ts FROM edits ORDER BY edit_ts DESC, id LIMIT 25 OFFSET $1` | true | n/a |  |
| daily_counts | timezone | `SELECT date_trunc('day', edit_ts, 'UTC') AS day, count(*) AS edits FROM edits WHERE wiki = $1 GROUP BY 1 ORDER BY 1` | true | n/a |  |
