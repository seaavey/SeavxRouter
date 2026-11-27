package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

func TestProbeCatalogOpenAIRequiresModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
	}))
	defer srv.Close()

	models, err := ProbeCatalog(context.Background(), "mock", srv.URL, "sk-good", "openai")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(models) != 2 || models[0] != "alpha" || models[1] != "beta" {
		t.Fatalf("models = %v", models)
	}

	_, err = ProbeCatalog(context.Background(), "mock", srv.URL, "sk-bad", "openai")
	if err == nil {
		t.Fatal("a rejected key must fail validation")
	}
	var httpErr *backend.HTTPError
	if !asHTTPError(err, &httpErr) || httpErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want upstream 401 preserved", err)
	}
}

func TestProbeCatalogAnthropicSendsMessagesHeaders(t *testing.T) {
	var sawKey, sawVersion, sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("x-api-key")
		sawVersion = r.Header.Get("anthropic-version")
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x","display_name":"Claude X"}],"has_more":false}`))
	}))
	defer srv.Close()

	models, err := ProbeCatalog(context.Background(), "mock", srv.URL+"/v1/", "sk-ant", "anthropic")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if sawPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models (a trailing /v1 must not double up)", sawPath)
	}
	if sawVersion != AnthropicVersion {
		t.Errorf("anthropic-version = %q", sawVersion)
	}
	if sawKey != "sk-ant" {
		t.Errorf("x-api-key = %q", sawKey)
	}
	if len(models) != 1 || models[0] != "claude-x" {
		t.Fatalf("models = %v", models)
	}
}

func TestProbeCatalogFallsBackToAPIModellistShape(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/api/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-good" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash"}]}`))
	}))
	defer srv.Close()

	models, err := ProbeCatalog(context.Background(), "mock", srv.URL+"/v1", "sk-good", "openai")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(models) != 1 || models[0] != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("models = %v", models)
	}
	if len(paths) != 2 || paths[0] != "/v1/models" || paths[1] != "/api/models" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestProbeCatalogRejectsEmptyCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if _, err := ProbeCatalog(context.Background(), "mock", srv.URL, "sk", "openai"); err == nil {
		t.Fatal("an empty catalog must not validate")
	}
}

func TestAnthropicChatFoldsStream(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-ant" {
			t.Errorf("x-api-key = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PO\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"NG\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n",
		))
	}))
	defer srv.Close()

	e, err := NewEngineSDK("mock", srv.URL, "anthropic", auth.AuthStore{}, []*auth.Auth{{Provider: "mock", AccessToken: "sk-ant"}})
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = srv.Client()

	var streamed strings.Builder
	result, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "claude-x",
		Messages: []backend.ChatMessage{{Role: "system", Content: "be brief"}, {Role: "user", Content: "ping"}},
	}, func(d backend.ChatDelta) { streamed.WriteString(d.Content) })
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if result.Content != "PONG" || streamed.String() != "PONG" {
		t.Fatalf("content = %q, streamed = %q", result.Content, streamed.String())
	}
	if result.FinishReason != "stop" {
		t.Errorf("finish = %q", result.FinishReason)
	}
	if result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 2 || result.Usage.TotalTokens != 13 {
		t.Errorf("usage = %+v", result.Usage)
	}
	// max_tokens is mandatory on the Messages API.
	if body["max_tokens"] == nil {
		t.Error("max_tokens missing from the Messages API payload")
	}
	if body["system"] != "be brief" {
		t.Errorf("system = %v", body["system"])
	}
	if body["stream"] != true {
		t.Errorf("stream = %v", body["stream"])
	}
}

func TestAnthropicChatRotatesOnRejectedKey(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		seen = append(seen, key)
		if key == "sk-bad" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()

	e, err := NewEngineSDK("mock", srv.URL, "anthropic", auth.AuthStore{}, []*auth.Auth{
		{Provider: "mock", AccessToken: "sk-bad"},
		{Provider: "mock", AccessToken: "sk-good"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = srv.Client()

	result, err := e.Chat(context.Background(), &backend.ChatRequest{Model: "claude-x", Messages: []backend.ChatMessage{{Role: "user", Content: "ping"}}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if result.Content != "ok" {
		t.Fatalf("content = %q", result.Content)
	}
	if len(seen) != 2 {
		t.Fatalf("keys tried = %v, want both", seen)
	}
}

func TestEngineQuotaReportsBaseURLAndSDK(t *testing.T) {
	e, err := NewEngineSDK("mock", "https://api.example.com/v1", "anthropic", auth.AuthStore{}, []*auth.Auth{
		{Provider: "mock", AccessToken: "sk", Email: "ops@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	q := e.Quota()
	if q.Provider != "mock" || q.BaseURL != "https://api.example.com/v1" || q.SDK != "anthropic" {
		t.Fatalf("quota = %+v", q)
	}
	if len(q.Accounts) != 1 || q.Accounts[0].Status != "ready" {
		t.Fatalf("accounts = %+v", q.Accounts)
	}
}

// asHTTPError is errors.As specialized to *backend.HTTPError.
func asHTTPError(err error, target **backend.HTTPError) bool {
	for err != nil {
		if he, ok := err.(*backend.HTTPError); ok {
			*target = he
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
