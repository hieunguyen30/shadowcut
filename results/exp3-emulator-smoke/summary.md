EMULATOR smoke test (functional only; throughput/latency NOT meaningful); 4 writers x 20s per strategy, one row per transaction.

| key strategy | commits/s | p50 ms | p95 ms | p99 ms | errors | retries | ORDER BY id DESC returns newest rows |
|---|---|---|---|---|---|---|---|
| sequential | 89.4 | 34.2 | 115.1 | 176.1 | 0 | 0 | 95% |
| bit_reversed_sequence | 85.3 | 37.6 | 116.6 | 169.4 | 0 | 0 | 0% |
| uuid_v4 | 84.4 | 36.9 | 120.9 | 178.3 | 0 | 0 | 0% |
