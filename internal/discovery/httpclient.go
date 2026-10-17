package discovery

import (
	"io"
	"log"
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// NewHTTPClient returns a client that retries 429/5xx with exponential backoff
// (honoring Retry-After) and identifies itself. Providers share one.
func NewHTTPClient(userAgent string, timeout time.Duration) *http.Client {
	rc := retryablehttp.NewClient()
	rc.RetryMax = 3
	rc.RetryWaitMin = 2 * time.Second
	rc.RetryWaitMax = 30 * time.Second
	rc.Logger = log.New(io.Discard, "", 0)
	rc.HTTPClient.Timeout = timeout
	c := rc.StandardClient()
	c.Transport = &uaTransport{ua: userAgent, next: c.Transport}
	return c
}

type uaTransport struct {
	ua   string
	next http.RoundTripper
}

func (t *uaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", t.ua)
	}
	return t.next.RoundTrip(r)
}

// DefaultUserAgent identifies the bot to publishers and APIs.
const DefaultUserAgent = "CollisionTrackerBot/0.1 (+https://github.com/joshdurbin/fuck_cars)"

// NewPlainHTTPClient returns a client with NO automatic retries. Search-style
// providers use it because their upstreams enforce strict request spacing, so
// retries must go back through the provider's own rate limiter.
func NewPlainHTTPClient(userAgent string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &uaTransport{ua: userAgent, next: http.DefaultTransport}}
}
