Statements scored: 61 (3 runs each). local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner).

**Overall (query × category): precision 0.63, recall 0.76** (TP 32, FP 19, FN 10).

Baseline: the curated set's hand-written `expect` labels as predictions: precision 0.63, recall 0.70 (TP 19, FP 11, FN 8).

| category | TP | FP | FN | precision | recall |
|---|---|---|---|---|---|
| collation | 8 | 6 | 0 | 0.57 | 1.00 |
| function_semantics | 0 | 2 | 0 | 0.00 | 0.00 |
| nondeterministic_order | 4 | 4 | 0 | 0.50 | 1.00 |
| null_ordering | 0 | 3 | 0 | 0.00 | 0.00 |
| null_semantics | 2 | 0 | 0 | 1.00 | 1.00 |
| other | 0 | 0 | 9 | 0.00 | 0.00 |
| precision | 2 | 1 | 1 | 0.67 | 0.67 |
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
| coll_window_rank | curated | collation, unsupported | unsupported | unobservable (rejected) [collation] |
| coll_upper_unicode | curated | function_semantics | — | FP [function_semantics] |
| null_order_desc | curated | collation, null_ordering | collation | FP [null_ordering] |
| null_order_asc_tail | curated | collation, null_ordering | — | FP [collation null_ordering] |
| null_order_explicit_control | curated | collation | — | FP [collation] |
| concat_null | curated | null_semantics | null_semantics | ✔ |
| concat_ws_null | curated | unsupported | unsupported | ✔ |
| concat_operator_control | curated | — | — | ✔ |
| coalesce_control | curated | — | — | ✔ |
| tz_date_trunc | curated | timezone | other | FP [timezone]; FN [other] |
| tz_text_cast | curated | timezone | timezone | ✔ |
| tz_extract_hour | curated | timezone | other | FP [timezone]; FN [other] |
| tz_date_cast | curated | timezone | — | FP [timezone] |
| tz_utc_explicit_control | curated | — | — | ✔ |
| ts_precision_control | curated | — | — | ✔ |
| float_sum | curated | precision | — | FP [precision] |
| float_avg_by_wiki | curated | collation, precision | precision | FP [collation] |
| numeric_avg | curated | — | other | FN [other] |
| numeric_division | curated | collation | — | FP [collation] |
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
| recent_edits_by_user | app | nondeterministic_order | other | FP [nondeterministic_order]; FN [other] |
| edits_in_range | app | — | — | ✔ |
| search_titles | app | collation, nondeterministic_order | collation | FP [nondeterministic_order] |
| feed_page | app | unsupported | unsupported | ✔ |
| feed_first_page | app | — | other | FN [other] |
| feed_offset | app | nondeterministic_order | other, precision | FP [nondeterministic_order]; FN [other precision] |
| pages_alpha | app | collation | collation | ✔ |
| wiki_stats | app | collation, precision | precision | FP [collation] |
| daily_counts | app | timezone | timezone, other | FN [other] |
| editor_labels | app | null_semantics | null_semantics, other | FN [other] |
| top_editors | app | collation, null_ordering | collation | FP [null_ordering] |
| page_detail | app | — | — | ✔ |
| gh_top_repos | app | nondeterministic_order | nondeterministic_order | ✔ |
| gh_repo_events | app | — | — | ✔ |
| touch_page_read | app | — | — | ✔ |

### Misses and false alarms, with evidence

- **coll_range_count** (rules Q-COLLATION-COMPARE): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. predicted [collation] but never observed in 3 runs.
- **coll_lower_order** (rules Q-COLLATION-ORDER, Q-FUNC-SEMANTICS): predicted [function_semantics] but never observed in 3 runs.
- **coll_upper_unicode** (rules Q-FUNC-SEMANTICS): predicted [function_semantics] but never observed in 3 runs.
- **null_order_desc** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [null_ordering] but never observed in 3 runs.
- **null_order_asc_tail** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [collation null_ordering] but never observed in 3 runs.
- **null_order_explicit_control** (rules Q-COLLATION-ORDER): predicted [collation] but never observed in 3 runs.
- **tz_date_trunc** (rules Q-TZ-DEFAULT): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. predicted [timezone] but never observed in 3 runs.
- **tz_extract_hour** (rules Q-TZ-DEFAULT): missed [other]. Observed: other: 3/3 rows differ persistently (still different after recheck) and could not be attributed. predicted [timezone] but never observed in 3 runs.
- **tz_date_cast** (rules Q-TZ-DEFAULT): predicted [timezone] but never observed in 3 runs.
- **float_sum** (rules Q-FLOAT-AGG): predicted [precision] but never observed in 3 runs.
- **float_avg_by_wiki** (rules Q-COLLATION-ORDER, Q-FLOAT-AGG): predicted [collation] but never observed in 3 runs.
- **numeric_avg** (rules ): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. 
- **numeric_division** (rules Q-COLLATION-ORDER): predicted [collation] but never observed in 3 runs.
- **order_ties_limit** (rules Q-ORDER-TIES): predicted [nondeterministic_order] but never observed in 3 runs.
- **recent_edits_by_user** (rules Q-ORDER-TIES): missed [other]. Observed: other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. predicted [nondeterministic_order] but never observed in 3 runs.
- **search_titles** (rules Q-COLLATION-ORDER, Q-ORDER-TIES, Q-ILIKE): predicted [nondeterministic_order] but never observed in 3 runs.
- **feed_first_page** (rules ): missed [other]. Observed: other: 15/15 rows differ persistently (still different after recheck) and could not be attributed. 
- **feed_offset** (rules Q-ORDER-TIES): missed [other precision]. Observed: other: 12/12 rows differ persistently (still different after recheck) and could not be attributed | precision: id: 97495 vs 97469; page_title: File:Fir Tree Farm - geograph.org.uk - 204355.jpg vs File:MAYERS, Hugh Gordon - Born- (BLANK), Naturalize.... predicted [nondeterministic_order] but never observed in 3 runs.
- **wiki_stats** (rules Q-COLLATION-ORDER, Q-FLOAT-AGG): predicted [collation] but never observed in 3 runs.
- **daily_counts** (rules Q-TZ-DEFAULT): missed [other]. Observed: timezone: day: timestamps differ by 8h0m0s (a whole time-zone offset) | other: 1/1 rows differ persistently (still different after recheck) and could not be attributed. 
- **editor_labels** (rules Q-CONCAT-NULL): missed [other]. Observed: null_semantics: id: 94234 vs 94206; id: 94231 vs 94199; id: 94227 vs 94203 | other: 11/11 rows differ persistently (still different after recheck) and could not be attributed. 
- **top_editors** (rules Q-COLLATION-ORDER, Q-NULL-ORDER): predicted [null_ordering] but never observed in 3 runs.
