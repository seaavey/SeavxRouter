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

func newTestEngine(t *testing.T, handler http.HandlerFunc) (*Engine, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	e, err := NewEngine("groq", srv.URL, auth.AuthStore{}, []*auth.Auth{{Provider: "groq", AccessToken: "gsk_test"}})
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = srv.Client()
	return e, srv
}

func TestModelsParsesCatalog(t *testing.T) {
	e, _ := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gsk_test" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"openai/gpt-oss-120b","name":"GPT OSS 120B"},{"id":"llama-3.3-70b","owned_by":"meta"}]}`))
	})
	models, err := e.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "openai/gpt-oss-120b" || models[0].Name != "GPT OSS 120B" {
		t.Fatalf("model[0] = %+v", models[0])
	}
	// A missing name falls back to the ID.
	if models[1].Name != "llama-3.3-70b" {
		t.Fatalf("model[1].Name = %q", models[1].Name)
	}
}

func TestChatFoldsSSEStream(t *testing.T) {
	e, _ := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["stream"] != true {
			t.Errorf("stream = %v, want true", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"PO\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"NG\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9,\"completion_tokens_details\":{\"reasoning_tokens\":1},\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\n" +
				"data: [DONE]\n\n"))
	})
	var deltas []string
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "test-model",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, func(d backend.ChatDelta) {
		if d.Content != "" {
			deltas = append(deltas, d.Content)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "PONG" {
		t.Fatalf("content = %q", res.Content)
	}
	if strings.Join(deltas, "") != "PONG" {
		t.Fatalf("deltas = %v", deltas)
	}
	if res.Usage.InputTokens != 7 || res.Usage.OutputTokens != 2 || res.Usage.ThinkingTokens != 1 || res.Usage.CacheReadTokens != 3 || res.Usage.TotalTokens != 9 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.FinishReason != "stop" {
		t.Fatalf("finish = %q", res.FinishReason)
	}
}

func TestChatAccumulatesToolCallDeltas(t *testing.T) {
	e, _ := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"ci\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"ty\\\":\\\"SF\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n"))
	})
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "test-model",
		Messages: []backend.ChatMessage{{Role: "user", Content: "weather?"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_weather" || tc.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call = %+v", tc)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", res.FinishReason)
	}
}

func TestChatFallsBackToPlainJSON(t *testing.T) {
	e, _ := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"plain"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	})
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "test-model",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "plain" || res.Usage.TotalTokens != 2 {
		t.Fatalf("result = %+v", res)
	}
}

func TestChatRotatesKeysOnAuthFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	e, err := NewEngine("groq", srv.URL, auth.AuthStore{}, []*auth.Auth{
		{Provider: "groq", AccessToken: "bad"},
		{Provider: "groq", AccessToken: "good"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = srv.Client()
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "test-model",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q", res.Content)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (bad key then good key)", calls)
	}
}

func TestCloudflareAccountScopedPaths(t *testing.T) {
	var gotModelPath, gotChatPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ai/models/search"):
			gotModelPath = r.URL.Path
			_, _ = w.Write([]byte(`{"result":[{"name":"@cf/meta/llama-3.1-8b-instruct"}]}`))
		case strings.HasSuffix(r.URL.Path, "/ai/v1/chat/completions"):
			gotChatPath = r.URL.Path
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"cf\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	acct := &auth.Auth{Provider: "cloudflare", AccessToken: "tok", AccountID: "acct123"}
	e, err := NewEngine("cloudflare", srv.URL, auth.AuthStore{}, []*auth.Auth{acct})
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = srv.Client()
	e.ModelsPath = "accounts/%s/ai/models/search"
	e.ChatPath = "accounts/%s/ai/v1/chat/completions"

	if _, err := e.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := "/accounts/acct123/ai/models/search"; gotModelPath != want {
		t.Fatalf("models path = %q, want %q", gotModelPath, want)
	}
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "@cf/meta/llama-3.1-8b-instruct",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "cf" {
		t.Fatalf("content = %q", res.Content)
	}
	if want := "/accounts/acct123/ai/v1/chat/completions"; gotChatPath != want {
		t.Fatalf("chat path = %q, want %q", gotChatPath, want)
	}
}

func TestWireMessagesPreservesToolTurns(t *testing.T) {
	msgs := wireMessages([]backend.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: "checking", ToolCalls: []backend.ToolCall{{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Jakarta"}`}}},
		{Role: "tool", ToolCallID: "call_1", Name: "get_weather", Content: "28C"},
		{Role: "user", Content: "and now?", Images: []backend.ImagePart{{MIMEType: "image/png", Data: "QUJD"}}},
	})
	if len(msgs) != 4 {
		t.Fatalf("messages = %d", len(msgs))
	}
	if msgs[1]["tool_calls"] == nil {
		t.Fatalf("assistant tool_calls dropped: %#v", msgs[1])
	}
	if msgs[2]["tool_call_id"] != "call_1" {
		t.Fatalf("tool message = %#v", msgs[2])
	}
	parts, ok := msgs[3]["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("image parts = %#v", msgs[3]["content"])
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("image part = %#v", parts[1])
	}
}
