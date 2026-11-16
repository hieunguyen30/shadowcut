| scenario | fault | acked writes | lost | extra | value mismatch | dup deliveries (Kafka) | dup effects | recovery (s) | max lag (s) | NO-GO during fault | false GO samples | false NO-GO samples | gate safe |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | no fault (control) | 2219 | 0 | 0 | 0 | 0 | 0 | 0.6 | 3.9 | false | 0 | 0 | true |
| kafka_partition | Toxiproxy cuts the applier <-> Kafka link | 2250 | 0 | 0 | 0 | 0 | 0 | 3.1 | 20.4 | true | 0 | 0 | true |
| kill_applier | docker kill the applier mid-stream, restart after the fault window | 2226 | 0 | 0 | 0 | 0 | 0 | 1.6 | 22.8 | true | 0 | 1 | true |
| kill_connect | docker kill Kafka Connect (Debezium), restart after the fault window | 2250 | 0 | 0 | 0 | 114 | 0 | 12.2 | 1.0 | true | 7 | 0 | false |
| spanner_latency | Toxiproxy adds 500 ms (+/-100) latency on applier -> Spanner | 2249 | 0 | 0 | 0 | 0 | 0 | 1.1 | 7.9 | true | 0 | 1 | true |
| spanner_partition | Toxiproxy cuts the applier <-> Spanner link | 2173 | 0 | 0 | 0 | 0 | 0 | 13.2 | 32.0 | true | 0 | 0 | true |
