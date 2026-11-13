package antigravity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

func TestEngineConversions(t *testing.T) {
	t.Run("contents", func(t *testing.T) {
		msgs := []backend.ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "thinking"},
			{Role: "assistant", ToolCalls: []backend.ToolCall{{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}}},
			{Role: "tool", Name: "f", ToolCallID: "call_1", Content: "result"},
		}
		got, err := chatContents(msgs, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 4 {
			t.Fatalf("contents len = %d, want 4", len(got))
		}
		if got[0]["role"] != "user" {
			t.Fatalf("user role = %v", got[0]["role"])
		}
		if got[1]["role"] != "model" {
			t.Fatalf("assistant text role = %v, want model", got[1]["role"])
		}
		if got[2]["role"] != "model" {
			t.Fatalf("assistant tool role = %v, want model", got[2]["role"])
		}
		parts := got[2]["parts"].([]map[string]any)
		fc := parts[0]["functionCall"].(map[string]any)
		if fc["name"] != "f" || fc["id"] != "call_1" {
			t.Fatalf("functionCall = %v", fc)
		}
		if fc["args"].(map[string]any)["x"] != float64(1) {
			t.Fatalf("args = %v", fc["args"])
		}
		if got[3]["role"] != "user" {
			t.Fatalf("tool role = %v, want user", got[3]["role"])
		}
		fr := got[3]["parts"].([]map[string]any)[0]["functionResponse"].(map[string]any)
		if fr["name"] != "f" || fr["response"].(map[string]any)["output"] != "result" {
			t.Fatalf("functionResponse = %v", fr)
		}
		if fr["id"] != "call_1" {
			t.Fatalf("functionResponse id = %v, want call_1", fr["id"])
		}
	})
	t.Run("thought signature", func(t *testing.T) {
		msgs := []backend.ChatMessage{
			{Role: "assistant", ToolCalls: []backend.ToolCall{{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}}},
		}
		sig := func(id string) string {
			if id == "call_1" {
				return "SIGBASE64"
			}
			return ""
		}
		got, err := chatContents(msgs, sig)
		if err != nil {
			t.Fatal(err)
		}
		part := got[0]["parts"].([]map[string]any)[0]
		if part["thoughtSignature"] != "SIGBASE64" {
			t.Fatalf("thoughtSignature not attached at part level: %v", part)
		}
		fc := part["functionCall"].(map[string]any)
		if _, ok := fc["thoughtSignature"]; ok {
			t.Fatalf("thoughtSignature must not be nested in functionCall: %v", fc)
		}
		gotNone, err := chatContents(msgs, func(string) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		partNone := gotNone[0]["parts"].([]map[string]any)[0]
		if _, ok := partNone["thoughtSignature"]; ok {
			t.Fatalf("thoughtSignature should be absent when unavailable: %v", partNone)
		}
	})
	t.Run("assistant text with tool calls", func(t *testing.T) {
		msgs := []backend.ChatMessage{
			{Role: "assistant", Content: "Let me check.", ToolCalls: []backend.ToolCall{{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}}},
		}
		got, err := chatContents(msgs, nil)
		if err != nil {
			t.Fatal(err)
		}
		parts := got[0]["parts"].([]map[string]any)
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2 (text + functionCall): %v", len(parts), parts)
		}
		if parts[0]["text"] != "Let me check." {
			t.Fatalf("assistant text dropped: %v", parts[0])
		}
		if _, ok := parts[1]["functionCall"]; !ok {
			t.Fatalf("functionCall missing: %v", parts[1])
		}
	})
	t.Run("images", func(t *testing.T) {
		msgs := []backend.ChatMessage{{Role: "user", Content: "what is this?", Images: []backend.ImagePart{{MIMEType: "image/png", Data: "aGVsbG8="}}}}
		got, err := chatContents(msgs, nil)
		if err != nil {
			t.Fatal(err)
		}
		parts := got[0]["parts"].([]map[string]any)
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2 (text + inlineData): %v", len(parts), parts)
		}
		inline := parts[1]["inlineData"].(map[string]any)
		if inline["mimeType"] != "image/png" || inline["data"] != "aGVsbG8=" {
			t.Fatalf("inlineData = %v", inline)
		}

		remote := []backend.ChatMessage{{Role: "user", Images: []backend.ImagePart{{URL: "https://example.com/a.png"}}}}
		if _, err := chatContents(remote, nil); err == nil {
			t.Fatal("remote image URL should be rejected for the antigravity backend")
		}
	})
	t.Run("tools", func(t *testing.T) {
		tools := []backend.ToolDefinition{{Name: "f", Description: "d", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"x": map[string]any{"type": "string"}},
		}}}
		got := chatTools(tools)
		if len(got) != 1 {
			t.Fatalf("tools len = %d, want 1", len(got))
		}
		decls := got[0]["functionDeclarations"].([]map[string]any)
		if decls[0]["name"] != "f" || decls[0]["description"] != "d" {
			t.Fatalf("declaration = %v", decls[0])
		}
		params := decls[0]["parameters"].(map[string]any)
		if params["type"] != "OBJECT" {
			t.Fatalf("params type = %v, want OBJECT", params["type"])
		}
		if params["properties"].(map[string]any)["x"].(map[string]any)["type"] != "STRING" {
			t.Fatalf("nested type not uppercased: %v", params["properties"])
		}
	})
	t.Run("no tools", func(t *testing.T) {
		if got := chatTools(nil); got != nil {
			t.Fatalf("chatTools(nil) = %v, want nil", got)
		}
	})
	t.Run("finish reason", func(t *testing.T) {
		cases := []struct {
			reason   string
			hasTools bool
			want     string
		}{
			{"STOP", false, "stop"},
			{"MAX_TOKENS", false, "length"},
			{"STOP", true, "tool_calls"},
			{"", false, "stop"},
		}
		for _, tc := range cases {
			if got := mapFinishReason(tc.reason, tc.hasTools); got != tc.want {
				t.Fatalf("mapFinishReason(%q,%v) = %q, want %q", tc.reason, tc.hasTools, got, tc.want)
			}
		}
	})
}

func TestEngineChatStreamFunctionCall(t *testing.T) {
	var upstreamBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"thoughtSignature\":\"SIG123\",\"functionCall\":{\"name\":\"get_weather\",\"args\":{\"city\":\"SF\"},\"id\":\"call_abc\"}}]}}]}}\n\n")
		fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"thoughtsTokenCount\":3,\"cachedContentTokenCount\":7,\"totalTokenCount\":15}}}\n\n")
	}))
	defer srv.Close()

	e, err := NewEngine(auth.AuthStore{}, srv.URL, []*auth.Auth{{AccessToken: "t", Expiry: time.Now().Add(time.Hour), ProjectID: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []backend.ChatDelta
	req := &backend.ChatRequest{
		Model:    "gemini-3.8-flash-high",
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
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 8 || res.Usage.ThinkingTokens != 3 || res.Usage.CacheReadTokens != 7 || res.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if !bytes.Contains(upstreamBody, []byte("functionDeclarations")) {
		t.Fatalf("upstream missing functionDeclarations: %s", upstreamBody)
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
	if e.lookupSig("call_abc") != "SIG123" {
		t.Fatalf("thoughtSignature not captured: %q", e.lookupSig("call_abc"))
	}

	roundTrip := &backend.ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []backend.ChatMessage{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []backend.ToolCall{tc}},
			{Role: "tool", Name: "get_weather", ToolCallID: "call_abc", Content: "sunny"},
		},
		Tools:  []backend.ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
		Stream: true,
	}
	if _, err := e.Chat(context.Background(), roundTrip, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(upstreamBody, []byte(`"thoughtSignature":"SIG123"`)) {
		t.Fatalf("round-trip upstream missing thoughtSignature: %s", upstreamBody)
	}
}

func TestEngineCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1internal:fetchAvailableModels" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q", got)
		}
		fmt.Fprint(w, `{"models":{"zeta":{},"alpha":{}}}`)
	}))
	defer srv.Close()

	e := &Engine{
		BaseURL: srv.URL,
		HTTP:    srv.Client(),
		auths:   []*auth.Auth{{Provider: "antigravity", AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}},
	}
	checks := e.Check(context.Background())
	if len(checks) != 1 {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].Err != nil {
		t.Fatal(checks[0].Err)
	}
	models := checks[0].Models
	if len(models) != 2 || models[0].ID != "alpha" || models[1].ID != "zeta" {
		t.Fatalf("models = %+v", models)
	}
}
