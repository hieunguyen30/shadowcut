| scenario | fault | acked writes | lost | extra | value mismatch | dup deliveries (Kafka) | dup effects | recovery (s) | max lag (s) | gate NO-GO during fault | gate correct |
|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | no fault (control) | 2249 | 0 | 0 | 0 | 0 | 0 | 0.0 | 3.8 | false | true |
| crash_mid_cutover |  | 105 | 0 | 0 | 0 | 0 | 0 | 0.0 | 0.0 | false | false |
| kafka_partition | Toxiproxy cuts the applier <-> Kafka link | 2249 | 0 | 0 | 0 | 0 | 0 | 5.0 | 25.3 | true | true |
| kill_applier | docker kill the applier mid-stream, restart after the fault window | 2223 | 0 | 0 | 0 | 0 | 0 | 30.1 | 51.3 | true | true |
| kill_connect | docker kill Kafka Connect (Debezium), restart after the fault window | 2240 | 0 | 0 | 0 | 70 | 0 | 21.8 | 40.9 | true | true |
| spanner_latency | Toxiproxy adds 500 ms (+/-100) latency on applier -> Spanner | 2250 | 0 | 0 | 0 | 0 | 0 | 1.0 | 5.1 | true | false |
| spanner_partition | Toxiproxy cuts the applier <-> Spanner link | 2250 | 0 | 0 | 0 | 0 | 0 | 14.0 | 33.3 | true | true |
