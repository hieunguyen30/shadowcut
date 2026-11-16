// Package telemetry wires OpenTelemetry metrics to a Prometheus scrape endpoint.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Setup installs a global MeterProvider exporting to Prometheus and, when addr
// (or $METRICS_ADDR) is non-empty, serves /metrics on it. It returns the meter
// for the service and a shutdown func.
func Setup(service, addr string) (metric.Meter, func(context.Context) error) {
	if addr == "" {
		addr = os.Getenv("METRICS_ADDR")
	}
	reg := prometheus.NewRegistry()
	exp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		slog.Warn("telemetry: prometheus exporter failed; metrics disabled", "err", err)
		return otel.Meter(service), func(context.Context) error { return nil }
	}
	res, _ := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(service)))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp), sdkmetric.WithResource(res))
	otel.SetMeterProvider(mp)
	var srv *http.Server
	if addr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		srv = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Warn("telemetry: metrics server stopped", "err", err)
			}
		}()
		slog.Info("metrics endpoint", "addr", addr)
	}
	return mp.Meter(service), func(ctx context.Context) error {
		if srv != nil {
			_ = srv.Shutdown(ctx)
		}
		return mp.Shutdown(ctx)
	}
}

// Must panics on instrument creation errors (they only happen on invalid names).
func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
