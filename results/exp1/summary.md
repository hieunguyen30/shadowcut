Statements scored: 61 (3 runs each). local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner).

**Overall (query × category): precision 0.73, recall 1.00** (TP 37, FP 14, FN 0).

Baseline: the curated set's hand-written `expect` labels as predictions: precision 0.77, recall 0.79 (TP 23, FP 7, FN 6).

| category | TP | FP | FN | precision | recall |
|---|---|---|---|---|---|
| collation | 10 | 4 | 0 | 0.71 | 1.00 |
| function_semantics | 0 | 2 | 0 | 0.00 | 0.00 |
| nondeterministic_order | 5 | 3 | 0 | 0.62 | 1.00 |
| null_ordering | 0 | 3 | 0 | 0.00 | 0.00 |
| null_semantics | 1 | 1 | 0 | 0.50 | 1.00 |
| precision | 3 | 0 | 0 | 1.00 | 1.00 |
| timezone | 4 | 1 | 0 | 0.80 | 1.00 |
| unsupported | 14 | 0 | 0 | 1.00 | 1.00 |

### Per statement

| statement | source | predicted | observed | result |
|---|---|---|---|---|
| coll_order_title | curated | collation | collation | ✔ |
| coll_min_max | curated | collation | collation | ✔ |
| coll_range_count | curated | collation | collation | ✔ |
| coll_lower_order | curated | collation, function_semantics | collation | FP [function_semantics] |
| coll_string_agg | curated | collation | collation | ✔ |
| coll_window_rank | curated | collation, unsupported | unsupported | unobservable (rejected) [collation] |
| coll_upper_unicode | curated | function_semantics | — | FP [function_semantics] |
| null_order_desc | curated | collation, null_ordering | collation | FP [null_ordering] |
| null_order_asc_tail | curated | collation, null_ordering | collation | FP [null_ordering] |
| null_order_explicit_control | curated | collation | — | FP [collation] |
| concat_null | curated | null_semantics | null_semantics | ✔ |
| concat_ws_null | curated | unsupported | unsupported | ✔ |
| concat_operator_control | curated | — | — | ✔ |
| coalesce_control | curated | — | — | ✔ |
| tz_date_trunc | curated | timezone | timezone | ✔ |
| tz_text_cast | curated | timezone | timezone | ✔ |
| tz_extract_hour | curated | timezone | timezone | ✔ |
| tz_date_cast | curated | timezone | — | FP [timezone] |
| tz_utc_explicit_control | curated | — | — | ✔ |
| ts_precision_control | curated | — | — | ✔ |
| float_sum | curated | precision | precision | ✔ |
| float_avg_by_wiki | curated | collation, precision | precision | FP [collation] |
| numeric_avg | curated | — | — | ✔ |
| numeric_division | curated | collation | timeout | FP [collation] |
| int_avg_control | curated | — | — | ✔ |
| limit_without_order | curated | nondeterministic_order | nondeterministic_order | ✔ |
| order_ties_limit | curated | nondeterministic_order | — | FP [nondeterministic_order] |
| unordered_group_control | curated | — | — | ✔ |
| gh_repo_ties | curated | nondeterministic_order | nondeterministic_order | ✔ |
| unsup_round_numeric | curated | collation, function_semantics, unsupported | unsupported | unobservable (rejected) [collation function_semantics] |
| unsup_distinct_on | curated | collation, nondeterministic_order, unsupported | unsupported | unobservable (rejected) [collation nondeterministic_order] |
| unsup_row_compare | curated | unsupported | unsupported | ✔ |
| unsup_any_array_literal | curated | unsupported | unsupported | ✔ |
| unsup_collate | curated | collation, nondeterministic_order, unsupported | unsupported | unobservable (rejected) [collation nondeterministic_order] |
| unsup_recursive_cte | curated | unsupported | unsupported | ✔ |
| unsup_json_build_object | curated | unsupported | unsupported | ✔ |
| unsup_generate_series_select | curated | unsupported | unsupported | ✔ |
| unsup_for_update_autocommit | curated | unsupported | unsupported | ✔ |
| any_array_constructor_control | curated | — | — | ✔ |
| ilike_operator | curated | — | — | ✔ |
| regex_operator | curated | — | — | ✔ |
| similar_to | curated | unsupported | unsupported | ✔ |
| jsonb_field | curated | — | — | ✔ |
| jsonb_whole | curated | — | — | ✔ |
| length_unicode | curated | unsupported | unsupported | ✔ |
| top_pages | app | nondeterministic_order | nondeterministic_order | ✔ |
| recent_edits_by_user | app | nondeterministic_order | — | FP [nondeterministic_order] |
| edits_in_range | app | — | — | ✔ |
| search_titles | app | collation, nondeterministic_order | collation | FP [nondeterministic_order] |
| feed_page | app | unsupported | unsupported | ✔ |
| feed_first_page | app | — | — | ✔ |
| feed_offset | app | nondeterministic_order | nondeterministic_order | ✔ |
| pages_alpha | app | collation | collation | ✔ |
| wiki_stats | app | collation, precision | precision | FP [collation] |
| daily_counts | app | timezone | timezone | ✔ |
| editor_labels | app | null_semantics | — | FP [null_semantics] |
| top_editors | app | collation, null_ordering | collation | FP [null_ordering] |
| page_detail | app | — | — | ✔ |
| gh_top_repos | app | nondeterministic_order | nondeterministic_order | ✔ |
| gh_repo_events | app | — | — | ✔ |
| touch_page_read | app | — | — | ✔ |

### Misses and false alarms, with evidence

- **coll_lower_order** (rules Q-COLLATION-ORDER, Q-FUNC-SEMANTICS): predicted [function_semantics] but never observed in 3 runs.
- **coll_upper_unicode** (rules Q-FUNC-SEMANTICS): predicted [function_semantics] but never observed in 3 runs.
- **null_order_desc** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [null_ordering] but never observed in 3 runs.
- **null_order_asc_tail** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [null_ordering] but never observed in 3 runs.
- **null_order_explicit_control** (rules Q-COLLATION-ORDER): predicted [collation] but never observed in 3 runs.
- **tz_date_cast** (rules Q-TZ-DEFAULT): predicted [timezone] but never observed in 3 runs.
- **float_avg_by_wiki** (rules Q-COLLATION-ORDER, Q-FLOAT-AGG): predicted [collation] but never observed in 3 runs.
- **numeric_division** (rules Q-COLLATION-ORDER): predicted [collation] but never observed in 3 runs.
- **order_ties_limit** (rules Q-ORDER-TIES): predicted [nondeterministic_order] but never observed in 3 runs.
- **recent_edits_by_user** (rules Q-ORDER-TIES): predicted [nondeterministic_order] but never observed in 3 runs.
- **search_titles** (rules Q-COLLATION-ORDER, Q-ORDER-TIES, Q-ILIKE): predicted [nondeterministic_order] but never observed in 3 runs.
- **wiki_stats** (rules Q-COLLATION-ORDER, Q-FLOAT-AGG): predicted [collation] but never observed in 3 runs.
- **editor_labels** (rules Q-CONCAT-NULL): predicted [null_semantics] but never observed in 3 runs.
- **top_editors** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [null_ordering] but never observed in 3 runs.
