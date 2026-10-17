package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
)

// SDK metrics recorded through the handler show up in the registry in Prometheus form.
func TestTemporalHandlerExportsToPrometheus(t *testing.T) {
	reg := NewRegistry()
	h, closeFn := TemporalHandler(reg)
	h.WithTags(map[string]string{"task_queue": "collision-llm"}).Counter("activity_execution_failed").Inc(2)
	require.NoError(t, closeFn()) // flushes

	var body string
	require.Eventually(t, func() bool {
		rec := httptest.NewRecorder()
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		b, _ := io.ReadAll(rec.Body)
		body = string(b)
		return strings.Contains(body, "activity_execution_failed")
	}, 3*time.Second, 50*time.Millisecond, body)
	require.Contains(t, body, `task_queue="collision_llm"`, "tally sanitizes label values: dashboards must query collision_llm")
	require.Contains(t, body, "go_goroutines", "runtime metrics are registered too")
}
