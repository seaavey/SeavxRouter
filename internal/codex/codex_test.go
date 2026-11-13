package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

func TestCodexChatConversions(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		msgs := []backend.ChatMessage{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "thinking"},
			{Role: "assistant", ToolCalls: []backend.ToolCall{{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}}},
			{Role: "tool", Name: "f", ToolCallID: "call_1", Content: "result"},
		}
		instructions, input := codexChatInput(msgs)
		if instructions != "be helpful" {
			t.Fatalf("instructions = %q", instructions)
		}
		if len(input) != 4 {
			t.Fatalf("input len = %d, want 4: %v", len(input), input)
		}
		if input[0]["type"] != "message" || input[0]["role"] != "user" {
			t.Fatalf("user item = %v", input[0])
		}
		uc := input[0]["content"].([]map[string]any)[0]
		if uc["type"] != "input_text" || uc["text"] != "hi" {
			t.Fatalf("user content = %v", uc)
		}
		if input[1]["type"] != "message" || input[1]["role"] != "assistant" {
			t.Fatalf("assistant item = %v", input[1])
		}
		ac := input[1]["content"].([]map[string]any)[0]
		if ac["type"] != "output_text" || ac["text"] != "thinking" {
			t.Fatalf("assistant content = %v", ac)
		}
		if input[2]["type"] != "function_call" || input[2]["call_id"] != "call_1" || input[2]["name"] != "f" || input[2]["arguments"] != `{"x":1}` {
			t.Fatalf("function_call item = %v", input[2])
		}
		if input[3]["type"] != "function_call_output" || input[3]["call_id"] != "call_1" || input[3]["output"] != "result" {
			t.Fatalf("function_call_output item = %v", input[3])
		}
	})
	t.Run("tools", func(t *testing.T) {
		tools := []backend.ToolDefinition{{Name: "f", Description: "d", Parameters: map[string]any{"type": "object"}}}
		got := codexChatTools(tools)
		if len(got) != 1 {
			t.Fatalf("tools len = %d, want 1", len(got))
		}
		if got[0]["type"] != "function" || got[0]["name"] != "f" || got[0]["description"] != "d" {
			t.Fatalf("tool = %v", got[0])
		}
		if got[0]["parameters"].(map[string]any)["type"] != "object" {
			t.Fatalf("parameters should keep lowercase JSON Schema: %v", got[0]["parameters"])
		}
		if got[0]["strict"] != false {
			t.Fatalf("strict = %v, want false", got[0]["strict"])
		}
		if codexChatTools(nil) != nil {
			t.Fatalf("codexChatTools(nil) should be nil")
		}
	})
}

func TestCodexChatImages(t *testing.T) {
	_, input := codexChatInput([]backend.ChatMessage{{Role: "user", Content: "look", Images: []backend.ImagePart{{MIMEType: "image/png", Data: "aGVsbG8="}}}})
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1: %v", len(input), input)
	}
	content := input[0]["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("content = %v", content)
	}
	if content[0]["type"] != "input_text" || content[0]["text"] != "look" {
		t.Fatalf("text part = %v", content[0])
	}
	if content[1]["type"] != "input_image" || content[1]["image_url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image part = %v", content[1])
	}
}

func TestCodexChatStreamFunctionCall(t *testing.T) {
	var upstreamBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_abc\",\"name\":\"get_weather\",\"arguments\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc_1\",\"delta\":\"\\\"SF\\\"}\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_abc\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"SF\\\"}\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"input_tokens_details\":{\"cached_tokens\":7},\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n")
	}))
	defer srv.Close()

	e, err := NewCodexEngine(auth.AuthStore{}, []*auth.Auth{{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	var deltas []backend.ChatDelta
	req := &backend.ChatRequest{
		Model:    "gpt-5-codex",
		Messages: []backend.ChatMessage{{Role: "user", Content: "weather?"}},
		Tools:    []backend.ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
		Stream:   true,
	}
	res, err := e.Chat(context.Background(), req, func(d backend.ChatDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	tc := res.ToolCalls[0]
	if tc.Name != "get_weather" || tc.ID != "call_abc" || tc.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call = %+v", tc)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", res.FinishReason)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q", res.Content)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 || res.Usage.CacheReadTokens != 7 || res.Usage.TotalTokens != 15 || res.Usage.ThinkingTokens != 3 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if !bytes.Contains(upstreamBody, []byte(`"type":"function"`)) || !bytes.Contains(upstreamBody, []byte("get_weather")) {
		t.Fatalf("upstream missing function tools: %s", upstreamBody)
	}
	var sawTool, sawText bool
	for _, d := range deltas {
		if len(d.ToolCalls) > 0 {
			sawTool = true
		}
		if d.Content != "" {
			sawText = true
		}
	}
	if !sawTool || !sawText {
		t.Fatalf("deltas missing tool/text: %+v", deltas)
	}
}

func TestCodexChatRequestRoundTrip(t *testing.T) {
	var upstreamBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	e, err := NewCodexEngine(auth.AuthStore{}, []*auth.Auth{{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	req := &backend.ChatRequest{
		Model: "gpt-5-codex",
		Messages: []backend.ChatMessage{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []backend.ToolCall{{ID: "call_abc", Type: "function", Name: "get_weather", Arguments: `{"city":"SF"}`}}},
			{Role: "tool", Name: "get_weather", ToolCallID: "call_abc", Content: "sunny"},
		},
		Tools: []backend.ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
	}
	if _, err := e.Chat(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(upstreamBody, &payload); err != nil {
		t.Fatal(err)
	}
	var sawCall, sawOutput bool
	for _, it := range payload.Input {
		switch it["type"] {
		case "function_call":
			sawCall = it["call_id"] == "call_abc" && it["name"] == "get_weather"
		case "function_call_output":
			sawOutput = it["call_id"] == "call_abc" && it["output"] == "sunny"
		}
	}
	if !sawCall || !sawOutput {
		t.Fatalf("round-trip input missing function_call/output: %s", upstreamBody)
	}
}

func TestCodexChatServiceTier(t *testing.T) {
	cases := []struct {
		name string
		tier string
		want string
	}{
		{"fast alias", "fast", "priority"},
		{"priority", "priority", "priority"},
		{"case and spaces", " Fast ", "priority"},
		{"default fast", "", "priority"},
		{"standard opt-out", "standard", ""},
		{"unsupported", "flex", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
			}))
			defer srv.Close()

			e, err := NewCodexEngine(auth.AuthStore{}, []*auth.Auth{{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}})
			if err != nil {
				t.Fatal(err)
			}
			e.BaseURL = srv.URL
			req := &backend.ChatRequest{
				Model:       "gpt-6-luna",
				Messages:    []backend.ChatMessage{{Role: "user", Content: "hi"}},
				ServiceTier: tc.tier,
			}
			if _, err := e.Chat(context.Background(), req, nil); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(upstreamBody, &payload); err != nil {
				t.Fatal(err)
			}
			got, present := payload["service_tier"]
			if tc.want == "" {
				if present {
					t.Fatalf("service_tier should be omitted, payload: %s", upstreamBody)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("service_tier = %v, want %q (payload %s)", got, tc.want, upstreamBody)
			}
		})
	}
}

func TestCodexLiveUsageWindows(t *testing.T) {
	resetPrimary := int64(1794467946)
	resetSecondary := int64(1794854808)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wham/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "acc-1" {
			t.Errorf("chatgpt-account-id = %q", got)
		}
		calls.Add(1)
		fmt.Fprintf(w, `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":8,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":24,"limit_window_seconds":604800,"reset_at":%d}}}`, resetPrimary, resetSecondary)
	}))
	defer srv.Close()

	a := &auth.Auth{AccessToken: "tok", AccountID: "acc-1", Expiry: time.Now().Add(time.Hour)}
	e, err := NewCodexEngine(auth.AuthStore{}, []*auth.Auth{a})
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	e.HTTP = srv.Client()

	windows := e.Quota().Accounts[0].Windows
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].Name != "5h" || windows[0].WindowMinutes != 300 || windows[0].UsedPercent == nil || *windows[0].UsedPercent != 8 {
		t.Fatalf("primary window = %+v", windows[0])
	}
	if windows[0].ResetsAt == nil || !windows[0].ResetsAt.Equal(time.Unix(resetPrimary, 0).UTC()) {
		t.Fatalf("primary resets_at = %v", windows[0].ResetsAt)
	}
	if windows[1].Name != "weekly" || windows[1].WindowMinutes != 10080 || windows[1].UsedPercent == nil || *windows[1].UsedPercent != 24 {
		t.Fatalf("secondary window = %+v", windows[1])
	}
	if windows[1].ResetsAt == nil || !windows[1].ResetsAt.Equal(time.Unix(resetSecondary, 0).UTC()) {
		t.Fatalf("secondary resets_at = %v", windows[1].ResetsAt)
	}

	if calls.Load() != 1 {
		t.Fatalf("usage calls = %d, want 1", calls.Load())
	}
	_ = e.Quota()
	if calls.Load() != 1 {
		t.Fatalf("cached quota should not refetch, calls = %d", calls.Load())
	}
}

func TestCodexQuotaTracking(t *testing.T) {
	a := &auth.Auth{Email: "c@example.com", AccountID: "acc-1"}
	e := &CodexEngine{auths: []*auth.Auth{a}, DefaultModel: "gpt-5.6-luna"}

	q := e.Quota()
	if q.Provider != "codex" || len(q.Accounts) != 1 {
		t.Fatalf("quota = %+v", q)
	}
	if q.Accounts[0].AccountID != "acc-1" || q.Accounts[0].Status != "ready" || q.Accounts[0].AccessTokenExpiresAt != nil {
		t.Fatalf("account = %+v", q.Accounts[0])
	}

	e.trackQuota(a, errors.New("usage_limit reached"))
	if q = e.Quota(); q.Accounts[0].Status != "limited" {
		t.Fatalf("expected limited account, got %+v", q.Accounts[0])
	}
}

func TestCodexQuotaRateLimitWindows(t *testing.T) {
	resetPrimary := time.Unix(1770000000, 0).UTC()
	resetSecondary := time.Unix(1770500000, 0).UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-codex-primary-used-percent", "62.5")
		w.Header().Set("x-codex-primary-window-minutes", "300")
		w.Header().Set("x-codex-primary-reset-at", "1770000000")
		w.Header().Set("x-codex-secondary-used-percent", "18")
		w.Header().Set("x-codex-secondary-window-minutes", "10080")
		w.Header().Set("x-codex-secondary-reset-at", "1770500000")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	a := &auth.Auth{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}
	e, err := NewCodexEngine(auth.AuthStore{}, []*auth.Auth{a})
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	req := &backend.ChatRequest{Model: "gpt-5-codex", Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}}}
	if _, err := e.Chat(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}

	windows := e.Quota().Accounts[0].Windows
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	primary, secondary := windows[0], windows[1]
	if primary.Name != "5h" || primary.WindowMinutes != 300 || primary.UsedPercent == nil || *primary.UsedPercent != 62.5 {
		t.Fatalf("primary window = %+v", primary)
	}
	if primary.ResetsAt == nil || !primary.ResetsAt.Equal(resetPrimary) {
		t.Fatalf("primary resets_at = %v, want %v", primary.ResetsAt, resetPrimary)
	}
	if secondary.Name != "weekly" || secondary.WindowMinutes != 10080 || secondary.UsedPercent == nil || *secondary.UsedPercent != 18 {
		t.Fatalf("secondary window = %+v", secondary)
	}
	if secondary.ResetsAt == nil || !secondary.ResetsAt.Equal(resetSecondary) {
		t.Fatalf("secondary resets_at = %v, want %v", secondary.ResetsAt, resetSecondary)
	}
}

func TestCodexCheck(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q", got)
		}
		calls.Add(1)
		fmt.Fprint(w, `{"models":[{"id":"gpt-b"},{"slug":"gpt-a"}]}`)
	}))
	defer srv.Close()

	e, err := NewCodexEngine(auth.AuthStore{}, []*auth.Auth{{Provider: "codex", AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	e.HTTP = srv.Client()

	if _, err := e.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	checks := e.Check(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (Check must bypass the Models cache)", calls.Load())
	}
	if len(checks) != 1 || checks[0].Err != nil {
		t.Fatalf("checks = %+v", checks)
	}
	models := checks[0].Models
	if len(models) != 2 || models[0].ID != "gpt-a" || models[1].ID != "gpt-b" {
		t.Fatalf("models = %+v", models)
	}
}
