package verboo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

func newTestEngine(t *testing.T, srv *httptest.Server, tokens ...string) *Engine {
	t.Helper()
	auths := make([]*auth.Auth, 0, len(tokens))
	for _, tok := range tokens {
		auths = append(auths, &auth.Auth{Provider: "verboo", AccessToken: tok})
	}
	e, err := NewEngine(auth.AuthStore{}, auths)
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	e.HTTP = srv.Client()
	return e
}

func TestVerbooMessages(t *testing.T) {
	msgs := verbooMessages([]backend.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "developer", Content: "dev"},
		{Role: "assistant", Content: "checking", ToolCalls: []backend.ToolCall{{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Jakarta"}`}}},
		{Role: "tool", ToolCallID: "call_1", Name: "get_weather", Content: "28C"},
		{Role: "user", Content: "what is this?", Images: []backend.ImagePart{{MIMEType: "image/png", Data: "QUJD"}}},
	})
	if len(msgs) != 5 {
		t.Fatalf("len(messages) = %d, want 5", len(msgs))
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "sys" {
		t.Fatalf("system message = %#v", msgs[0])
	}
	if msgs[1]["role"] != "developer" || msgs[1]["content"] != "dev" {
		t.Fatalf("developer message = %#v", msgs[1])
	}
	tcs, ok := msgs[2]["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("assistant tool_calls = %#v", msgs[2]["tool_calls"])
	}
	fn, _ := tcs[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"Jakarta"}` {
		t.Fatalf("tool function = %#v", fn)
	}
	tool := msgs[3]
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["name"] != "get_weather" || tool["content"] != "28C" {
		t.Fatalf("tool message = %#v", tool)
	}
	parts, ok := msgs[4]["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("user content parts = %#v", msgs[4]["content"])
	}
	if parts[0]["type"] != "text" || parts[1]["type"] != "image_url" {
		t.Fatalf("user parts = %#v", parts)
	}
	img, _ := parts[1]["image_url"].(map[string]any)
	if img["url"] != "data:image/png;base64,QUJD" {
		t.Fatalf("image url = %#v", img["url"])
	}
}

func TestVerbooTools(t *testing.T) {
	if got := verbooTools(nil); got != nil {
		t.Fatalf("verbooTools(nil) = %#v, want nil", got)
	}
	out := verbooTools([]backend.ToolDefinition{{Name: "get_weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}}})
	if len(out) != 1 || out[0]["type"] != "function" {
		t.Fatalf("tools = %#v", out)
	}
	fn, _ := out[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["description"] != "Get weather" || fn["parameters"] == nil {
		t.Fatalf("function = %#v", fn)
	}
}

func TestChatStreamAggregates(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"thinking\"},\"index\":0}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"index\":0}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"index\":0}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\"}}]},\"index\":0}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\"}}]},\"index\":0}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"Jakarta\\\"}\"}}]},\"index\":0}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\",\"index\":0}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":294,\"completion_tokens\":63,\"total_tokens\":357,\"prompt_tokens_details\":{\"cached_tokens\":128},\"completion_tokens_details\":{\"reasoning_tokens\":13}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	e := newTestEngine(t, srv, "tok")
	temp := 0.2
	maxTokens := 128
	var deltas []backend.ChatDelta
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:       "deepseek-v4.1-flash",
		Messages:    []backend.ChatMessage{{Role: "user", Content: "hi"}},
		Stream:      true,
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	}, func(d backend.ChatDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer tok" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotBody["model"] != "deepseek-v4.1-flash" {
		t.Fatalf("model = %#v", gotBody["model"])
	}
	if gotBody["stream"] != true {
		t.Fatalf("stream = %#v", gotBody["stream"])
	}
	so, _ := gotBody["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Fatalf("stream_options = %#v", gotBody["stream_options"])
	}
	if gotBody["temperature"] != 0.2 || gotBody["max_tokens"] != float64(128) {
		t.Fatalf("sampling params = %#v / %#v", gotBody["temperature"], gotBody["max_tokens"])
	}

	if res.Content != "Hello world" {
		t.Fatalf("content = %q", res.Content)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", res.FinishReason)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", res.ToolCalls)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_weather" || tc.Arguments != `{"city":"Jakarta"}` {
		t.Fatalf("tool call = %#v", tc)
	}
	wantUsage := backend.Usage{InputTokens: 294, OutputTokens: 63, ThinkingTokens: 13, CacheReadTokens: 128, TotalTokens: 357}
	if res.Usage != wantUsage {
		t.Fatalf("usage = %#v, want %#v", res.Usage, wantUsage)
	}
	var content, thinking, toolDeltas int
	for _, d := range deltas {
		content += len(d.Content)
		thinking += len(d.Thinking)
		toolDeltas += len(d.ToolCalls)
	}
	if content != len("Hello world") || thinking != len("thinking") || toolDeltas != 1 {
		t.Fatalf("deltas: content bytes = %d, thinking bytes = %d, tool calls = %d", content, thinking, toolDeltas)
	}
}

func TestChatClassifiesPausedFreeTierAsQuota(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":{"code":"free_tokens_accounting_pending","message":"The limit of requests awaiting usage confirmation has been reached. Free inference is paused until a request is settled; paid activation remains available."}}`)
	}))
	defer srv.Close()

	e := newTestEngine(t, srv, "tok")
	_, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "deepseek-v4.1-flash",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil || !backend.IsQuotaError(err) {
		t.Fatalf("err = %v, want a quota error", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestModelsServesStaleWhileRefreshing(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"stale","display_name":"Stale"}]}`)
			return
		}
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"fresh","display_name":"Fresh"}]}`)
	}))
	defer srv.Close()

	e := newTestEngine(t, srv, "tok")
	e.catalog = backend.NewCatalog("verboo", time.Nanosecond, e.fetchModels)
	models, err := e.Models(context.Background())
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(models) != 1 || models[0].ID != "stale" {
		t.Fatalf("models = %+v, want the first fetch", models)
	}

	time.Sleep(5 * time.Millisecond) // let the entry go stale
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	models, err = e.Models(ctx)
	if err != nil {
		t.Fatalf("stale read blocked on refresh: %v", err)
	}
	if len(models) != 1 || models[0].ID != "stale" {
		t.Fatalf("models = %+v, want the cached entry", models)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh did not start")
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		models, err := e.Models(context.Background())
		if err == nil && len(models) == 1 && models[0].ID == "fresh" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("catalog was not refreshed in the background")
}

func TestChatRotatesOnQuota(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"rate limit reached"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"index\":0}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	e := newTestEngine(t, srv, "bad", "good")
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "m",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q", res.Content)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func TestChatErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		check  func(error) bool
	}{
		{"payment", http.StatusPaymentRequired, backend.IsQuotaError},
		{"rate limit", http.StatusTooManyRequests, backend.IsQuotaError},
		{"unauthorized", http.StatusUnauthorized, backend.IsAuthError},
		{"forbidden", http.StatusForbidden, backend.IsAuthError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":{"message":"nope"}}`)
			}))
			defer srv.Close()
			e := newTestEngine(t, srv, "tok")
			_, err := e.Chat(context.Background(), &backend.ChatRequest{
				Model:    "m",
				Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
			}, nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !tc.check(err) {
				t.Fatalf("error not classified: %v", err)
			}
		})
	}
}

func TestModels(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-v4.1-flash","object":"model","display_name":"DeepSeek V4.1 Flash"},{"id":"mimo-v2.6-flash","object":"model"}]}`)
	}))
	defer srv.Close()

	e := newTestEngine(t, srv, "tok")
	models, err := e.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	if models[0] != (backend.Model{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"}) {
		t.Fatalf("models[0] = %#v", models[0])
	}
	if models[1] != (backend.Model{ID: "mimo-v2.6-flash", Name: "mimo-v2.6-flash"}) {
		t.Fatalf("models[1] = %#v", models[1])
	}
	if _, err := e.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream model calls = %d, want 1 (cached)", got)
	}
}

func TestLoginStoresKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vbk_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"m1"}]}`)
	}))
	defer srv.Close()

	t.Setenv("VERBOO_API_KEY", "vbk_test")
	store := auth.AuthStore{Path: filepath.Join(t.TempDir(), "auth.jsonc")}
	e := &Engine{Store: store, HTTP: srv.Client(), BaseURL: srv.URL, Out: io.Discard}
	if err := e.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	auths, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 1 {
		t.Fatalf("stored accounts = %#v", auths)
	}
	if auths[0].Provider != "verboo" || auths[0].AccessToken != "vbk_test" {
		t.Fatalf("stored account = %#v", auths[0])
	}
}

func TestLoginRejectsBadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer srv.Close()

	t.Setenv("VERBOO_API_KEY", "vbk_bad")
	store := auth.AuthStore{Path: filepath.Join(t.TempDir(), "auth.jsonc")}
	e := &Engine{Store: store, HTTP: srv.Client(), BaseURL: srv.URL, Out: io.Discard}
	if err := e.Login(context.Background()); err == nil {
		t.Fatal("expected login to fail")
	}
}
