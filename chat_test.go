package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestChatRequestJSONRoundTrip(t *testing.T) {
	temp := 0.7
	maxTok := 1024
	req := ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []ChatMessage{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Name: "get_weather", Arguments: `{"city":"SF"}`}}},
			{Role: "tool", ToolCallID: "call_1", Name: "get_weather", Content: `{"temp":70}`},
		},
		Tools:       []ToolDefinition{{Name: "get_weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}}},
		ToolChoice:  "auto",
		Stream:      true,
		Temperature: &temp,
		MaxTokens:   &maxTok,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got ChatRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req, got) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", req, got)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "messages", "tools", "tool_choice", "stream", "temperature", "max_tokens"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing wire field %q in %s", key, data)
		}
	}
	msgs := raw["messages"].([]any)
	if _, ok := msgs[1].(map[string]any)["tool_calls"]; !ok {
		t.Fatalf("assistant missing tool_calls: %s", data)
	}
	if _, ok := msgs[2].(map[string]any)["tool_call_id"]; !ok {
		t.Fatalf("tool message missing tool_call_id: %s", data)
	}
}

func TestChatRequestOmitEmpty(t *testing.T) {
	req := ChatRequest{Model: "x", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tools", "tool_choice", "temperature", "max_tokens"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("expected %q omitted, got %s", key, data)
		}
	}
}

func TestMultiBackendChatDelegates(t *testing.T) {
	agy := &fakeBackend{models: map[string]bool{"gemini-3.8-flash-high": true}}
	m := &MultiBackend{antigravity: agy}
	var deltas []ChatDelta
	req := &ChatRequest{Model: "gemini.3.8-flash", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
	res, err := m.Chat(context.Background(), req, func(d ChatDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if agy.lastModel != "gemini-3.8-flash-high" {
		t.Fatalf("backend got model %q, want normalized id", agy.lastModel)
	}
	if res == nil || res.Content != "ok" {
		t.Fatalf("result not passed through: %+v", res)
	}
	if len(deltas) != 1 || deltas[0].Content != "x" {
		t.Fatalf("onDelta not wired: %+v", deltas)
	}
	if req.Model != "gemini.3.8-flash" {
		t.Fatalf("caller request was mutated: %q", req.Model)
	}
}

func TestBackendChatPlainText(t *testing.T) {
	f := &fakeBackend{}
	req := &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	res, err := chatViaText(context.Background(), f, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello" || res.FinishReason != "stop" {
		t.Fatalf("non-stream chat: %+v", res)
	}

	var deltas []ChatDelta
	sreq := &ChatRequest{Model: "m", Stream: true, Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
	res, err = chatViaText(context.Background(), f, sreq, func(d ChatDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello" {
		t.Fatalf("stream chat result: %+v", res)
	}
	if len(deltas) != 1 || deltas[0].Content != "hello" {
		t.Fatalf("stream deltas not wired: %+v", deltas)
	}
}