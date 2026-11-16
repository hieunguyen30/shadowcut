//go:build ignore

// Generates deploy/grafana/dashboards/shadowcut.json (run: go run scripts/gen-dashboard.go).
package main

import (
	"encoding/json"
	"os"
)

type target struct {
	Expr         string `json:"expr"`
	LegendFormat string `json:"legendFormat"`
	RefID        string `json:"refId"`
}

func main() {
	type panelDef struct {
		title, unit string
		targets     [][2]string
	}
	defs := []panelDef{
		{"Replication lag (applier)", "s", [][2]string{{`max(shadowcut_replication_lag_seconds)`, "lag"}}},
		{"CDC records applied / skipped by offset guard", "ops", [][2]string{{`sum(rate(shadowcut_applier_records_applied_total[1m]))`, "applied/s"}, {`sum(rate(shadowcut_applier_records_skipped_total[1m]))`, "dedup-skipped/s"}, {`sum(rate(shadowcut_applier_errors_total[1m]))`, "errors/s"}}},
		{"Applier batch latency p95", "s", [][2]string{{`histogram_quantile(0.95, sum by (le) (rate(shadowcut_applier_batch_seconds_bucket[5m])))`, "p95"}}},
		{"Shadow reads compared / mismatched", "ops", [][2]string{{`sum(rate(shadowcut_shadow_compared_total[1m]))`, "compared/s"}, {`sum(rate(shadowcut_shadow_mismatches_total[1m]))`, "mismatched/s"}, {`sum(rate(shadowcut_shadow_dropped_total[1m]))`, "dropped/s"}}},
		{"Mismatches by root cause", "short", [][2]string{{`sum by (category) (increase(shadowcut_shadow_mismatches_total[15m]))`, "{{category}}"}}},
		{"Mismatches by query", "short", [][2]string{{`topk(10, sum by (query) (increase(shadowcut_shadow_mismatches_total[15m])))`, "{{query}}"}}},
		{"Spanner shadow query latency p95", "s", [][2]string{{`histogram_quantile(0.95, sum by (le) (rate(shadowcut_shadow_query_seconds_bucket[5m])))`, "p95"}}},
		{"Verifier: rows read / drift", "short", [][2]string{{`sum by (side) (rate(shadowcut_verify_rows_read_total[5m]))`, "rows/s {{side}}"}, {`sum(increase(shadowcut_verify_drift_total[15m]))`, "drift (15m)"}}},
		{"Demo app latency p95 by mode", "s", [][2]string{{`histogram_quantile(0.95, sum by (le, mode) (rate(shadowcut_app_request_seconds_bucket[5m])))`, "{{mode}}"}}},
		{"Demo app requests by status", "reqps", [][2]string{{`sum by (status, mode) (rate(shadowcut_app_requests_total[1m]))`, "{{mode}} {{status}}"}, {`sum(rate(shadowcut_app_txn_retries_total[1m]))`, "txn retries/s"}}},
		{"Ingest: events received / rows written", "ops", [][2]string{{`sum(rate(shadowcut_ingest_events_total[1m]))`, "events/s"}, {`sum(rate(shadowcut_ingest_rows_written_total[1m]))`, "rows/s"}, {`sum(increase(shadowcut_ingest_reconnects_total[15m]))`, "reconnects (15m)"}}},
	}
	var panels []map[string]any
	for i, d := range defs {
		var ts []target
		for j, t := range d.targets {
			ts = append(ts, target{Expr: t[0], LegendFormat: t[1], RefID: string(rune('A' + j))})
		}
		panels = append(panels, map[string]any{
			"id": i + 1, "type": "timeseries", "title": d.title,
			"datasource": map[string]string{"type": "prometheus", "uid": "prom"},
			"gridPos":    map[string]int{"h": 8, "w": 12, "x": (i % 2) * 12, "y": (i / 2) * 8},
			"fieldConfig": map[string]any{"defaults": map[string]any{"unit": d.unit}, "overrides": []any{}},
			"targets":    ts,
		})
	}
	dash := map[string]any{
		"uid": "shadowcut", "title": "Shadowcut", "schemaVersion": 39, "version": 1, "refresh": "5s",
		"time": map[string]string{"from": "now-30m", "to": "now"}, "panels": panels, "tags": []string{"shadowcut"},
	}
	b, _ := json.MarshalIndent(dash, "", "  ")
	os.WriteFile("deploy/grafana/dashboards/shadowcut.json", b, 0o644)
}
