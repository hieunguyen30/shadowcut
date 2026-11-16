Statements scored: 61 (3 runs each). local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner).

**Overall (query × category): precision 0.62, recall 0.67** (TP 36, FP 22, FN 18).

Baseline: the curated set's hand-written `expect` labels as predictions: precision 0.73, recall 0.55 (TP 22, FP 8, FN 18).

| category | TP | FP | FN | precision | recall |
|---|---|---|---|---|---|
| collation | 9 | 9 | 0 | 0.50 | 1.00 |
| function_semantics | 0 | 3 | 0 | 0.00 | 0.00 |
| nondeterministic_order | 6 | 4 | 1 | 0.60 | 0.86 |
| null_ordering | 0 | 3 | 0 | 0.00 | 0.00 |
| null_semantics | 2 | 0 | 0 | 1.00 | 1.00 |
| other | 0 | 0 | 17 | 0.00 | 0.00 |
| precision | 3 | 0 | 0 | 1.00 | 1.00 |
| timezone | 2 | 3 | 0 | 0.40 | 1.00 |
| unsupported | 14 | 0 | 0 | 1.00 | 1.00 |

### Per statement

| statement | source | predicted | observed | result |
|---|---|---|---|---|
| coll_order_title | curated | collation | collation | ✔ |
| coll_min_max | curated | collation | collation | ✔ |
| coll_range_count | curated | collation | other | FP [collation]; FN [other] |
| coll_lower_order | curated | collation, function_semantics | collation | FP [function_semantics] |
| coll_string_agg | curated | collation | collation | ✔ |
| coll_window_rank | curated | collation, unsupported | unsupported | FP [collation] |
| coll_upper_unicode | curated | function_semantics | — | FP [function_semantics] |
| null_order_desc | curated | collation, null_ordering | collation | FP [null_ordering] |
| null_order_asc_tail | curated | collation, null_ordering | nondeterministic_order, collation | FP [null_ordering]; FN [nondeterministic_order] |
| null_order_explicit_control | curated | collation | — | FP [collation] |
| concat_null | curated | null_semantics | null_semantics | ✔ |
| concat_ws_null | curated | unsupported | unsupported | ✔ |
| concat_operator_control | curated | — | — | ✔ |
| coalesce_control | curated | — | — | ✔ |
| tz_date_trunc | curated | timezone | other, timezone | FN [other] |
| tz_text_cast | curated | timezone | timezone | ✔ |
| tz_extract_hour | curated | timezone | other | FP [timezone]; FN [other] |
| tz_date_cast | curated | timezone | other | FP [timezone]; FN [other] |
| tz_utc_explicit_control | curated | — | other | FN [other] |
| ts_precision_control | curated | — | — | ✔ |
| float_sum | curated | precision | other, precision | FN [other] |
| float_avg_by_wiki | curated | collation, precision | precision | FP [collation] |
| numeric_avg | curated | — | other | FN [other] |
| numeric_division | curated | collation | — | FP [collation] |
| int_avg_control | curated | — | other | FN [other] |
| limit_without_order | curated | nondeterministic_order | nondeterministic_order | ✔ |
| order_ties_limit | curated | nondeterministic_order | other, nondeterministic_order | FN [other] |
| unordered_group_control | curated | — | other | FN [other] |
| gh_repo_ties | curated | nondeterministic_order | nondeterministic_order | ✔ |
| unsup_round_numeric | curated | collation, function_semantics, unsupported | unsupported | FP [collation function_semantics] |
| unsup_distinct_on | curated | collation, nondeterministic_order, unsupported | unsupported | FP [collation nondeterministic_order] |
| unsup_row_compare | curated | unsupported | unsupported | ✔ |
| unsup_any_array_literal | curated | unsupported | unsupported | ✔ |
| unsup_collate | curated | collation, nondeterministic_order, unsupported | unsupported | FP [collation nondeterministic_order] |
| unsup_recursive_cte | curated | unsupported | unsupported | ✔ |
| unsup_json_build_object | curated | unsupported | unsupported | ✔ |
| unsup_generate_series_select | curated | unsupported | unsupported | ✔ |
| unsup_for_update_autocommit | curated | unsupported | unsupported | ✔ |
| any_array_constructor_control | curated | — | other | FN [other] |
| ilike_operator | curated | — | — | ✔ |
| regex_operator | curated | — | other | FN [other] |
| similar_to | curated | unsupported | unsupported | ✔ |
| jsonb_field | curated | — | — | ✔ |
| jsonb_whole | curated | — | — | ✔ |
| length_unicode | curated | unsupported | unsupported | ✔ |
| top_pages | app | nondeterministic_order | nondeterministic_order | ✔ |
| recent_edits_by_user | app | nondeterministic_order | other | FP [nondeterministic_order]; FN [other] |
| edits_in_range | app | — | — | ✔ |
| search_titles | app | collation, nondeterministic_order | collation | FP [nondeterministic_order] |
| feed_page | app | unsupported | unsupported | ✔ |
| feed_first_page | app | — | other | FN [other] |
| feed_offset | app | nondeterministic_order | nondeterministic_order, other | FN [other] |
| pages_alpha | app | collation | collation | ✔ |
| wiki_stats | app | collation, precision | precision | FP [collation] |
| daily_counts | app | timezone | other | FP [timezone]; FN [other] |
| editor_labels | app | null_semantics | null_semantics, other | FN [other] |
| top_editors | app | collation, null_ordering | collation | FP [null_ordering] |
| page_detail | app | — | — | ✔ |
| gh_top_repos | app | nondeterministic_order | nondeterministic_order | ✔ |
| gh_repo_events | app | — | — | ✔ |
| touch_page_read | app | — | — | ✔ |

### Misses and false alarms, with evidence

- **coll_range_count** (rules Q-COLLATION-COMPARE): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. predicted [collation] but never observed in 3 runs.
- **coll_lower_order** (rules Q-COLLATION-ORDER, Q-FUNC-SEMANTICS): predicted [function_semantics] but never observed in 3 runs.
- **coll_window_rank** (rules Q-COLLATION-ORDER, Q-UNSUP-SYNTAX): predicted [collation] but never observed in 3 runs.
- **coll_upper_unicode** (rules Q-FUNC-SEMANTICS): predicted [function_semantics] but never observed in 3 runs.
- **null_order_desc** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [null_ordering] but never observed in 3 runs.
- **null_order_asc_tail** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): missed [nondeterministic_order]. Observed: nondeterministic_order: rows tie on every ORDER BY key; each engine broke the tie differently (limit) | collation: text sort key "리듬" vs "%Pier%": PostgreSQL's linguistic collation and Spanner's code-point order disagree (order). predicted [null_ordering] but never observed in 3 runs.
- **null_order_explicit_control** (rules Q-COLLATION-ORDER): predicted [collation] but never observed in 3 runs.
- **tz_date_trunc** (rules Q-TZ-DEFAULT): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed | timezone: day: timestamps differ by 8h0m0s (a whole time-zone offset). 
- **tz_extract_hour** (rules Q-TZ-DEFAULT): missed [other]. Observed: other: 2/2 rows differ persistently (still different after recheck) and could not be attributed. predicted [timezone] but never observed in 3 runs.
- **tz_date_cast** (rules Q-TZ-DEFAULT): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. predicted [timezone] but never observed in 3 runs.
- **tz_utc_explicit_control** (rules ): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. 
- **float_sum** (rules Q-FLOAT-AGG): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed | precision: s: 96568.27775237974 vs 96568.27775237976 (relative difference 1.5e-16). 
- **float_avg_by_wiki** (rules Q-COLLATION-ORDER, Q-FLOAT-AGG): predicted [collation] but never observed in 3 runs.
- **numeric_avg** (rules ): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. 
- **numeric_division** (rules Q-COLLATION-ORDER): predicted [collation] but never observed in 3 runs.
- **int_avg_control** (rules ): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. 
- **order_ties_limit** (rules Q-ORDER-TIES): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed | nondeterministic_order: rows tie on every ORDER BY key; each engine broke the tie differently (order). 
- **unordered_group_control** (rules ): missed [other]. Observed: other: 8/8 rows differ persistently (still different after recheck) and could not be attributed. 
- **unsup_round_numeric** (rules Q-COLLATION-ORDER, Q-FUNC-SEMANTICS, Q-UNSUP-FUNC): predicted [collation function_semantics] but never observed in 3 runs.
- **unsup_distinct_on** (rules Q-COLLATION-ORDER, Q-ORDER-TIES, Q-UNSUP-SYNTAX): predicted [collation nondeterministic_order] but never observed in 3 runs.
- **unsup_collate** (rules Q-COLLATION-ORDER, Q-ORDER-TIES, Q-UNSUP-SYNTAX): predicted [collation nondeterministic_order] but never observed in 3 runs.
- **any_array_constructor_control** (rules ): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. 
- **regex_operator** (rules ): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. 
- **recent_edits_by_user** (rules Q-ORDER-TIES): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. predicted [nondeterministic_order] but never observed in 3 runs.
- **search_titles** (rules Q-COLLATION-ORDER, Q-ORDER-TIES, Q-ILIKE): predicted [nondeterministic_order] but never observed in 3 runs.
- **feed_first_page** (rules ): missed [other]. Observed: other: 25/25 rows differ persistently (still different after recheck) and could not be attributed. 
- **feed_offset** (rules Q-ORDER-TIES): missed [other]. Observed: nondeterministic_order: rows tie on every ORDER BY key; each engine broke the tie differently (order) | other: 13/13 rows differ persistently (still different after recheck) and could not be attributed. 
- **wiki_stats** (rules Q-COLLATION-ORDER, Q-FLOAT-AGG): predicted [collation] but never observed in 3 runs.
- **daily_counts** (rules Q-TZ-DEFAULT): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. predicted [timezone] but never observed in 3 runs.
- **editor_labels** (rules Q-CONCAT-NULL): missed [other]. Observed: null_semantics: id: 69509 vs 69482; label: concat returns NULL on one engine ( @ kowiki vs NULL): NULL handling of concat() differs | other: 26/26 rows differ persistently (still different after recheck) and could not be attributed. 
- **top_editors** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [null_ordering] but never observed in 3 runs.
