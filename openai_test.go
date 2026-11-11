package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type chatStub struct {
	gotReq *ChatRequest
	result *ChatResult
	deltas []ChatDelta
}

func (c *chatStub) Models(context.Context) ([]Model, error) { return []Model{{ID: "m"}}, nil }
func (c *chatStub) HasModel(context.Context, string) bool   { return true }
func (c *chatStub) Complete(context.Context, string, string) (*Result, error) {
	return &Result{Status: "SUCCESS"}, nil
}
func (c *chatStub) Stream(context.Context, string, string, func(string)) (*Result, error) {
	return &Result{Status: "SUCCESS"}, nil
}
func (c *chatStub) Chat(_ context.Context, req *ChatRequest, onDelta func(ChatDelta)) (*ChatResult, error) {
	c.gotReq = req
	if onDelta != nil {
		for _, d := range c.deltas {
			onDelta(d)
		}
	}
	return c.result, nil
}
func (c *chatStub) Default() string { return "m" }

func TestOpenAIToolCalls(t *testing.T) {
	t.Run("non-streaming", func(t *testing.T) {
		stub := &chatStub{result: &ChatResult{
			ToolCalls:    []ToolCall{{ID: "call_1", Type: "function", Name: "get_weather", Arguments: `{"city":"SF"}`}},
			FinishReason: "tool_calls",
			Usage:        Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 7, TotalTokens: 15},
		}}
		s := &Server{backend: stub}
		body := `{"model":"m","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"d","parameters":{"type":"object"}}}]}`
		w := httptest.NewRecorder()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		var resp struct {
			Choices []struct {
				Message struct {
					Role      string `json:"role"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens        int `json:"prompt_tokens"`
				TotalTokens         int `json:"total_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Choices) != 1 {
			t.Fatalf("choices: %s", w.Body.String())
		}
		ch := resp.Choices[0]
		if ch.FinishReason != "tool_calls" {
			t.Fatalf("finish_reason = %q", ch.FinishReason)
		}
		if len(ch.Message.ToolCalls) != 1 {
			t.Fatalf("tool_calls: %s", w.Body.String())
		}
		tc := ch.Message.ToolCalls[0]
		if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "get_weather" || tc.Function.Arguments != `{"city":"SF"}` {
			t.Fatalf("tool call = %+v", tc)
		}
		if resp.Usage.PromptTokens != 10 || resp.Usage.TotalTokens != 15 || resp.Usage.PromptTokensDetails.CachedTokens != 7 {
			t.Fatalf("usage = %+v", resp.Usage)
		}
		if stub.gotReq == nil || len(stub.gotReq.Tools) != 1 || stub.gotReq.Tools[0].Name != "get_weather" {
			t.Fatalf("tools not parsed: %+v", stub.gotReq)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		tc := ToolCall{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}
		stub := &chatStub{
			result: &ChatResult{ToolCalls: []ToolCall{tc}, FinishReason: "tool_calls"},
			deltas: []ChatDelta{{ToolCalls: []ToolCall{tc}}},
		}
		s := &Server{backend: stub}
		body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		w := httptest.NewRecorder()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		var toolChunkFound bool
		var finalFinish string
		for _, line := range strings.Split(w.Body.String(), "\n") {
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok || payload == "[DONE]" {
				continue
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
				continue
			}
			c := chunk.Choices[0]
			if len(c.Delta.ToolCalls) > 0 {
				toolChunkFound = true
				got := c.Delta.ToolCalls[0]
				if got.Index != 0 || got.ID != "call_1" || got.Type != "function" || got.Function.Name != "f" || got.Function.Arguments != `{"x":1}` {
					t.Fatalf("stream tool chunk = %+v", got)
				}
			}
			if c.FinishReason != nil {
				finalFinish = *c.FinishReason
			}
		}
		if !toolChunkFound {
			t.Fatalf("no tool_calls chunk in %s", w.Body.String())
		}
		if finalFinish != "tool_calls" {
			t.Fatalf("final finish_reason = %q", finalFinish)
		}
	})

	t.Run("multi-turn parsing", func(t *testing.T) {
		stub := &chatStub{result: &ChatResult{Content: "done", FinishReason: "stop"}}
		s := &Server{backend: stub}
		body := `{"model":"m","messages":[` +
			`{"role":"user","content":"weather?"},` +
			`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},` +
			`{"role":"tool","tool_call_id":"call_1","name":"get_weather","content":"sunny"}` +
			`]}`
		w := httptest.NewRecorder()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		if stub.gotReq == nil || len(stub.gotReq.Messages) != 3 {
			t.Fatalf("messages not parsed: %+v", stub.gotReq)
		}
		asst := stub.gotReq.Messages[1]
		if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Name != "get_weather" || asst.ToolCalls[0].Arguments != `{"city":"SF"}` {
			t.Fatalf("assistant tool_calls = %+v", asst)
		}
		tool := stub.gotReq.Messages[2]
		if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Name != "get_weather" || tool.Content != "sunny" {
			t.Fatalf("tool message = %+v", tool)
		}
	})
}
