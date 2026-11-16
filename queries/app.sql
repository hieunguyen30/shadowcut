-- Demo app queries. The Go demo app embeds this file and executes these exact
-- statements; `shadowcut scan` reads the same file. Annotations:
--   name:  query identifier used by the app
--   route: HTTP route that runs it

-- name: top_pages
-- route: GET /api/pages/top
SELECT wiki, title, edit_count, last_edit_ts
FROM pages
WHERE wiki = $1
ORDER BY edit_count DESC
LIMIT $2;

-- name: recent_edits_by_user
-- route: GET /api/users/{user}/edits
SELECT id, wiki, page_title, size_delta, edit_ts
FROM edits
WHERE user_name = $1
ORDER BY edit_ts DESC
LIMIT 50;

-- name: edits_in_range
-- route: GET /api/edits
SELECT id, wiki, page_title, user_name, edit_ts
FROM edits
WHERE edit_ts >= $1 AND edit_ts < $2
ORDER BY edit_ts, id
LIMIT 500;

-- name: search_titles
-- route: GET /api/search
SELECT DISTINCT page_title, wiki
FROM edits
WHERE page_title ILIKE $1 || '%'
ORDER BY page_title
LIMIT 50;

-- name: feed_page
-- route: GET /api/feed  (keyset pagination)
SELECT id, wiki, page_title, user_name, size_delta, edit_ts
FROM edits
WHERE (edit_ts, id) < ($1, $2)
ORDER BY edit_ts DESC, id DESC
LIMIT 25;

-- name: feed_first_page
-- route: GET /api/feed  (first page)
SELECT id, wiki, page_title, user_name, size_delta, edit_ts
FROM edits
ORDER BY edit_ts DESC, id DESC
LIMIT 25;

-- name: feed_offset
-- route: GET /api/feed/offset
SELECT id, wiki, page_title, edit_ts
FROM edits
ORDER BY edit_ts DESC
LIMIT 25 OFFSET $1;

-- name: pages_alpha
-- route: GET /api/pages/alpha  (A-Z browse)
SELECT title, edit_count
FROM pages
WHERE wiki = $1 AND title > $2
ORDER BY title
LIMIT 50;

-- name: wiki_stats
-- route: GET /api/stats/wikis
SELECT wiki, count(*) AS edits, avg(size_delta) AS avg_delta, avg(growth_ratio) AS avg_growth, sum(growth_ratio) AS sum_growth
FROM edits
GROUP BY wiki
ORDER BY edits DESC, wiki;

-- name: daily_counts
-- route: GET /api/stats/daily
SELECT date_trunc('day', edit_ts) AS day, count(*) AS edits
FROM edits
WHERE wiki = $1
GROUP BY 1
ORDER BY 1;

-- name: editor_labels
-- route: GET /api/editors/recent
SELECT id, concat(user_name, ' @ ', wiki) AS label
FROM edits
ORDER BY id DESC
LIMIT 50;

-- name: top_editors
-- route: GET /api/editors/top
SELECT user_name, count(*) AS edits
FROM edits
WHERE wiki = $1
GROUP BY user_name
ORDER BY user_name
LIMIT 100;

-- name: page_detail
-- route: GET /api/pages/{wiki}/{title}
SELECT wiki, title, edit_count, bytes, avg_delta, last_editor, last_edit_ts
FROM pages
WHERE wiki = $1 AND title = $2;

-- name: gh_top_repos
-- route: GET /api/gh/repos/top
SELECT repo_name, count(*) AS events
FROM gh_events
GROUP BY repo_name
ORDER BY events DESC
LIMIT 20;

-- name: gh_repo_events
-- route: GET /api/gh/repos/{repo}
SELECT id, type, actor_login, org_login, created_at, payload
FROM gh_events
WHERE repo_name = $1
ORDER BY created_at DESC, id DESC
LIMIT 20;

-- name: insert_edit
-- txn: create_edit
-- route: POST /api/edits
INSERT INTO edits (event_id, wiki, page_title, namespace, user_name, is_bot, is_minor, change_type, comment, old_len, new_len, size_delta, growth_ratio, rev_new, edit_ts)
VALUES ($1, $2, $3, 0, $4, false, false, 'edit', $5, $6, $7, $8, $9, NULL, $10)
RETURNING id;

-- name: upsert_page
-- txn: create_edit
-- route: POST /api/edits
INSERT INTO pages AS p (wiki, title, edit_count, bytes, avg_delta, last_editor, last_edit_ts)
VALUES ($1, $2, 1, $3, $4, $5, $6)
ON CONFLICT (wiki, title) DO UPDATE SET
  edit_count   = p.edit_count + 1,
  bytes        = COALESCE(EXCLUDED.bytes, p.bytes),
  avg_delta    = (COALESCE(p.avg_delta, 0) * p.edit_count + COALESCE(EXCLUDED.avg_delta, 0)) / (p.edit_count + 1),
  last_editor  = EXCLUDED.last_editor,
  last_edit_ts = GREATEST(p.last_edit_ts, EXCLUDED.last_edit_ts)
RETURNING wiki, title, edit_count, bytes, avg_delta, last_editor, last_edit_ts;

-- name: touch_page_read
-- txn: touch_page
-- route: POST /api/pages/{wiki}/{title}/touch  (read-modify-write in one transaction)
SELECT edit_count FROM pages WHERE wiki = $1 AND title = $2;

-- name: touch_page_write
-- txn: touch_page
-- route: POST /api/pages/{wiki}/{title}/touch
UPDATE pages SET edit_count = $3, last_edit_ts = now() WHERE wiki = $1 AND title = $2;
