package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/compress"
)

func compressionDiffFixture() string {
	var b strings.Builder
	b.WriteString("diff --git a/main.go b/main.go\nindex 1234567..89abcde 100644\n--- a/main.go\n+++ b/main.go\n@@ -1,220 +1,220 @@\n")
	for i := 0; i < 200; i++ {
		switch i {
		case 60:
			b.WriteString("-old line\n+new line\n")
		case 140:
			b.WriteString("-old line two\n+new line two\n")
		default:
			fmt.Fprintf(&b, " context %03d padding padding padding padding\n", i)
		}
	}
	return b.String()
}

func compressionChatBody() []byte {
	body := map[string]any{
		"model": "m",
		"messages": []any{
			map[string]any{"role": "user", "content": "fix this"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]any{"name": "Bash", "arguments": `{"command":"git diff"}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": compressionDiffFixture()},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestCompressionChat(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s := &Server{backend: stub, compressor: compress.NewDefault()}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(compressionChatBody())))
	s.handleChat(w, req)
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if stub.gotReq == nil {
		t.Fatal("backend did not receive a request")
	}
	msgs := stub.gotReq.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	if msgs[0].Content != "fix this" {
		t.Errorf("user message changed: %q", msgs[0].Content)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Arguments != `{"command":"git diff"}` {
		t.Errorf("assistant tool call changed: %+v", msgs[1].ToolCalls)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("tool message = %+v", msgs[2])
	}
	if len(msgs[2].Content) >= len(compressionDiffFixture()) {
		t.Error("tool result was not compressed")
	}
	if !strings.Contains(msgs[2].Content, "seavx:") || !strings.Contains(msgs[2].Content, "+new line") {
		t.Errorf("compressed tool result lost structure:\n%s", msgs[2].Content)
	}
}

func TestCompressionAnthropicMessages(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "done", FinishReason: "stop"}}
	s := &Server{backend: stub, compressor: compress.NewDefault()}
	body := map[string]any{
		"model":      "m",
		"max_tokens": 128,
		"messages": []any{
			map[string]any{"role": "user", "content": "fix this"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "checking"},
				map[string]any{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": map[string]any{"command": "git diff"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": compressionDiffFixture()},
			}},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleAnthropicMessages(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(raw))))
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if stub.gotReq == nil {
		t.Fatal("backend did not receive a request")
	}
	var tool *backend.ChatMessage
	for i := range stub.gotReq.Messages {
		if stub.gotReq.Messages[i].Role == "tool" {
			tool = &stub.gotReq.Messages[i]
		}
	}
	if tool == nil {
		t.Fatalf("no tool message in %+v", stub.gotReq.Messages)
	}
	if tool.ToolCallID != "tu_1" {
		t.Errorf("tool_call_id = %q", tool.ToolCallID)
	}
	if len(tool.Content) >= len(compressionDiffFixture()) {
		t.Error("anthropic tool_result was not compressed")
	}
	if !strings.Contains(tool.Content, "seavx:") {
		t.Errorf("missing compression marker:\n%s", tool.Content)
	}
}

func TestCompressionEndpointStats(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s := NewServer(stub, "", nil)
	h := s.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(compressionChatBody()))))
	if w.Code != 200 {
		t.Fatalf("chat code %d body %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/compression", nil))
	if w.Code != 200 {
		t.Fatalf("stats code %d body %s", w.Code, w.Body.String())
	}
	var stats struct {
		Enabled        bool    `json:"enabled"`
		Requests       int64   `json:"requests"`
		ToolResults    int64   `json:"tool_results"`
		Compressed     int64   `json:"compressed"`
		BytesBefore    int64   `json:"bytes_before"`
		BytesAfter     int64   `json:"bytes_after"`
		BytesSaved     int64   `json:"bytes_saved"`
		SavedPercent   float64 `json:"saved_percent"`
		EstTokensSaved int64   `json:"estimated_tokens_saved"`
		MinBytes       int     `json:"min_bytes"`
		MinSavings     float64 `json:"min_savings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if !stats.Enabled || stats.Requests != 1 || stats.ToolResults != 1 || stats.Compressed != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.BytesBefore <= 0 || stats.BytesAfter >= stats.BytesBefore || stats.BytesSaved != stats.BytesBefore-stats.BytesAfter {
		t.Fatalf("byte stats = %+v", stats)
	}
	if stats.EstTokensSaved != stats.BytesSaved/4 || stats.SavedPercent <= 0 {
		t.Fatalf("derived stats = %+v", stats)
	}
	if stats.MinBytes != 2000 || stats.MinSavings != 0.20 {
		t.Fatalf("thresholds = %+v", stats)
	}
}

func TestCompressionDisabled(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s := NewServer(stub, "", nil, WithoutCompression())
	w := httptest.NewRecorder()
	s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(compressionChatBody()))))
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if stub.gotReq.Messages[2].Content != compressionDiffFixture() {
		t.Error("disabled server modified tool content")
	}

	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/compression", nil))
	var stats struct {
		Enabled  bool  `json:"enabled"`
		Requests int64 `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Enabled || stats.Requests != 0 {
		t.Fatalf("disabled stats = %+v", stats)
	}
}

func TestCompressionResponses(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "done", FinishReason: "stop"}}
	s := &Server{backend: stub, compressor: compress.NewDefault()}
	body := map[string]any{
		"model": "m",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "fix this"},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": `{"command":"git diff"}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": compressionDiffFixture()},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleResponses(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(raw))))
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if stub.gotReq == nil {
		t.Fatal("backend did not receive a request")
	}
	msgs := stub.gotReq.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages = %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 ||
		msgs[1].ToolCalls[0].ID != "call_1" || msgs[1].ToolCalls[0].Name != "Bash" {
		t.Fatalf("function_call not converted: %+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("function_call_output not converted: %+v", msgs[2])
	}
	if len(msgs[2].Content) >= len(compressionDiffFixture()) {
		t.Error("responses tool output was not compressed")
	}
	if !strings.Contains(msgs[2].Content, "seavx:") || !strings.Contains(msgs[2].Content, "+new line") {
		t.Errorf("compressed responses tool output lost structure:\n%s", msgs[2].Content)
	}
}
