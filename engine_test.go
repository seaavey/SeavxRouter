package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestStripJSONC(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"line comment", "// header\n{\"a\":1}\n// tail", "{\"a\":1}\n"},
		{"block comment", "/* hi */{\"a\":1}", "{\"a\":1}"},
		{"slashes in string", `{"url":"https://x.dev/a//b"}`, `{"url":"https://x.dev/a//b"}`},
		{"escaped quote", `{"s":"a\"//b"}`, `{"s":"a\"//b"}`},
		{"trailing comment", "{\"a\":1} // note", "{\"a\":1} "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(stripJSONC([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	in := []*Auth{
		{AccessToken: "at1", RefreshToken: "1//rt1", ProjectID: "proj1", Tier: "pro", Email: "a@x.dev"},
		{AccessToken: "at2", RefreshToken: "1//rt2", ProjectID: "proj2", Email: "b@x.dev"},
	}
	if err := store.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].RefreshToken != "1//rt1" || out[1].ProjectID != "proj2" {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestLoadLegacySingleObject(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	legacy := "// legacy\n{\"access_token\":\"at\",\"refresh_token\":\"1//rt\",\"project_id\":\"p\"}\n"
	if err := os.WriteFile(store.Path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].RefreshToken != "1//rt" {
		t.Fatalf("legacy load mismatch: %+v", out)
	}
}

func TestSaveRotatedRefreshToken(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	agy := []*Auth{
		{Provider: "antigravity", RefreshToken: "1//a", Email: "a@x.dev"},
		{Provider: "antigravity", RefreshToken: "1//b", Email: "b@x.dev"},
	}
	codex := []*Auth{{Provider: "codex", RefreshToken: "rt-c", Email: "c@x.dev"}}
	if err := store.Save(agy); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(codex); err != nil {
		t.Fatal(err)
	}
	// codex refreshes its token and saves only its own subset
	codex[0].RefreshToken = "rt-c2"
	if err := store.Save(codex); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 accounts, got %d: %+v", len(out), out)
	}
	counts := map[string]int{}
	for _, a := range out {
		counts[a.Provider]++
	}
	if counts["antigravity"] != 2 || counts["codex"] != 1 {
		t.Fatalf("provider counts wrong: %v", counts)
	}
}

func TestEngineConversions(t *testing.T) {
	t.Run("contents", func(t *testing.T) {
		msgs := []ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "thinking"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}}},
			{Role: "tool", Name: "f", ToolCallID: "call_1", Content: "result"},
		}
		got := chatContents(msgs)
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
		if got[3]["role"] != "function" {
			t.Fatalf("tool role = %v, want function", got[3]["role"])
		}
		fr := got[3]["parts"].([]map[string]any)[0]["functionResponse"].(map[string]any)
		if fr["name"] != "f" || fr["response"].(map[string]any)["output"] != "result" {
			t.Fatalf("functionResponse = %v", fr)
		}
	})
	t.Run("tools", func(t *testing.T) {
		tools := []ToolDefinition{{Name: "f", Description: "d", Parameters: map[string]any{
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
		fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"get_weather\",\"args\":{\"city\":\"SF\"},\"id\":\"call_abc\"}}]}}]}}\n\n")
		fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"cachedContentTokenCount\":7,\"totalTokenCount\":15}}}\n\n")
	}))
	defer srv.Close()

	e, err := NewEngine(AuthStore{}, srv.URL, []*Auth{{AccessToken: "t", Expiry: time.Now().Add(time.Hour), ProjectID: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []ChatDelta
	req := &ChatRequest{
		Model:    "gemini-3.8-flash-high",
		Messages: []ChatMessage{{Role: "user", Content: "weather?"}},
		Tools:    []ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
		Stream:   true,
	}
	res, err := e.Chat(context.Background(), req, func(d ChatDelta) { deltas = append(deltas, d) })
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
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 || res.Usage.CacheReadTokens != 7 || res.Usage.TotalTokens != 15 {
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
}
