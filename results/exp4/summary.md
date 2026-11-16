| scenario | fault | acked writes | lost | extra | value mismatch | dup deliveries (Kafka) | dup effects | recovery (s) | max lag (s) | NO-GO during fault | false GO samples | false NO-GO samples | gate safe |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | no fault (control) | 2249 | 0 | 0 | 0 | 0 | 0 | 0.1 | 1.0 | false | 0 | 0 | true |
| kafka_partition | Toxiproxy cuts the applier <-> Kafka link | 2250 | 0 | 0 | 0 | 0 | 0 | 6.1 | 26.2 | true | 0 | 0 | true |
| kill_applier | docker kill the applier mid-stream, restart after the fault window | 2250 | 0 | 0 | 0 | 0 | 0 | 1.4 | 22.2 | true | 0 | 1 | true |
| kill_connect | docker kill Kafka Connect (Debezium), restart after the fault window | 2250 | 0 | 0 | 0 | 85 | 0 | 11.2 | 1.0 | true | 0 | 0 | true |
| spanner_latency | Toxiproxy adds 500 ms (+/-100) latency on applier -> Spanner | 2155 | 0 | 0 | 0 | 0 | 0 | 1.1 | 7.7 | true | 0 | 1 | true |
| spanner_partition | Toxiproxy cuts the applier <-> Spanner link | 2249 | 0 | 0 | 0 | 0 | 0 | 15.2 | 33.4 | true | 0 | 0 | true |

**crash_mid_cutover**: app writes acknowledged 64 (by mode map[postgres:54 spanner:10]), rejected while frozen 123, other errors 0; lost in Postgres 0; missing in Spanner after CDC 0; duplicate rows 0; freeze 6.2s (cutover incl. crash + 3s operator delay) / 3.0s (rollback); journal entries replayed 20.

Steps:

       3.2s cutover (crash injected after freeze)
       3.2s crashed; control state = frozen/frozen
       6.2s cutover resumed
       9.4s cutover complete; state = spanner/cutover_done
       9.5s applier paused (emulator single-RW-transaction workaround)
      39.6s rollback (crash injected after freeze)
      39.6s crashed; control state = frozen/rollback_frozen
      42.6s rollback complete; 20 journal entries replayed
      45.8s verified: acked=64 lost=0 missing_in_spanner=0 dupes=0
