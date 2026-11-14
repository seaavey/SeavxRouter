package completions

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"seavxrouter/internal/backend"
)

func TestAnthropicMessages(t *testing.T) {
	t.Run("non-streaming tool_use", func(t *testing.T) {
		stub := &chatStub{result: &backend.ChatResult{
			Content:      "Let me check.",
			ToolCalls:    []backend.ToolCall{{ID: "toolu_1", Type: "function", Name: "get_weather", Arguments: `{"city":"SF"}`}},
			FinishReason: "tool_calls",
			Usage:        backend.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 7, TotalTokens: 15},
		}}
		s := &Server{backend: stub}
		body := `{"model":"m","max_tokens":1024,"system":"You are helpful.","messages":[{"role":"user","content":"weather?"}],"tools":[{"name":"get_weather","description":"d","input_schema":{"type":"object"}}]}`
		w := httptest.NewRecorder()
		s.HandleAnthropicMessages(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		var resp struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type  string         `json:"type"`
				Text  string         `json:"text"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      struct {
				InputTokens          int `json:"input_tokens"`
				OutputTokens         int `json:"output_tokens"`
				CacheReadInputTokens int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Type != "message" || resp.Role != "assistant" || !strings.HasPrefix(resp.ID, "msg_") {
			t.Fatalf("envelope = %+v", resp)
		}
		if resp.StopReason != "tool_use" {
			t.Fatalf("stop_reason = %q", resp.StopReason)
		}
		if len(resp.Content) != 2 {
			t.Fatalf("content = %s", w.Body.String())
		}
		if resp.Content[0].Type != "text" || resp.Content[0].Text != "Let me check." {
			t.Fatalf("text block = %+v", resp.Content[0])
		}
		if resp.Content[1].Type != "tool_use" || resp.Content[1].Name != "get_weather" || resp.Content[1].Input["city"] != "SF" {
			t.Fatalf("tool_use block = %+v", resp.Content[1])
		}
		if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.CacheReadInputTokens != 7 {
			t.Fatalf("usage = %+v", resp.Usage)
		}
		if stub.gotReq == nil || len(stub.gotReq.Tools) != 1 || stub.gotReq.Tools[0].Name != "get_weather" {
			t.Fatalf("tools not parsed: %+v", stub.gotReq)
		}
		if len(stub.gotReq.Messages) == 0 || stub.gotReq.Messages[0].Role != "system" || stub.gotReq.Messages[0].Content != "You are helpful." {
			t.Fatalf("system not parsed: %+v", stub.gotReq.Messages)
		}
	})

	t.Run("streaming sequence", func(t *testing.T) {
		tc := backend.ToolCall{ID: "toolu_1", Type: "function", Name: "f", Arguments: `{"x":1}`}
		stub := &chatStub{
			result: &backend.ChatResult{Content: "hi", ToolCalls: []backend.ToolCall{tc}, FinishReason: "tool_calls", Usage: backend.Usage{OutputTokens: 5}},
			deltas: []backend.ChatDelta{{Content: "hi"}, {ToolCalls: []backend.ToolCall{tc}}},
		}
		s := &Server{backend: stub}
		body := `{"model":"m","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
		w := httptest.NewRecorder()
		s.HandleAnthropicMessages(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

		var events, blockStartTypes []string
		var sawTextDelta, sawInputJSONDelta bool
		var finalStop string
		starts, stops := 0, 0
		for _, line := range strings.Split(w.Body.String(), "\n") {
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev struct {
				Type         string `json:"type"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(payload), &ev) != nil {
				continue
			}
			events = append(events, ev.Type)
			switch ev.Type {
			case "content_block_start":
				starts++
				blockStartTypes = append(blockStartTypes, ev.ContentBlock.Type)
			case "content_block_stop":
				stops++
			case "content_block_delta":
				if ev.Delta.Type == "text_delta" {
					sawTextDelta = true
				}
				if ev.Delta.Type == "input_json_delta" {
					sawInputJSONDelta = true
					if ev.Delta.PartialJSON != `{"x":1}` {
						t.Fatalf("partial_json = %q", ev.Delta.PartialJSON)
					}
				}
			case "message_delta":
				finalStop = ev.Delta.StopReason
			}
		}
		if len(events) < 3 {
			t.Fatalf("too few events: %v\n%s", events, w.Body.String())
		}
		if events[0] != "message_start" {
			t.Fatalf("first event = %q", events[0])
		}
		if events[len(events)-1] != "message_stop" || events[len(events)-2] != "message_delta" {
			t.Fatalf("tail events = %v", events)
		}
		if starts != stops {
			t.Fatalf("block start/stop mismatch: %d vs %d", starts, stops)
		}
		if !sawTextDelta || !sawInputJSONDelta {
			t.Fatalf("missing deltas: text=%v input_json=%v", sawTextDelta, sawInputJSONDelta)
		}
		if len(blockStartTypes) != 2 || blockStartTypes[0] != "text" || blockStartTypes[1] != "tool_use" {
			t.Fatalf("block types = %v", blockStartTypes)
		}
		if finalStop != "tool_use" {
			t.Fatalf("message_delta stop_reason = %q", finalStop)
		}
	})

	t.Run("image blocks", func(t *testing.T) {
		stub := &chatStub{result: &backend.ChatResult{Content: "red", FinishReason: "stop"}}
		s := &Server{backend: stub}
		body := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + testPNGB64 + `"}},` +
			`{"type":"text","text":"what color?"}]}]}`
		w := httptest.NewRecorder()
		s.HandleAnthropicMessages(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		if stub.gotReq == nil || len(stub.gotReq.Messages) != 1 {
			t.Fatalf("messages not parsed: %+v", stub.gotReq)
		}
		msg := stub.gotReq.Messages[0]
		if msg.Content != "what color?" || len(msg.Images) != 1 || msg.Images[0].MIMEType != "image/png" || msg.Images[0].Data != testPNGB64 {
			t.Fatalf("image message = %+v", msg)
		}
	})

	t.Run("tool_result images", func(t *testing.T) {
		stub := &chatStub{result: &backend.ChatResult{Content: "done", FinishReason: "stop"}}
		s := &Server{backend: stub}
		body := `{"model":"m","max_tokens":64,"messages":[` +
			`{"role":"user","content":"read the screenshot"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"url","url":"https://example.com/x.png"}}]}]}` +
			`]}`
		w := httptest.NewRecorder()
		s.HandleAnthropicMessages(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		if stub.gotReq == nil || len(stub.gotReq.Messages) != 4 {
			t.Fatalf("messages not parsed: %+v", stub.gotReq)
		}
		if stub.gotReq.Messages[2].Role != "tool" || stub.gotReq.Messages[2].ToolCallID != "toolu_1" {
			t.Fatalf("tool message = %+v", stub.gotReq.Messages[2])
		}
		after := stub.gotReq.Messages[3]
		if after.Role != "user" || len(after.Images) != 1 || after.Images[0].URL != "https://example.com/x.png" {
			t.Fatalf("tool_result image not carried: %+v", after)
		}
	})

	t.Run("unsupported image source", func(t *testing.T) {
		s := &Server{backend: &chatStub{result: &backend.ChatResult{Content: "x"}}}
		body := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[` +
			`{"type":"image","source":{"type":"file","file_id":"f1"}}]}]}`
		w := httptest.NewRecorder()
		s.HandleAnthropicMessages(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "unsupported image source type") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})

	t.Run("tool_result parsing", func(t *testing.T) {
		stub := &chatStub{result: &backend.ChatResult{Content: "done", FinishReason: "stop"}}
		s := &Server{backend: stub}
		body := `{"model":"m","max_tokens":100,"messages":[` +
			`{"role":"user","content":"weather?"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}` +
			`]}`
		w := httptest.NewRecorder()
		s.HandleAnthropicMessages(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		if stub.gotReq == nil {
			t.Fatal("no request captured")
		}
		var toolMsg *backend.ChatMessage
		for i := range stub.gotReq.Messages {
			if stub.gotReq.Messages[i].Role == "tool" {
				toolMsg = &stub.gotReq.Messages[i]
			}
		}
		if toolMsg == nil {
			t.Fatalf("no tool message: %+v", stub.gotReq.Messages)
		}
		if toolMsg.ToolCallID != "toolu_1" || toolMsg.Name != "get_weather" || toolMsg.Content != "sunny" {
			t.Fatalf("tool message = %+v", toolMsg)
		}
		asst := stub.gotReq.Messages[1]
		if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Name != "get_weather" || asst.ToolCalls[0].Arguments != `{"city":"SF"}` {
			t.Fatalf("assistant tool_use = %+v", asst)
		}
	})
}
