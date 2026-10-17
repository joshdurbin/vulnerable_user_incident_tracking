package api

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/joshdurbin/fuck_cars/internal/storage"
	"github.com/joshdurbin/fuck_cars/internal/storage/dbgen"
)

// HTTPMetrics records request counts and latency by route pattern (not raw path, so
// cardinality stays bounded).
type HTTPMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	m := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "collision_http_requests_total", Help: "HTTP requests by method, route and status."}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "collision_http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets}, []string{"method", "route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		m.requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		m.duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// PipelineCollector reports pipeline state read from PostgreSQL at scrape time. Reading the
// database (rather than counting in each worker) means the numbers survive restarts and
// are not double counted when an activity retries. Results are cached briefly so a
// scrape storm cannot hammer the database.
type PipelineCollector struct {
	store *storage.Store
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	at      time.Time
	cached  []prometheus.Metric
	lastErr error

	descs map[string]*prometheus.Desc
}

func NewPipelineCollector(st *storage.Store) *PipelineCollector {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc("collision_"+name, help, labels, nil)
	}
	return &PipelineCollector{store: st, ttl: 20 * time.Second, now: time.Now, descs: map[string]*prometheus.Desc{
		"incidents":        d("incidents", "Active incidents (not merged, not withdrawn)."),
		"withdrawn":        d("incidents_withdrawn", "Incidents withdrawn after reprocessing."),
		"newest":           d("newest_incident_created_timestamp_seconds", "When the most recent incident was created."),
		"articles":         d("articles", "Articles by fetch status.", "status"),
		"pending_reviews":  d("pending_reviews", "Possible duplicates waiting for a human decision."),
		"discovered":       d("discovered_urls", "Unique URLs discovered."),
		"extractions":      d("llm_extractions_total", "Classifications stored, by model.", "model"),
		"tokens":           d("llm_tokens_total", "LLM tokens used, by model and direction.", "model", "direction"),
		"cost":             d("llm_cost_usd_total", "LLM spend in USD, by model.", "model"),
		"src_failures":     d("source_consecutive_failures", "Consecutive failed discovery runs.", "source"),
		"src_last_success": d("source_last_success_timestamp_seconds", "Last successful discovery run (0 if never).", "source"),
		"src_enabled":      d("source_enabled", "1 if the source is enabled.", "source"),
		"src_interval":     d("source_poll_interval_seconds", "Configured poll interval.", "source"),
		"up":               d("pipeline_collector_up", "1 if the last database read succeeded."),
	}}
}

func (c *PipelineCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descs {
		ch <- d
	}
}

func (c *PipelineCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Sub(c.at) > c.ttl {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.cached, c.lastErr = c.read(ctx)
		c.at = c.now()
	}
	up := 1.0
	if c.lastErr != nil {
		up = 0
	}
	ch <- prometheus.MustNewConstMetric(c.descs["up"], prometheus.GaugeValue, up)
	for _, m := range c.cached {
		ch <- m
	}
}

func (c *PipelineCollector) read(ctx context.Context) ([]prometheus.Metric, error) {
	q := dbgen.New(c.store.Pool)
	var out []prometheus.Metric
	g := func(name string, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(c.descs[name], prometheus.GaugeValue, v, labels...))
	}
	ctr := func(name string, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(c.descs[name], prometheus.CounterValue, v, labels...))
	}
	counts, err := q.PipelineCounts(ctx)
	if err != nil {
		return nil, err
	}
	g("pending_reviews", float64(counts.PendingReviews))
	g("discovered", float64(counts.DiscoveredUrls))
	inc, err := q.MetricsIncidentTotals(ctx)
	if err != nil {
		return nil, err
	}
	g("incidents", float64(inc.Active))
	g("withdrawn", float64(inc.Withdrawn))
	g("newest", float64(inc.NewestCreatedAt.Unix()))
	arts, err := q.MetricsArticleStatus(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range arts {
		g("articles", float64(a.N), a.Status)
	}
	llm, err := q.MetricsLLMTotals(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range llm {
		ctr("extractions", float64(l.Extractions), l.Model)
		ctr("tokens", float64(l.InputTokens), l.Model, "input")
		ctr("tokens", float64(l.OutputTokens), l.Model, "output")
		ctr("cost", l.CostUsd, l.Model)
	}
	srcs, err := q.MetricsSourceHealth(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range srcs {
		g("src_failures", float64(s.ConsecutiveFailures), s.Slug)
		last := 0.0
		if s.LastSuccessAt != nil {
			last = float64(s.LastSuccessAt.Unix())
		}
		g("src_last_success", last, s.Slug)
		en := 0.0
		if s.Enabled {
			en = 1
		}
		g("src_enabled", en, s.Slug)
		g("src_interval", float64(s.PollIntervalSeconds), s.Slug)
	}
	return out, nil
}
