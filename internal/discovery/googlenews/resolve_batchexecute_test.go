package googlenews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

const page = `<html><body><c-wiz jsrenderer="x"><div data-n-a-sg="AZ5SIG123" data-n-a-ts="1792199000"></div></c-wiz></body></html>`

func fakeGoogle(t *testing.T, pageBody string, apiStatus int) (*BatchExecuteResolver, *atomic.Int32, *string) {
	resp, err := os.ReadFile("testdata/batchexecute_response.txt")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var freq string
	mux := http.NewServeMux()
	mux.HandleFunc("/rss/articles/", func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(pageBody)) })
	mux.HandleFunc("/batchexecute", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		freq = r.PostForm.Get("f.req")
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded;charset=UTF-8" {
			t.Errorf("content-type %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(apiStatus)
		w.Write(resp)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r := NewBatchExecuteResolver(srv.Client(), WithPageBase(srv.URL+"/rss/articles"), WithAPIURL(srv.URL+"/batchexecute"),
		WithBatchLimiter(rate.NewLimiter(rate.Inf, 1)))
	return r, &calls, &freq
}

const gurl = "https://news.google.com/rss/articles/CBMiTESTID123?oc=5"

func TestResolvesPublisherURLAndSendsSignedRequest(t *testing.T) {
	r, calls, freq := fakeGoogle(t, page, 200)
	got, err := r.Resolve(context.Background(), gurl)
	if err != nil || got != "https://abc6onyourside.com/news/local/pedestrian-struck-by-car-killed-in-brewery-district" {
		t.Fatalf("%q %v", got, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls %d", calls.Load())
	}
	for _, want := range []string{"Fbv4je", "garturlreq", "CBMiTESTID123", "AZ5SIG123", "1792199000"} {
		if !strings.Contains(*freq, want) && !strings.Contains(mustUnescape(*freq), want) {
			t.Errorf("f.req missing %q: %s", want, *freq)
		}
	}
}

func mustUnescape(s string) string {
	u, _ := url.QueryUnescape(s)
	return strings.ReplaceAll(u, `\"`, `"`)
}

func TestCachesPerArticleID(t *testing.T) {
	r, calls, _ := fakeGoogle(t, page, 200)
	r.Resolve(context.Background(), gurl)
	r.Resolve(context.Background(), gurl)
	if calls.Load() != 2 {
		t.Fatalf("second resolve should be cached, calls=%d", calls.Load())
	}
}

func TestMissingSignatureIsAnError(t *testing.T) {
	r, _, _ := fakeGoogle(t, `<html><body>Before you continue to Google (consent)</body></html>`, 200)
	if _, err := r.Resolve(context.Background(), gurl); err == nil || !strings.Contains(err.Error(), "signature attributes not found") {
		t.Fatalf("got %v", err)
	}
}

func TestCircuitBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	r, calls, _ := fakeGoogle(t, "no signature here", 200)
	for i := 0; i < 3; i++ {
		r.Resolve(context.Background(), "https://news.google.com/rss/articles/ID"+string(rune('A'+i)))
	}
	before := calls.Load()
	_, err := r.Resolve(context.Background(), "https://news.google.com/rss/articles/IDZ")
	if err != ErrCircuitOpen || calls.Load() != before {
		t.Fatalf("breaker should stop all traffic: err=%v extra calls=%d", err, calls.Load()-before)
	}
}

func TestPerRunBudget(t *testing.T) {
	r, calls, _ := fakeGoogle(t, page, 200)
	r.maxCalls = 1
	if _, err := r.Resolve(context.Background(), "https://news.google.com/rss/articles/AAA"); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := r.Resolve(context.Background(), "https://news.google.com/rss/articles/BBB"); err == nil || calls.Load() != before {
		t.Fatalf("budget not enforced: %v", err)
	}
}

func TestParseBatchResponseRejectsBadShapes(t *testing.T) {
	good, _ := os.ReadFile("testdata/batchexecute_response.txt")
	if u, err := parseBatchResponse(good); err != nil || !strings.HasPrefix(u, "https://abc6onyourside.com/") {
		t.Fatalf("%q %v", u, err)
	}
	for name, body := range map[string]string{
		"html":         "<html>blocked</html>",
		"no payload":   ")]}'\n\n[[\"wrb.fr\",\"Fbv4je\",null,null]]",
		"still google": ")]}'\n\n[[\"wrb.fr\",\"Fbv4je\",\"[\\\"garturlres\\\",\\\"https://news.google.com/x\\\"]\"]]",
		"javascript":   ")]}'\n\n[[\"wrb.fr\",\"Fbv4je\",\"[\\\"garturlres\\\",\\\"javascript:alert(1)\\\"]\"]]",
		"bad inner":    ")]}'\n\n[[\"wrb.fr\",\"Fbv4je\",\"not json\"]]",
	} {
		if u, err := parseBatchResponse([]byte(body)); err == nil {
			t.Errorf("%s: expected error, got %q", name, u)
		}
	}
}

func TestContextCancelDoesNotCountAsFailure(t *testing.T) {
	r, _, _ := fakeGoogle(t, page, 200)
	r.limiter = rate.NewLimiter(rate.Every(time.Hour), 0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r.Resolve(ctx, gurl)
	if r.fails != 0 {
		t.Fatalf("cancellation must not trip the breaker, fails=%d", r.fails)
	}
}
