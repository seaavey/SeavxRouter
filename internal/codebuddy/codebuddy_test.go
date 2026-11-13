package codebuddy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

func TestCodeBuddyMessages(t *testing.T) {
	msgs := []backend.ChatMessage{
		{Role: "system", Content: "be nice"},
		{Role: "user", Content: "weather?"},
		{Role: "assistant", Content: "checking", ToolCalls: []backend.ToolCall{{ID: "call_1", Type: "function", Name: "get_weather", Arguments: `{"city":"SF"}`}}},
		{Role: "tool", ToolCallID: "call_1", Name: "get_weather", Content: "sunny"},
	}
	got := codebuddyMessages(msgs)
	if len(got) != 4 {
		t.Fatalf("messages len = %d, want 4", len(got))
	}
	if got[0]["role"] != "system" || got[0]["content"] != "be nice" {
		t.Fatalf("system = %v", got[0])
	}
	if got[1]["role"] != "user" || got[1]["content"] != "weather?" {
		t.Fatalf("user = %v", got[1])
	}
	assistant := got[2]
	if assistant["role"] != "assistant" || assistant["content"] != "checking" {
		t.Fatalf("assistant = %v", assistant)
	}
	tcs := assistant["tool_calls"].([]map[string]any)
	if len(tcs) != 1 || tcs[0]["id"] != "call_1" || tcs[0]["type"] != "function" {
		t.Fatalf("assistant tool_calls = %v", tcs)
	}
	fn := tcs[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"SF"}` {
		t.Fatalf("assistant function = %v", fn)
	}
	tool := got[3]
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "sunny" {
		t.Fatalf("tool message = %v", tool)
	}
	if tool["name"] != "get_weather" {
		t.Fatalf("tool name = %v", tool["name"])
	}

	// A conversation not starting with a system turn gets a synthetic one:
	// CodeBuddy requires a leading system prompt.
	short := codebuddyMessages([]backend.ChatMessage{{Role: "user", Content: "hi"}})
	if len(short) != 2 || short[0]["role"] != "system" {
		t.Fatalf("short messages = %v", short)
	}
	toolFirst := codebuddyMessages([]backend.ChatMessage{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", ToolCalls: []backend.ToolCall{{ID: "c1", Name: "get_weather", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", Content: "sunny"},
	})
	if len(toolFirst) != 4 || toolFirst[0]["role"] != "system" || toolFirst[1]["role"] != "user" {
		t.Fatalf("tool-first messages = %v", toolFirst)
	}
}

func TestCodeBuddyImageMessages(t *testing.T) {
	got := codebuddyMessages([]backend.ChatMessage{{Role: "user", Content: "look", Images: []backend.ImagePart{{MIMEType: "image/png", Data: "aGVsbG8="}}}})
	if len(got) != 2 || got[0]["role"] != "system" || got[1]["role"] != "user" {
		t.Fatalf("messages = %v", got)
	}
	parts := got[1]["content"].([]map[string]any)
	if len(parts) != 2 {
		t.Fatalf("parts = %v", parts)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "look" {
		t.Fatalf("text part = %v", parts[0])
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("image part = %v", parts[1])
	}
	img := parts[1]["image_url"].(map[string]any)
	if img["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image url = %v", img)
	}
}

func TestCodeBuddyChat(t *testing.T) {
	var gotBody []byte
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"SF\\\"}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":3},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{
		HTTP:    &http.Client{},
		BaseURL: srv.URL,
		auths:   []*auth.Auth{{Provider: "codebuddy", AccessToken: "tok-1", AccountID: "user-1"}},
	}
	var deltas []backend.ChatDelta
	req := &backend.ChatRequest{
		Model:    "glm5.1",
		Messages: []backend.ChatMessage{{Role: "user", Content: "weather?"}},
		Tools:    []backend.ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
		Stream:   true,
	}
	res, err := e.Chat(context.Background(), req, func(d backend.ChatDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	var upstream map[string]any
	if err := json.Unmarshal(gotBody, &upstream); err != nil {
		t.Fatal(err)
	}
	if upstream["model"] != "glm-5.1" {
		t.Fatalf("model alias not applied: %v", upstream["model"])
	}
	if upstream["stream"] != true {
		t.Fatalf("upstream stream = %v, want true", upstream["stream"])
	}
	if tools, ok := upstream["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", upstream["tools"])
	}
	if res.Content != "Hello" {
		t.Fatalf("content = %q", res.Content)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_a" || tc.Type != "function" || tc.Name != "get_weather" || tc.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call = %+v", tc)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", res.FinishReason)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 || res.Usage.ThinkingTokens != 2 ||
		res.Usage.CacheReadTokens != 3 || res.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	var sawText, sawTool bool
	for _, d := range deltas {
		sawText = sawText || d.Content != ""
		sawTool = sawTool || len(d.ToolCalls) > 0
	}
	if !sawText || !sawTool {
		t.Fatalf("deltas = %+v", deltas)
	}
}

func TestCodeBuddyChatRotatesAccounts(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer exhausted" {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"rate limit reached"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{
		HTTP:    &http.Client{},
		BaseURL: srv.URL,
		auths: []*auth.Auth{
			{Provider: "codebuddy", AccessToken: "exhausted"},
			{Provider: "codebuddy", AccessToken: "healthy"},
		},
	}
	res, err := e.Chat(context.Background(), &backend.ChatRequest{Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q", res.Content)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	q := e.Quota()
	if q.Accounts[0].Status != "limited" || q.Accounts[1].Status != "ready" {
		t.Fatalf("quota = %+v", q.Accounts)
	}
}

func TestCodeBuddyLogin(t *testing.T) {
	t.Setenv("SEAVX_NO_BROWSER", "1")
	var polls atomic.Int32
	jwt := fakeJWT(t, map[string]any{"email": "cb@example.com", "sub": "user-42", "exp": time.Now().Add(time.Hour).Unix()})
	var sawNoAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v2/plugin/auth/state":
			sawNoAuth = r.Header.Get("X-No-Authorization")
			if r.URL.Query().Get("platform") != "CLI" || r.URL.Query().Get("nonce") == "" {
				t.Errorf("state query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"code":0,"data":{"state":"st-1","authUrl":"https://example.test/login?state=st-1"}}`)
		case r.Method == "GET" && r.URL.Path == "/v2/plugin/auth/token":
			if r.URL.Query().Get("state") != "st-1" {
				t.Errorf("poll state = %q", r.URL.Query().Get("state"))
			}
			if polls.Add(1) == 1 {
				fmt.Fprint(w, `{"code":11217,"msg":"login pending"}`)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"accessToken":%q,"refreshToken":"rt-1","tokenType":"Bearer","expiresIn":3600,"domain":"www.codebuddy.ai"}}`, jwt)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	store := auth.AuthStore{Path: dir + "/auth.jsonc"}
	e := &CodeBuddyEngine{Store: store, HTTP: &http.Client{}, BaseURL: srv.URL, PollInterval: time.Millisecond}
	if err := e.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawNoAuth != "true" {
		t.Fatalf("state request missing X-No-Authorization: %q", sawNoAuth)
	}
	if polls.Load() < 2 {
		t.Fatalf("polls = %d, want at least 2", polls.Load())
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Provider != "codebuddy" || out[0].Email != "cb@example.com" ||
		out[0].AccountID != "user-42" || out[0].AccessToken != jwt || out[0].RefreshToken != "rt-1" {
		t.Fatalf("stored auth = %+v", out)
	}
	if !out[0].Expiry.After(time.Now()) {
		t.Fatalf("expiry = %v", out[0].Expiry)
	}
	if got := e.count(); got != 1 {
		t.Fatalf("engine accounts = %d", got)
	}
}

func TestCodeBuddyModelDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q", got)
		}
		fmt.Fprint(w, `{"code":0,"data":{"agents":[{"name":"cli","models":["gpt-6-astra","glm-5.3","gone"]}],`+
			`"models":[{"id":"gpt-6-astra","name":"GPT-6-Astra"},{"id":"glm-5.3","name":"GLM-5.3"},{"id":"gone","name":"Gone","disabled":true}]}}`)
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{HTTP: &http.Client{}, BaseURL: srv.URL, auths: []*auth.Auth{{Provider: "codebuddy", AccessToken: "tok"}}}
	models, err := e.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "gpt-6-astra" || models[0].Name != "GPT-6-Astra" {
		t.Fatalf("model[0] = %+v", models[0])
	}
	if models[1].ID != "glm-5.3" || models[1].Name != "GLM-5.3" {
		t.Fatalf("model[1] = %+v", models[1])
	}
	if !e.HasModel(context.Background(), "gpt-6-astra") {
		t.Fatal("discovered model not recognized")
	}
	if e.HasModel(context.Background(), "gone") {
		t.Fatal("disabled model must be rejected")
	}
}

func TestCodeBuddyModelDiscoveryFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "boom")
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{HTTP: &http.Client{}, BaseURL: srv.URL, auths: []*auth.Auth{{Provider: "codebuddy", AccessToken: "tok"}}}
	models, err := e.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range models {
		if m.ID == "auto-chat" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fallback catalog missing auto-chat: %d models", len(models))
	}
	if !e.HasModel(context.Background(), "auto-chat") {
		t.Fatal("fallback model not recognized")
	}
}

func TestCodeBuddyParallelToolCalls(t *testing.T) {
	// CodeBuddy reuses index 0 for parallel calls; the ID marks a new call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"one\",\"arguments\":\"{}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"two\",\"arguments\":\"{\\\"y\\\":2}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{HTTP: &http.Client{}, BaseURL: srv.URL, auths: []*auth.Auth{{Provider: "codebuddy", AccessToken: "t"}}}
	res, err := e.Chat(context.Background(), &backend.ChatRequest{Messages: []backend.ChatMessage{{Role: "user", Content: "go"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].ID != "call_a" || res.ToolCalls[0].Name != "one" {
		t.Fatalf("first call = %+v", res.ToolCalls[0])
	}
	if res.ToolCalls[1].ID != "call_b" || res.ToolCalls[1].Name != "two" || res.ToolCalls[1].Arguments != `{"y":2}` {
		t.Fatalf("second call = %+v", res.ToolCalls[1])
	}
}

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

func TestCodeBuddyQuotaTracking(t *testing.T) {
	a := &auth.Auth{Provider: "codebuddy", AccessToken: "tok", Email: "cb@example.com"}
	e := &CodeBuddyEngine{auths: []*auth.Auth{a}, DefaultModel: "auto-chat"}
	q := e.Quota()
	if q.Provider != "codebuddy" || q.DefaultModel != "auto-chat" || len(q.Accounts) != 1 || q.Accounts[0].Status != "ready" {
		t.Fatalf("initial quota = %+v", q)
	}
	e.trackQuota(a, errors.New("429 rate limit exceeded"))
	if q := e.Quota(); q.Accounts[0].Status != "limited" || q.Accounts[0].LimitedSince == nil {
		t.Fatalf("limited quota = %+v", q)
	}
	e.trackQuota(a, nil)
	if q := e.Quota(); q.Accounts[0].Status != "ready" {
		t.Fatalf("recovered quota = %+v", q)
	}
}

func TestCodeBuddyCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"agents":[{"name":"cli","models":["glm-5.3"]}],`+
			`"models":[{"id":"glm-5.3","name":"GLM-5.3"}]}}`)
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{HTTP: &http.Client{}, BaseURL: srv.URL, auths: []*auth.Auth{{Provider: "codebuddy", AccessToken: "tok"}}}
	checks := e.Check(context.Background())
	if len(checks) != 1 || checks[0].Err != nil {
		t.Fatalf("checks = %+v", checks)
	}
	models := checks[0].Models
	if len(models) != 1 || models[0].ID != "glm-5.3" {
		t.Fatalf("models = %+v", models)
	}
}

func TestCodeBuddyCheckReportsUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code":401,"msg":"invalid token"}`)
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{HTTP: &http.Client{}, BaseURL: srv.URL, auths: []*auth.Auth{{Provider: "codebuddy", AccessToken: "bad"}}}
	checks := e.Check(context.Background())
	if len(checks) != 1 || checks[0].Err == nil {
		t.Fatalf("Check must report upstream failures instead of falling back to the built-in catalog: %+v", checks)
	}
}

func TestCodeBuddyCheckIsolatesDeadAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":401,"msg":"invalid token"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"agents":[{"name":"cli","models":["glm-5.3"]}],`+
			`"models":[{"id":"glm-5.3","name":"GLM-5.3"}]}}`)
	}))
	defer srv.Close()

	e := &CodeBuddyEngine{HTTP: &http.Client{}, BaseURL: srv.URL, auths: []*auth.Auth{
		{Provider: "codebuddy", AccessToken: "good", Email: "ok@example.com"},
		{Provider: "codebuddy", AccessToken: "bad", Email: "dead@example.com"},
	}}
	checks := e.Check(context.Background())
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].Err != nil || len(checks[0].Models) != 1 {
		t.Fatalf("healthy account should pass: %+v", checks[0])
	}
	if checks[1].Err == nil {
		t.Fatalf("dead account should fail: %+v", checks[1])
	}
}

// Accounts added to the credentials file while the engine is running must be
// picked up without a restart, so a login performed against a live server
// takes effect on the next request.
func TestCodeBuddyHotReloadsAccounts(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, tok)
		mu.Unlock()
		if tok != "tok-new" {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"code":6004,"msg":"usage exceeds frequency limit"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "auth.jsonc")
	bump := time.Now()
	writeFile := func(auths ...*auth.Auth) {
		t.Helper()
		data, err := json.Marshal(auths)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		bump = bump.Add(time.Second)
		if err := os.Chtimes(path, bump, bump); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(&auth.Auth{Provider: "codebuddy", Email: "old@example.com", AccessToken: "tok-old"})
	store := auth.AuthStore{Path: path}
	initial, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewCodeBuddyEngine(store, initial)
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = &http.Client{}
	e.BaseURL = srv.URL

	req := &backend.ChatRequest{Model: "glm-5.3", Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}}}
	if _, err := e.Chat(context.Background(), req, nil); err == nil {
		t.Fatalf("single limited account should fail")
	}

	writeFile(
		&auth.Auth{Provider: "codebuddy", Email: "old@example.com", AccessToken: "tok-old"},
		&auth.Auth{Provider: "codebuddy", Email: "new@example.com", AccessToken: "tok-new"},
	)
	if n := e.count(); n != 2 {
		t.Fatalf("account count after reload = %d, want 2", n)
	}
	res, err := e.Chat(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("chat after reload: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q, want ok", res.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(seen, ","), "tok-old") || !strings.Contains(strings.Join(seen, ","), "tok-new") {
		t.Fatalf("expected both accounts to be attempted, saw %v", seen)
	}
}
