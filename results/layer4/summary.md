Explainer: `mock-rules-v1`. Distinct mismatches (query x category): 36.

| category | mismatches | concrete SQL rewrite offered | rewrite gives parity | parity and Postgres result preserved |
|---|---|---|---|---|
| collation | 9 | 0 | 0 | 0 |
| null_semantics | 1 | 1 | 1 | 1 |
| precision | 3 | 0 | 0 | 0 |
| timezone | 4 | 2 | 0 | 0 |
| nondeterministic_order | 4 | 3 | 0 | 0 |
| unsupported | 14 | 2 | 1 | 1 |
| other | 1 | 0 | 0 | 0 |
| **total** | 36 | 8 | 2 | 2 |

| query | category | rewrite | parity | preserved | error |
|---|---|---|---|---|---|
| concat_null | null_semantics | `SELECT id, concat(coalesce(user_name::text, ''), ' / ', coalesce(wiki::text, '')) AS label FROM edits WHERE user_name IS NULL ORDER BY id LIMIT 20` | true | true |  |
| tz_date_trunc | timezone | `SELECT date_trunc('day', edit_ts, 'UTC') AS day, count(*) AS n FROM edits GROUP BY 1 ORDER BY 1` | false | n/a |  |
| limit_without_order | nondeterministic_order | `SELECT id, wiki FROM editsORDER BY id LIMIT 10` | false | n/a | syntax error at or near "id" (SQLSTATE 42601) syntax error at or near "id" |
| unsup_any_array_literal | unsupported | `SELECT count(*) AS n FROM edits WHERE wiki = ANY(ARRAY['enwiki', 'dewiki'])` | false | true |  |
| top_pages | nondeterministic_order | `SELECT wiki, title, edit_count, last_edit_ts FROM pages WHERE wiki = $1 ORDER BY edit_count DESC, wiki, title LIMIT $2` | false | n/a |  |
| recent_edits_by_user | nondeterministic_order | `SELECT id, wiki, page_title, size_delta, edit_ts FROM edits WHERE user_name = $1 ORDER BY edit_ts DESC, id LIMIT 50` | false | n/a |  |
| feed_page | unsupported | `SELECT id, wiki, page_title, user_name, size_delta, edit_ts FROM edits WHERE (edit_ts < $1 OR (edit_ts = $1 AND id < $2)) ORDER BY edit_ts DESC, id DESC LIMIT 25` | true | true |  |
| daily_counts | timezone | `SELECT date_trunc('day', edit_ts, 'UTC') AS day, count(*) AS edits FROM edits WHERE wiki = $1 GROUP BY 1 ORDER BY 1` | false | n/a |  |
