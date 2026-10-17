package googlenews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/time/rate"
)

// BatchExecuteResolver resolves current-format Google News ids by replaying the
// two requests Google's own web page makes: fetch the article page for its
// signature and timestamp, then POST them to the batchexecute endpoint.
//
// UNDOCUMENTED and unsupported by Google. It was verified to work live on
// 2026-10-16 and can break or be blocked at any time, so it is OFF by default,
// rate limited, bounded per run, and trips a circuit breaker after consecutive
// failures. Everything that depends on it must tolerate failure (the provider
// keeps unresolved items).
type BatchExecuteResolver struct {
	client   *http.Client
	pageBase string // https://news.google.com/rss/articles
	apiURL   string
	limiter  *rate.Limiter
	maxCalls int
	maxFails int

	mu    sync.Mutex
	calls int
	fails int // consecutive
	cache map[string]string
}

type BatchOption func(*BatchExecuteResolver)

func WithPageBase(u string) BatchOption { return func(r *BatchExecuteResolver) { r.pageBase = u } }
func WithAPIURL(u string) BatchOption   { return func(r *BatchExecuteResolver) { r.apiURL = u } }
func WithBatchLimiter(l *rate.Limiter) BatchOption {
	return func(r *BatchExecuteResolver) { r.limiter = l }
}
func WithMaxCalls(n int) BatchOption { return func(r *BatchExecuteResolver) { r.maxCalls = n } }

// NewBatchExecuteResolver uses client for both requests; it must not retry automatically.
func NewBatchExecuteResolver(client *http.Client, opts ...BatchOption) *BatchExecuteResolver {
	r := &BatchExecuteResolver{
		client: client, pageBase: "https://news.google.com/rss/articles",
		apiURL:  "https://news.google.com/_/DotsSplashUi/data/batchexecute",
		limiter: rate.NewLimiter(rate.Every(2*time.Second), 1), maxCalls: 100, maxFails: 3, cache: map[string]string{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *BatchExecuteResolver) Name() string { return "batchexecute" }

// ErrCircuitOpen is returned once the resolver has given up for this run.
var ErrCircuitOpen = errors.New("batchexecute resolver disabled for this run after repeated failures")

func (r *BatchExecuteResolver) Resolve(ctx context.Context, googleURL string) (string, error) {
	id, err := articleID(googleURL)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	if u, ok := r.cache[id]; ok {
		r.mu.Unlock()
		return u, nil
	}
	if r.fails >= r.maxFails {
		r.mu.Unlock()
		return "", ErrCircuitOpen
	}
	if r.calls >= r.maxCalls {
		r.mu.Unlock()
		return "", fmt.Errorf("batchexecute: per-run budget of %d resolutions used", r.maxCalls)
	}
	r.calls++
	r.mu.Unlock()

	u, err := r.resolve(ctx, id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		var lw *limiterWaitError
		if ctx.Err() == nil && !errors.As(err, &lw) { // our own throttling/cancellation is not Google failing
			r.fails++
		}
		return "", err
	}
	r.fails = 0
	r.cache[id] = u
	return u, nil
}

// limiterWaitError marks errors from waiting on our own rate limiter.
type limiterWaitError struct{ err error }

func (e *limiterWaitError) Error() string { return "batchexecute: rate limiter: " + e.err.Error() }
func (e *limiterWaitError) Unwrap() error { return e.err }

func (r *BatchExecuteResolver) wait(ctx context.Context) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return &limiterWaitError{err}
	}
	return nil
}

func (r *BatchExecuteResolver) resolve(ctx context.Context, id string) (string, error) {
	sig, ts, err := r.signature(ctx, id)
	if err != nil {
		return "", err
	}
	inner, err := json.Marshal([]any{"garturlreq",
		[]any{[]any{"X", "X", []any{"X", "X"}, nil, nil, 1, 1, "US:en", nil, 1, nil, nil, nil, nil, nil, 0, 1}, "X", "X", 1, []any{1, 1, 1}, 1, 1, nil, 0, 0, nil, 0},
		id, ts, sig})
	if err != nil {
		return "", err
	}
	freq, err := json.Marshal([]any{[]any{[]any{"Fbv4je", string(inner), nil, "generic"}}})
	if err != nil {
		return "", err
	}
	if err := r.wait(ctx); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.apiURL, strings.NewReader(url.Values{"f.req": {string(freq)}}.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("batchexecute: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return parseBatchResponse(body)
}

// signature fetches the article page and reads data-n-a-sg / data-n-a-ts.
func (r *BatchExecuteResolver) signature(ctx context.Context, id string) (sig string, ts int64, err error) {
	if err := r.wait(ctx); err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.pageBase+"/"+id, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("batchexecute: article page http %d", resp.StatusCode)
	}
	z := html.NewTokenizer(io.LimitReader(resp.Body, 4<<20))
	var tsStr string
	for {
		switch z.Next() {
		case html.ErrorToken:
			return "", 0, errors.New("batchexecute: signature attributes not found on article page (page format changed or consent wall)")
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			var s, t string
			for _, a := range tok.Attr {
				switch a.Key {
				case "data-n-a-sg":
					s = a.Val
				case "data-n-a-ts":
					t = a.Val
				}
			}
			if s != "" && t != "" {
				sig, tsStr = s, t
				var n int64
				if _, err := fmt.Sscan(tsStr, &n); err != nil {
					return "", 0, fmt.Errorf("batchexecute: bad timestamp %q", tsStr)
				}
				return sig, n, nil
			}
		}
	}
}

// parseBatchResponse extracts the publisher URL from a batchexecute reply:
//
//	)]}'
//
//	[["wrb.fr","Fbv4je","[\"garturlres\",\"https://publisher/...\",1]",...]]
func parseBatchResponse(body []byte) (string, error) {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), ")]}'"))
	i := strings.Index(s, "[[")
	if i < 0 {
		return "", errors.New("batchexecute: unexpected response shape")
	}
	var outer [][]any
	if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&outer); err != nil {
		return "", fmt.Errorf("batchexecute: decode response: %w", err)
	}
	if len(outer) == 0 || len(outer[0]) < 3 {
		return "", errors.New("batchexecute: empty response")
	}
	payload, ok := outer[0][2].(string)
	if !ok {
		return "", errors.New("batchexecute: no payload (article id rejected)")
	}
	var inner []any
	if err := json.Unmarshal([]byte(payload), &inner); err != nil || len(inner) < 2 {
		return "", errors.New("batchexecute: bad payload")
	}
	u, ok := inner[1].(string)
	if !ok {
		return "", errors.New("batchexecute: payload has no url")
	}
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return "", fmt.Errorf("batchexecute: result is not an http(s) url: %q", u)
	}
	if strings.HasSuffix(pu.Hostname(), "google.com") {
		return "", errors.New("batchexecute: result still points at google.com")
	}
	return u, nil
}
