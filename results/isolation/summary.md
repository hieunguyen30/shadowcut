8 concurrent workers x 3 read-modify-write increments of one row (`touch_page`). local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner).

| variant | succeeded | failed | retries | expected increment | actual increment | lost updates | seconds |
|---|---|---|---|---|---|---|---|
| postgres_read_committed | 24 | 0 | 0 | 24 | 5 | 19 | 0.1 |
| spanner_with_retry | 17 | 7 | 86 | 17 | 17 | 0 | 24.5 |
| spanner_no_retry | 2 | 22 | 0 | 2 | 2 | 0 | 4.7 |

Error sample (spanner_with_retry): `The transaction was aborted and could not be retried due to a concurrent modification (SQLSTATE 40001)`

Error sample (spanner_no_retry): `The transaction was aborted and could not be retried due to a concurrent modification (SQLSTATE 40001)`
