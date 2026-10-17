package gdelt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
)

var q = discovery.Query{Family: "pedestrian", Name: "ped-struck", Text: `(pedestrian OR walker) (struck OR killed)`}

func win(h int) discovery.Window {
	until := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	return discovery.Window{Since: until.Add(-time.Duration(h) * time.Hour), Until: until}
}

func fast() Option { return WithLimiter(rate.NewLimiter(rate.Inf, 1)) }

func serve(t *testing.T, h http.HandlerFunc) *Provider {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.Client(), WithBaseURL(srv.URL), fast(), WithBackoff(5*time.Millisecond, 5*time.Millisecond))
}

func TestParsesRealResponseFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/artlist.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotQuery, gotStart string
	p := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotStart = r.URL.Query().Get("query"), r.URL.Query().Get("startdatetime")
		w.Write(body)
	})
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(24)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 12 || res.Truncated || res.Requests != 1 {
		t.Fatalf("%d candidates, truncated=%v requests=%d", len(res.Candidates), res.Truncated, res.Requests)
	}
	c := res.Candidates[0]
	if !strings.HasPrefix(c.URL, "https://") || c.Title == "" || c.Publisher == "" || c.PublishedAt == nil || c.Language != "English" || c.QueryName != "ped-struck" {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(gotQuery, "sourcecountry:US") || !strings.Contains(gotQuery, "sourcelang:english") || !strings.Contains(gotQuery, "(pedestrian OR walker)") {
		t.Fatalf("query %q", gotQuery)
	}
	if gotStart != "20261016000000" {
		t.Fatalf("start %q", gotStart)
	}
}

// respond returns n distinct articles.
func respond(w http.ResponseWriter, n int, tag string) {
	arts := make([]article, n)
	for i := range arts {
		arts[i] = article{URL: fmt.Sprintf("https://n.example.com/%s/%d", tag, i), Title: "t", SeenDate: "20261016T010000Z"}
	}
	json.NewEncoder(w).Encode(map[string]any{"articles": arts})
}

func TestWindowSplitsWhenCapped(t *testing.T) {
	var mu sync.Mutex
	var spans []time.Duration
	p := serve(t, func(w http.ResponseWriter, r *http.Request) {
		s, _ := time.Parse(timeLayout, r.URL.Query().Get("startdatetime"))
		e, _ := time.Parse(timeLayout, r.URL.Query().Get("enddatetime"))
		mu.Lock()
		spans = append(spans, e.Sub(s))
		mu.Unlock()
		if e.Sub(s) > 12*time.Hour { // the full 24h window overflows; halves do not
			respond(w, maxRecords, "full")
			return
		}
		respond(w, 40, s.Format("150405"))
	})
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(24)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Requests != 3 || len(res.Candidates) != 80 || res.Truncated {
		t.Fatalf("requests=%d candidates=%d truncated=%v spans=%v", res.Requests, len(res.Candidates), res.Truncated, spans)
	}
}

func TestSplitStopsAtMinWindowAndReportsTruncation(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; respond(w, maxRecords, fmt.Sprint(n)) }))
	defer srv.Close()
	p := New(srv.Client(), WithBaseURL(srv.URL), fast(), WithMinWindow(3*time.Hour))
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(24)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Warnings) == 0 {
		t.Fatalf("expected truncation warning: %+v", res)
	}
	if n > 64 {
		t.Fatalf("splitting must be bounded, made %d requests", n)
	}
}

func TestPlainTextQueryErrorWith200(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Queries containing OR'd terms must be surrounded by ()."))
	})
	_, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(1)})
	if err == nil || !strings.Contains(err.Error(), "query rejected") {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyResultIsNotAnError(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) })
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(1)})
	if err != nil || len(res.Candidates) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRateLimited429(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte("Please limit requests to one every 5 seconds."))
	})
	_, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(1)})
	var rl *discovery.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter == 0 {
		t.Fatalf("got %v", err)
	}
}

func TestPartialFailureKeepsGoodQueries(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "bad") {
			w.Write([]byte("Your query was too short."))
			return
		}
		respond(w, 3, "ok")
	})
	res, err := p.Discover(context.Background(), discovery.Request{
		Queries: []discovery.Query{{Name: "bad", Text: "bad"}, {Name: "good", Text: "good term"}}, Window: win(1)})
	if err != nil || len(res.Candidates) != 3 || len(res.Warnings) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestAllQueriesFailIsAnError(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if _, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(1)}); err == nil {
		t.Fatal("expected error")
	}
}

func TestLimiterSpacesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) }))
	defer srv.Close()
	p := New(srv.Client(), WithBaseURL(srv.URL), WithLimiter(rate.NewLimiter(rate.Every(80*time.Millisecond), 1)))
	start := time.Now()
	_, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q, q, q}, Window: win(1)})
	if err != nil || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("limiter not applied: %v elapsed=%v", err, time.Since(start))
	}
}

func TestContextCancelStopsDiscovery(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Discover(ctx, discovery.Request{Queries: []discovery.Query{q}, Window: win(1)}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNoQueriesIsConfigError(t *testing.T) {
	p := New(http.DefaultClient)
	if _, err := p.Discover(context.Background(), discovery.Request{Window: win(1)}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRetriesTransient429ThenSucceeds(t *testing.T) {
	var n int
	p := serve(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(429)
			w.Write([]byte("Please limit requests to one every 5 seconds."))
			return
		}
		respond(w, 2, "ok")
	})
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(1)})
	if err != nil || len(res.Candidates) != 2 || res.Requests != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestPersistent429StopsAllRemainingQueries(t *testing.T) {
	var n int
	p := serve(t, func(w http.ResponseWriter, r *http.Request) { n++; w.WriteHeader(429) })
	res, err := p.Discover(context.Background(), discovery.Request{
		Queries: []discovery.Query{{Name: "a", Text: "aaa bbb"}, {Name: "b", Text: "ccc ddd"}, {Name: "c", Text: "eee fff"}}, Window: win(1)})
	var rl *discovery.RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitedError, got %v", err)
	}
	if n != 3 { // 1 try + 2 retries for the FIRST query only; later queries are not attempted
		t.Fatalf("hammered a rate-limited API: %d requests", n)
	}
	if res.Requests != 3 {
		t.Fatalf("requests=%d", res.Requests)
	}
}

func TestRetriesAreSpacedByTheLimiter(t *testing.T) {
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times = append(times, time.Now())
		w.WriteHeader(429)
	}))
	defer srv.Close()
	// Backoff is tiny; the limiter (100ms) must be what spaces the retries.
	p := New(srv.Client(), WithBaseURL(srv.URL), WithLimiter(rate.NewLimiter(rate.Every(100*time.Millisecond), 1)), WithBackoff(time.Millisecond, time.Millisecond))
	p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: win(1)})
	if len(times) != 3 {
		t.Fatalf("attempts %d", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 90*time.Millisecond {
			t.Fatalf("retry %d came %v after the previous request, violating the rate limit", i, gap)
		}
	}
}
