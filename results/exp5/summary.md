Table `gh_events` (40000 rows at start), 5 injected drifted rows per trial, 1 trials per rate. local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner).

| sample rate | first detection (s) | all detected (s) | rounds to all | expected rounds (1 row) | rows read to all | rows read / round | round time (ms) | trials complete | false drift |
|---|---|---|---|---|---|---|---|---|---|
| 1% | 53.9 | 411.3 | 87 | 50.3 | 138622 | 1593 | 4726 | 1/1 | 0 |
| 3% | 27.0 | not reached | not reached | 16.9 | not reached | 4795 | 13133 | 0/1 | 0 |
| 10% | 42.5 | 172.6 | 4 | 5.3 | 63470 | 15868 | 43146 | 1/1 | 0 |
| 30% | 130.8 | 259.3 | 2 | 2.0 | 95582 | 47791 | 129674 | 1/1 | 0 |
