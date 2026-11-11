package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCodexChatConversions(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		msgs := []ChatMessage{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "thinking"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}}},
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
		tools := []ToolDefinition{{Name: "f", Description: "d", Parameters: map[string]any{"type": "object"}}}
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

	e, err := NewCodexEngine(AuthStore{}, []*Auth{{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	var deltas []ChatDelta
	req := &ChatRequest{
		Model:    "gpt-5-codex",
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

	e, err := NewCodexEngine(AuthStore{}, []*Auth{{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	e.BaseURL = srv.URL
	req := &ChatRequest{
		Model: "gpt-5-codex",
		Messages: []ChatMessage{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_abc", Type: "function", Name: "get_weather", Arguments: `{"city":"SF"}`}}},
			{Role: "tool", Name: "get_weather", ToolCallID: "call_abc", Content: "sunny"},
		},
		Tools: []ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
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
