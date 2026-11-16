-- Curated hazard query set: hand-written queries designed to trigger (or, for the
-- "control" entries, NOT trigger) each known Postgres -> Spanner (PG interface)
-- hazard against the Shadowcut dataset. Used as ground truth for scanner accuracy
-- (Experiment 1). Annotations:
--   name:   identifier
--   expect: the mismatch category the author EXPECTS (design intent), or "none".
--           Experiment 1 scores the scanner against what is actually OBSERVED by
--           shadow replay, not against this label; the label is reported alongside.

-- ===== collation =====
-- name: coll_order_title
-- expect: collation
SELECT title FROM pages WHERE wiki = 'enwiki' ORDER BY title LIMIT 200;

-- name: coll_min_max
-- expect: collation
SELECT wiki, min(title) AS first_title, max(title) AS last_title FROM pages GROUP BY wiki ORDER BY wiki;

-- name: coll_range_count
-- expect: collation
SELECT count(*) AS n FROM pages WHERE title > 'M';

-- name: coll_lower_order
-- expect: collation
SELECT lower(title) AS t FROM pages WHERE wiki = 'enwiki' ORDER BY t LIMIT 100;

-- name: coll_string_agg
-- expect: collation
SELECT wiki, string_agg(title, ' | ' ORDER BY title) AS titles FROM pages WHERE wiki IN ('dewiki', 'frwiki') GROUP BY wiki ORDER BY wiki;

-- name: coll_window_rank
-- expect: collation
SELECT title, row_number() OVER (ORDER BY title) AS rn FROM pages WHERE wiki = 'commonswiki' ORDER BY rn LIMIT 100;

-- name: coll_upper_unicode
-- expect: none
SELECT id, upper(page_title) AS t FROM edits ORDER BY id LIMIT 200;

-- ===== NULL ordering =====
-- name: null_order_desc
-- expect: null_ordering
SELECT DISTINCT user_name FROM edits WHERE wiki = 'wikidatawiki' ORDER BY user_name DESC LIMIT 5;

-- name: null_order_asc_tail
-- expect: null_ordering
SELECT user_name, count(*) AS n FROM edits GROUP BY user_name ORDER BY user_name LIMIT 100000;

-- name: null_order_explicit_control
-- expect: none
SELECT id, user_name FROM edits WHERE user_name IS NULL OR id < 50 ORDER BY user_name NULLS FIRST, id LIMIT 100;

-- ===== NULL semantics of functions =====
-- name: concat_null
-- expect: null_semantics
SELECT id, concat(user_name, ' / ', wiki) AS label FROM edits WHERE user_name IS NULL ORDER BY id LIMIT 20;

-- name: concat_ws_null
-- expect: null_semantics
SELECT id, concat_ws(' / ', user_name, wiki) AS label FROM edits WHERE user_name IS NULL ORDER BY id LIMIT 20;

-- name: concat_operator_control
-- expect: none
SELECT id, user_name || ' / ' || wiki AS label FROM edits WHERE user_name IS NULL ORDER BY id LIMIT 20;

-- name: coalesce_control
-- expect: none
SELECT id, coalesce(user_name, '(anonymous)') AS who FROM edits ORDER BY id LIMIT 50;

-- ===== time zones =====
-- name: tz_date_trunc
-- expect: timezone
SELECT date_trunc('day', edit_ts) AS day, count(*) AS n FROM edits GROUP BY 1 ORDER BY 1;

-- name: tz_text_cast
-- expect: timezone
SELECT id, edit_ts::text AS ts FROM edits ORDER BY id LIMIT 20;

-- name: tz_extract_hour
-- expect: timezone
SELECT extract(hour FROM edit_ts) AS h, count(*) AS n FROM edits GROUP BY 1 ORDER BY 1;

-- name: tz_date_cast
-- expect: timezone
SELECT edit_ts::date AS d, count(*) AS n FROM edits GROUP BY 1 ORDER BY 1;

-- name: tz_utc_explicit_control
-- expect: none
SELECT date_trunc('day', edit_ts, 'UTC') AS day, count(*) AS n FROM edits GROUP BY 1 ORDER BY 1;

-- name: ts_precision_control
-- expect: none
SELECT id, edit_ts, ingested_at FROM edits ORDER BY id LIMIT 200;

-- ===== numeric / float precision =====
-- name: float_sum
-- expect: precision
SELECT sum(growth_ratio) AS s FROM edits;

-- name: float_avg_by_wiki
-- expect: precision
SELECT wiki, avg(growth_ratio) AS g FROM edits GROUP BY wiki ORDER BY wiki;

-- name: numeric_avg
-- expect: precision
SELECT avg(avg_delta) AS a FROM pages;

-- name: numeric_division
-- expect: none
SELECT wiki, title, bytes::numeric / 3 AS third FROM pages WHERE bytes IS NOT NULL ORDER BY wiki, title LIMIT 50;

-- name: int_avg_control
-- expect: none
SELECT avg(size_delta) AS a FROM edits;

-- ===== nondeterminism =====
-- name: limit_without_order
-- expect: nondeterministic_order
SELECT id, wiki FROM edits LIMIT 10;

-- name: order_ties_limit
-- expect: nondeterministic_order
SELECT title, edit_count FROM pages ORDER BY edit_count DESC LIMIT 10;

-- name: unordered_group_control
-- expect: none
SELECT wiki, count(*) AS n FROM edits GROUP BY wiki;

-- name: gh_repo_ties
-- expect: nondeterministic_order
SELECT repo_name, count(*) AS events FROM gh_events GROUP BY repo_name ORDER BY events DESC LIMIT 20;

-- ===== unsupported features =====
-- name: unsup_round_numeric
-- expect: unsupported
SELECT wiki, title, round(avg_delta, 1) AS r FROM pages ORDER BY wiki, title LIMIT 10;

-- name: unsup_distinct_on
-- expect: unsupported
SELECT DISTINCT ON (wiki) wiki, page_title, edit_ts FROM edits ORDER BY wiki, edit_ts DESC;

-- name: unsup_row_compare
-- expect: unsupported
SELECT id FROM edits WHERE (edit_ts, id) < (now(), 1000000000) ORDER BY edit_ts DESC, id DESC LIMIT 5;

-- name: unsup_any_array_literal
-- expect: unsupported
SELECT count(*) AS n FROM edits WHERE wiki = ANY('{enwiki,dewiki}');

-- name: unsup_collate
-- expect: unsupported
SELECT title FROM pages ORDER BY title COLLATE "C" LIMIT 5;

-- name: unsup_recursive_cte
-- expect: unsupported
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 5) SELECT i FROM n;

-- name: unsup_json_build_object
-- expect: unsupported
SELECT json_build_object('id', id, 'wiki', wiki) AS j FROM edits ORDER BY id LIMIT 5;

-- name: unsup_generate_series_select
-- expect: unsupported
SELECT generate_series(1, 3) AS g;

-- name: unsup_for_update_autocommit
-- expect: unsupported
SELECT id FROM edits ORDER BY id LIMIT 1 FOR UPDATE;

-- name: any_array_constructor_control
-- expect: none
SELECT count(*) AS n FROM edits WHERE wiki = ANY(ARRAY['enwiki', 'dewiki']);

-- ===== operators that may differ =====
-- name: ilike_operator
-- expect: none
SELECT count(*) AS n FROM edits WHERE page_title ILIKE 'a%';

-- name: regex_operator
-- expect: none
SELECT count(*) AS n FROM edits WHERE page_title ~ '^[A-Z]';

-- name: similar_to
-- expect: unsupported
SELECT count(*) AS n FROM edits WHERE page_title SIMILAR TO '[A-Z]%';

-- name: jsonb_field
-- expect: none
SELECT id, payload->>'action' AS action FROM gh_events ORDER BY id LIMIT 50;

-- name: jsonb_whole
-- expect: none
SELECT id, payload FROM gh_events WHERE type = 'PushEvent' ORDER BY id LIMIT 20;

-- name: length_unicode
-- expect: none
SELECT id, length(page_title) AS chars, octet_length(page_title) AS bytes FROM edits ORDER BY id LIMIT 200;
