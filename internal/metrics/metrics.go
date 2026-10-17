// Package metrics exposes Prometheus metrics: Temporal SDK metrics (workflow and
// activity latency, failures, task queue lag) through the SDK's tally integration,
// and a small server to scrape them from. VictoriaMetrics scrapes it.
package metrics

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/uber-go/tally/v4"
	tallyprom "github.com/uber-go/tally/v4/prometheus"
	"go.temporal.io/sdk/client"
	sdktally "go.temporal.io/sdk/contrib/tally"
)

// NewRegistry returns a registry with Go runtime and process metrics already registered.
func NewRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

// TemporalHandler turns Temporal SDK metrics into Prometheus metrics on reg.
// The returned close function stops the scope's reporting loop.
func TemporalHandler(reg *prometheus.Registry) (client.MetricsHandler, func() error) {
	reporter := tallyprom.NewReporter(tallyprom.Options{Registerer: reg, Gatherer: reg, OnRegisterError: func(error) {}})
	scope, closer := tally.NewRootScope(tally.ScopeOptions{
		CachedReporter:  reporter,
		Separator:       tallyprom.DefaultSeparator,
		SanitizeOptions: &sdktally.PrometheusSanitizeOptions,
		Prefix:          "temporal",
	}, time.Second)
	scope = sdktally.NewPrometheusNamingScope(scope)
	return sdktally.NewMetricsHandler(scope), closer.Close
}

// Serve exposes /metrics on addr until ctx is done. It is a separate port from any
// public listener, so metrics are never reachable through the web proxy.
func Serve(ctx context.Context, addr string, reg *prometheus.Registry) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
