package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// mockGateway is an OpenAI-compatible /v1/chat/completions stub.
type mockGateway struct {
	mu       sync.Mutex
	formats  []string // response_format.type seen per request
	rejectJS bool     // reject json_schema with 400
	rejectJO bool
	status   int // if set, always return this status
	content  string
}

func (m *mockGateway) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	_ = json.Unmarshal(body, &req)
	m.mu.Lock()
	m.formats = append(m.formats, req.ResponseFormat.Type)
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case m.status != 0:
		w.WriteHeader(m.status)
		w.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
	case m.rejectJS && req.ResponseFormat.Type == "json_schema", m.rejectJO && req.ResponseFormat.Type == "json_object":
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"response_format not supported","type":"invalid_request_error"}}`))
	default:
		json.NewEncoder(w).Encode(map[string]any{
			"id": "x", "object": "chat.completion", "created": 1, "model": "m",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": m.content}}},
			"usage":   map[string]any{"prompt_tokens": 1000, "completion_tokens": 200, "total_tokens": 1200},
		})
	}
}

func setup(t *testing.T, m *mockGateway, cfg Config) *Client {
	srv := httptest.NewServer(http.HandlerFunc(m.handler))
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL + "/v1"
	cfg.APIKey = "test"
	return NewClient(cfg)
}

var req = Request{Model: "qwen", System: "sys", User: "usr", SchemaName: "s", Schema: map[string]any{"type": "object"}}

func TestNativeSchemaMode(t *testing.T) {
	m := &mockGateway{content: `{"ok":true}`}
	c := setup(t, m, Config{})
	r, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r.Content != `{"ok":true}` || r.Mode != ModeJSONSchema || m.formats[0] != "json_schema" {
		t.Fatalf("bad: %+v formats=%v", r, m.formats)
	}
}

func TestDowngradesAndRemembersCapability(t *testing.T) {
	m := &mockGateway{content: `{}`, rejectJS: true}
	c := setup(t, m, Config{})
	r, err := c.Complete(context.Background(), req)
	if err != nil || r.Mode != ModeJSONObject {
		t.Fatalf("want json_object, got %+v err=%v", r, err)
	}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Second call must start at json_object (learned), not retry json_schema.
	if got := m.formats; len(got) != 3 || got[2] != "json_object" {
		t.Fatalf("formats=%v", got)
	}
}

func TestDowngradesToPromptMode(t *testing.T) {
	m := &mockGateway{content: `{}`, rejectJS: true, rejectJO: true}
	c := setup(t, m, Config{})
	r, err := c.Complete(context.Background(), req)
	if err != nil || r.Mode != ModePrompt {
		t.Fatalf("want prompt mode, got %+v err=%v", r, err)
	}
}

func TestServerErrorIsNotDowngraded(t *testing.T) {
	m := &mockGateway{status: 500}
	c := setup(t, m, Config{MaxRetries: 0})
	if _, err := c.Complete(context.Background(), req); err == nil {
		t.Fatal("expected error")
	}
	if len(m.formats) != 1 {
		t.Fatalf("500 must not trigger mode downgrade: %v", m.formats)
	}
}

func TestRetriesTransientErrors(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL + "/v1", APIKey: "k", MaxRetries: 2})
	if _, err := c.Complete(context.Background(), req); err != nil || n != 2 {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL + "/v1", APIKey: "k"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Complete(ctx, req); err == nil {
		t.Fatal("expected cancellation error")
	}
}
