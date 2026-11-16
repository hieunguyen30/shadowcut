Live window 2026-11-16 10:23 to 10:35 UTC. local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner).

**664 reads shadowed, 528 mismatched (79.5%)**, 7417 not shadowed because the queue was full.

| root cause | mismatches | % of all reads | % of mismatches |
|---|---|---|---|
| collation | 73 | 11.0% | 13.8% |
| precision | 27 | 4.1% | 5.1% |
| timezone | 32 | 4.8% | 6.1% |
| nondeterministic_order | 63 | 9.5% | 11.9% |
| replication_lag | 139 | 20.9% | 26.3% |
| missing_row | 9 | 1.4% | 1.7% |
| unsupported | 121 | 18.2% | 22.9% |
| other | 64 | 9.6% | 12.1% |

| query | reads | mismatches | rate | root causes |
|---|---|---|---|---|
| top_pages | 96 | 96 | 100% | timezone 1, nondeterministic_order 28, replication_lag 9, other 58 |
| search_titles | 94 | 93 | 99% | collation 6, missing_row 1, unsupported 86 |
| feed_first_page | 57 | 57 | 100% | replication_lag 55, other 2 |
| pages_alpha | 57 | 48 | 84% | collation 40, missing_row 8 |
| editor_labels | 37 | 37 | 100% | replication_lag 36, other 1 |
| feed_page | 35 | 35 | 100% | unsupported 35 |
| top_editors | 35 | 35 | 100% | collation 27, nondeterministic_order 6, replication_lag 2 |
| daily_counts | 34 | 34 | 100% | timezone 31, other 3 |
| gh_top_repos | 29 | 29 | 100% | nondeterministic_order 29 |
| feed_offset | 28 | 28 | 100% | replication_lag 28 |
| wiki_stats | 27 | 27 | 100% | precision 27 |
| recent_edits_by_user | 69 | 9 | 13% | replication_lag 9 |
| gh_repo_events | 19 | 0 | 0% |  |
| edits_in_range | 47 | 0 | 0% |  |

### One real example per root cause

**collation**: `top_editors` (args [wikidatawiki])

> text sort key "대구버스매니아" vs "BuffyCarrasquillo": PostgreSQL's linguistic collation and Spanner's code-point order disagree (limit)

Postgres rows: `(대구버스매니아 | 17)`<br>Spanner rows: `(BuffyCarrasquillo | 1)`

**missing_row**: `pages_alpha` (args [eswiki Z])

> 0 row(s) only in Postgres, 2 only in Spanner, still different after recheck (persistent drift)

Postgres rows: ``<br>Spanner rows: `(Área marina protegida Namuncurá-Banco Burdwood II | 1) (Óscar Arnulfo Romero | 4)`

**nondeterministic_order**: `gh_top_repos` (args [])

> rows tie on every ORDER BY key; each engine broke the tie differently (order)

Postgres rows: `(dim12512a/Repo5 | 666) (appref5555ix63/Repo3 | 666) (appref5555ix63/Repo4 | 653)`<br>Spanner rows: `(appref5555ix63/Repo3 | 666) (dim12512a/Repo5 | 666) (appref5555ix63/Repo4 | 653)`

**precision**: `wiki_stats` (args [])

> edits: 35156 vs 35119; avg_delta: 951.0884912959381044 vs 951.138215780631567; avg_growth: 1.1726889202283222 vs 1.1727305089356055

Postgres rows: `(wikidatawiki | 35156 | 951.0884912959381044 | 1.1726889202283222 | 39222.92631487669) (commonswiki | 30309 | 1550.1152462964795935 | 1.901498186985819 | 56854.795790875985) (enwiki | 9724 | 158.1356437679967092 | 1.135276264442888 | 9783.810846968809)`<br>Spanner rows: `(wikidatawiki | 35119 | 951.138215780631567 | 1.1727305089356055 | 39180.92630353858) (commonswiki | 30271 | 1549.454989924350038 | 1.9004433272266936 | 56752.939080970755) (enwiki | 9714 | 158.2581840642371834 | 1.1354170801781305 | 9773.670226173346)`

**replication_lag**: `top_pages` (args [zhwiki 20])

> results converged when re-run after the replication-lag bound

Postgres rows: `(zhwiki | Wikipedia:持续出没的破坏者/User:Pasienren | 5 | 2026-11-16T09:59:18.158000Z) (zhwiki | 深夜食堂 (日本版電視劇) | 5 | 2026-11-16T10:05:36.877000Z)`<br>Spanner rows: `(zhwiki | Wikipedia:新条目推荐/候选 | 5 | 2026-11-16T10:03:22.180000Z) (zhwiki | 八仙樂園彩色派對火災的訴訟與賠償 | 5 | 2026-11-16T09:47:26.437000Z)`

**timezone**: `daily_counts` (args [eswiki])

> Postgres reproduces Spanner's result when the session time zone is America/Los_Angeles (counterfactual re-execution)

Postgres rows: `(2026-11-16T00:00:00.000000Z | 717)`<br>Spanner rows: `(2026-11-16T08:00:00.000000Z | 719)`

**unsupported**: `feed_page` (args [2026-11-16T10:23:46.074000Z 105682])

> Spanner rejected the query: Unsupported PostgreSQL expression type: RowCompareExpr

