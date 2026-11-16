| scenario | fault | acked writes | lost | extra | value mismatch | dup deliveries (Kafka) | dup effects | recovery (s) | max lag (s) | gate NO-GO during fault | gate correct |
|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | no fault (control) | 2250 | 0 | 0 | 0 | 0 | 0 | 0.1 | 1.0 | false | true |
| crash_mid_cutover |  | 55 | 0 | 0 | 0 | 0 | 0 | 0.0 | 0.0 | false | false |
| kafka_partition | Toxiproxy cuts the applier <-> Kafka link | 2234 | 0 | 0 | 0 | 0 | 0 | 3.0 | 23.3 | true | true |
| kill_applier | docker kill the applier mid-stream, restart after the fault window | 2241 | 0 | 0 | 0 | 0 | 0 | 1.4 | 19.4 | true | true |
| kill_connect | docker kill Kafka Connect (Debezium), restart after the fault window | 2250 | 0 | 0 | 0 | 85 | 0 | 21.3 | 39.8 | true | false |
| spanner_latency | Toxiproxy adds 500 ms (+/-100) latency on applier -> Spanner | 2247 | 0 | 0 | 0 | 0 | 0 | 1.1 | 5.1 | true | true |
| spanner_partition | Toxiproxy cuts the applier <-> Spanner link | 2249 | 0 | 0 | 0 | 0 | 0 | 9.1 | 27.8 | true | false |

**crash_mid_cutover**: app writes acknowledged 55 (by mode map[postgres:55]), rejected while frozen 141, other errors 0; lost in Postgres 0; missing in Spanner after CDC 0; duplicate rows 0; freeze 7.1s (cutover incl. crash + 3s operator delay) / 2.8s (rollback); journal entries replayed 0.

Steps:

       3.1s cutover (crash injected after freeze)
       3.1s crashed; control state = frozen/frozen
       6.1s cutover resumed
      10.2s cutover complete; state = spanner/cutover_done
      15.2s rollback (crash injected after freeze)
      15.2s crashed; control state = frozen/rollback_frozen
      18.0s rollback complete; 0 journal entries replayed
      21.1s verified: acked=55 lost=0 missing_in_spanner=0 dupes=0
