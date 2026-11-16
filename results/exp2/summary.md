Live window 2026-11-16 10:37 to 10:49 UTC. local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner).

**678 reads shadowed, 537 mismatched (79.2%)**, 7280 not shadowed because the queue was full.

| root cause | mismatches | % of all reads | % of mismatches |
|---|---|---|---|
| collation | 86 | 12.7% | 16.0% |
| precision | 20 | 2.9% | 3.7% |
| timezone | 35 | 5.2% | 6.5% |
| nondeterministic_order | 90 | 13.3% | 16.8% |
| replication_lag | 157 | 23.2% | 29.2% |
| missing_row | 6 | 0.9% | 1.1% |
| unsupported | 28 | 4.1% | 5.2% |
| timeout | 88 | 13.0% | 16.4% |
| other | 27 | 4.0% | 5.0% |

| query | reads | mismatches | rate | root causes |
|---|---|---|---|---|
| top_pages | 113 | 113 | 100% | nondeterministic_order 77, replication_lag 10, other 26 |
| search_titles | 84 | 84 | 100% | timeout 84 |
| pages_alpha | 86 | 66 | 77% | collation 56, replication_lag 4, missing_row 6 |
| feed_first_page | 62 | 62 | 100% | replication_lag 62 |
| daily_counts | 39 | 39 | 100% | timezone 34, timeout 4, other 1 |
| top_editors | 37 | 37 | 100% | collation 30, timezone 1, replication_lag 6 |
| editor_labels | 32 | 32 | 100% | replication_lag 32 |
| feed_offset | 32 | 32 | 100% | replication_lag 32 |
| feed_page | 28 | 28 | 100% | unsupported 28 |
| wiki_stats | 20 | 20 | 100% | precision 20 |
| gh_top_repos | 13 | 13 | 100% | nondeterministic_order 13 |
| recent_edits_by_user | 82 | 11 | 13% | replication_lag 11 |
| gh_repo_events | 20 | 0 | 0% |  |
| edits_in_range | 30 | 0 | 0% |  |

### One real example per root cause

**collation**: `pages_alpha` (args [eswiki Ö])

> text sort key "Obligación de servicio público" vs "Žalgiris B Vilnius": PostgreSQL's linguistic collation and Spanner's code-point order disagree (limit)

Postgres rows: `(Obligación de servicio público | 1) (Oihan Sancet | 2) (Ooquiste | 1)`<br>Spanner rows: `(Žalgiris B Vilnius | 1)`

**missing_row**: `pages_alpha` (args [wikidatawiki Ö])

> 50 row(s) only in Postgres, 0 only in Spanner, still different after recheck (persistent drift)

Postgres rows: `(Property:P10844 | 1) (Property:P217 | 1) (Property:P962 | 1)`<br>Spanner rows: ``

**nondeterministic_order**: `gh_top_repos` (args [])

> rows tie on every ORDER BY key; each engine broke the tie differently (order)

Postgres rows: `(dim12512a/Repo5 | 666) (appref5555ix63/Repo3 | 666) (appref5555ix63/Repo4 | 653)`<br>Spanner rows: `(appref5555ix63/Repo3 | 666) (dim12512a/Repo5 | 666) (appref5555ix63/Repo4 | 653)`

**precision**: `wiki_stats` (args [])

> edits: 40731 vs 40727; avg_delta: 941.3835162407011858 vs 941.4676504530164264; avg_growth: 1.1609798501046775 vs 1.1610136807390177

Postgres rows: `(wikidatawiki | 40731 | 941.3835162407011858 | 1.1609798501046775 | 45176.04792727321) (commonswiki | 35262 | 1530.5261754863592536 | 1.895304519647136 | 65962.28319727928) (enwiki | 11116 | 166.1220762864339691 | 1.1434450103770797 | 11358.98273308591)`<br>Spanner rows: `(wikidatawiki | 40727 | 941.4676504530164264 | 1.1610136807390177 | 45172.7202901937) (commonswiki | 35254 | 1530.6995801894820446 | 1.895409704293392 | 65950.78066088857) (enwiki | 11116 | 166.1220762864339691 | 1.1434450103770817 | 11358.98273308593)`

**replication_lag**: `editor_labels` (args [])

> results converged when re-run after the replication-lag bound

Postgres rows: `(123376 | Dev Jadiya @ wikidatawiki) (123375 | BlueCrowns @ enwiki) (123374 | InternetArchiveBot @ bnwiki)`<br>Spanner rows: `(123467 | Printstream @ wikidatawiki) (123466 | GreenC bot @ enwiki) (123465 | PMG @ commonswiki)`

**timeout**: `search_titles` (args [B])

> Spanner shadow query timed out: timeout: context deadline exceeded

**timezone**: `daily_counts` (args [dewiki])

> Postgres reproduces Spanner's result when the session time zone is America/Los_Angeles (counterfactual re-execution)

Postgres rows: `(2026-11-16T00:00:00.000000Z | 2178)`<br>Spanner rows: `(2026-11-16T08:00:00.000000Z | 2181)`

**unsupported**: `daily_counts` (args [commonswiki])

> Spanner rejected the query: context canceled

