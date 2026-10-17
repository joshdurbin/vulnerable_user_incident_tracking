package googlenews

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
)

var q = discovery.Query{Family: "pedestrian", Name: "ped", Text: "pedestrian struck killed"}

// window covering the fixture's items (all published 2026-10-17 ~00:00-01:30 UTC)
func window() discovery.Window {
	return discovery.Window{Since: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)}
}

func fixture(t *testing.T) []byte {
	b, err := os.ReadFile("testdata/search.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serve(t *testing.T, body []byte, opts ...Option) *Provider {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	all := append([]Option{WithBaseURL(srv.URL), WithLimiter(rate.NewLimiter(rate.Inf, 1))}, opts...)
	return New(srv.Client(), all...)
}

func TestFixtureItemsKeepPublisherAndAreUnresolved(t *testing.T) {
	p := serve(t, fixture(t))
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: window()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 6 {
		t.Fatalf("got %d", len(res.Candidates))
	}
	c := res.Candidates[0]
	if !c.NeedsResolution || !strings.Contains(c.URL, "news.google.com") {
		t.Fatalf("current feed ids are encrypted and must stay unresolved: %+v", c)
	}
	if c.Publisher != "WSYX" || c.PublisherURL != "https://abc6onyourside.com" {
		t.Fatalf("publisher lost: %+v", c)
	}
	if c.Title != "Pedestrian struck by car, killed in Brewery District" {
		t.Fatalf("title not cleaned: %q", c.Title)
	}
	if c.PublishedAt == nil || c.QueryName != "ped" || c.Country != "US" {
		t.Fatalf("%+v", c)
	}
}

func TestRequestParams(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.RawQuery; w.Write(fixture(t)) }))
	defer srv.Close()
	p := New(srv.Client(), WithBaseURL(srv.URL), WithLimiter(rate.NewLimiter(rate.Inf, 1)))
	if _, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: window()}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ceid=US%3Aen", "gl=US", "hl=en-US", "after%3A2026-10-15", "before%3A2026-10-19"} {
		if !strings.Contains(got, want) {
			t.Errorf("query %q missing %q", got, want)
		}
	}
}

func TestWindowTrimsItemsOutsideRange(t *testing.T) {
	p := serve(t, fixture(t))
	w := discovery.Window{Since: time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)}
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: w})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Candidates {
		if c.PublishedAt.Before(w.Since) {
			t.Fatalf("item before window kept: %v", c.PublishedAt)
		}
	}
	if len(res.Candidates) >= 6 {
		t.Fatalf("expected some items trimmed, got %d", len(res.Candidates))
	}
}

func legacyID(url string) string {
	payload := append([]byte{0x08, 0x13, 0x22, byte(len(url))}, []byte(url)...)
	payload = append(payload, 0xd2, 0x01, 0x00)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestLegacyDecoder(t *testing.T) {
	d := LegacyDecoder{}
	got, err := d.Resolve(context.Background(), "https://news.google.com/rss/articles/"+legacyID("https://pub.example.com/2026/story-1")+"?oc=5")
	if err != nil || got != "https://pub.example.com/2026/story-1" {
		t.Fatalf("%q %v", got, err)
	}
	// A current-format id from the live feed has no embedded URL.
	if _, err := d.Resolve(context.Background(), "https://news.google.com/rss/articles/CBMihAJBVV95cUxQN2l6VXlFNXFLLWRSakVZS3RPNjBHUF9kRTZTS0pQdGYwdnJCdVhrUTR4dmdHWEY3OFVjakY1bzBW?oc=5"); err == nil {
		t.Fatal("encrypted id must not decode")
	}
	if _, err := d.Resolve(context.Background(), "https://example.com/not-google"); err == nil {
		t.Fatal("non-google url must fail")
	}
}

type fakeResolver struct {
	name string
	out  string
	err  error
	n    int
}

func (f *fakeResolver) Name() string { return f.name }
func (f *fakeResolver) Resolve(context.Context, string) (string, error) {
	f.n++
	return f.out, f.err
}

func TestResolverChainFallsThroughAndKeepsRawURL(t *testing.T) {
	first := &fakeResolver{name: "a", err: errors.New("nope")}
	second := &fakeResolver{name: "b", out: "https://abc6onyourside.com/news/ped"}
	third := &fakeResolver{name: "c", out: "https://never.example.com"}
	p := serve(t, fixture(t), WithResolvers(first, second, third))
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: window()})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Candidates[0]
	if c.NeedsResolution || c.URL != "https://abc6onyourside.com/news/ped" || !strings.Contains(c.RawURL, "news.google.com") {
		t.Fatalf("%+v", c)
	}
	if third.n != 0 {
		t.Fatal("chain should stop at the first success")
	}
}

func TestResolverReturningGoogleURLIsRejected(t *testing.T) {
	bad := &fakeResolver{name: "bad", out: "https://news.google.com/articles/abc"}
	p := serve(t, fixture(t), WithResolvers(bad))
	res, _ := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: window()})
	if !res.Candidates[0].NeedsResolution {
		t.Fatal("a resolver must not be able to hand back a google.com URL")
	}
}

func TestRateLimitedAndBadStatusAndBadFeed(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		rl     bool
	}{"429": {429, "", true}, "500": {500, "", false}, "html": {200, "<html>consent</html>", false}} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer srv.Close()
			p := New(srv.Client(), WithBaseURL(srv.URL), WithLimiter(rate.NewLimiter(rate.Inf, 1)))
			_, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{q}, Window: window()})
			var rl *discovery.RateLimitedError
			if err == nil || errors.As(err, &rl) != tc.rl {
				t.Fatalf("err=%v rl=%v", err, tc.rl)
			}
		})
	}
}

func TestPartialFailureAndNoQueries(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(500)
			return
		}
		w.Write(fixture(t))
	}))
	defer srv.Close()
	p := New(srv.Client(), WithBaseURL(srv.URL), WithLimiter(rate.NewLimiter(rate.Inf, 1)))
	res, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{{Name: "a", Text: "a"}, {Name: "b", Text: "b"}}, Window: window()})
	if err != nil || len(res.Candidates) != 6 || len(res.Warnings) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := p.Discover(context.Background(), discovery.Request{Window: window()}); err == nil {
		t.Fatal("no queries must be an error")
	}
}

func TestRateLimitStopsRemainingQueries(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; w.WriteHeader(429) }))
	defer srv.Close()
	p := New(srv.Client(), WithBaseURL(srv.URL), WithLimiter(rate.NewLimiter(rate.Inf, 1)))
	_, err := p.Discover(context.Background(), discovery.Request{Queries: []discovery.Query{{Name: "a", Text: "a"}, {Name: "b", Text: "b"}, {Name: "c", Text: "c"}}, Window: window()})
	var rl *discovery.RateLimitedError
	if !errors.As(err, &rl) || n != 1 {
		t.Fatalf("err=%v requests=%d (should stop after the first 429)", err, n)
	}
}
