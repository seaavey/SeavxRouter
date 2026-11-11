package main

import "context"

// ToolDefinition is a provider-neutral function/tool schema. Parameters holds
// a JSON Schema object.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolCall is a single function invocation requested by the model. Arguments
// is a JSON-encoded string (OpenAI wire form).
type ToolCall struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatMessage is one turn in a structured conversation. ToolCalls is set on
// assistant turns that invoke tools; ToolCallID/Name are set on tool results.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ChatRequest is the unified structured chat request passed to backends.
type ChatRequest struct {
	Model       string           `json:"model"`
	Messages    []ChatMessage    `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	ToolChoice  any              `json:"tool_choice,omitempty"`
	Stream      bool             `json:"stream"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
}

// ChatDelta is one incremental update emitted while streaming a chat response.
type ChatDelta struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
}

// ChatResult is the final aggregated chat response.
type ChatResult struct {
	Content      string
	ToolCalls    []ToolCall
	Usage        Usage
	FinishReason string
}

// chatMessagesToPrompt flattens structured messages into a single text prompt
// for backends without native tool calling.
func chatMessagesToPrompt(msgs []ChatMessage) string {
	tm := make([]textMessage, 0, len(msgs))
	for _, m := range msgs {
		tm = append(tm, textMessage{Role: m.Role, Text: m.Content})
	}
	return buildPrompt("", tm)
}

// chatViaText serves a ChatRequest through a backend's plain-text
// Complete/Stream path. It preserves existing text behavior for backends that
// do not implement native function calling.
func chatViaText(ctx context.Context, b BackendAPI, req *ChatRequest, onDelta func(ChatDelta)) (*ChatResult, error) {
	prompt := chatMessagesToPrompt(req.Messages)
	if req.Stream {
		r, err := b.Stream(ctx, req.Model, prompt, func(s string) {
			if onDelta != nil {
				onDelta(ChatDelta{Content: s})
			}
		})
		if err != nil {
			return nil, err
		}
		return &ChatResult{Content: r.Response, Usage: r.Usage, FinishReason: "stop"}, nil
	}
	r, err := b.Complete(ctx, req.Model, prompt)
	if err != nil {
		return nil, err
	}
	return &ChatResult{Content: r.Response, Usage: r.Usage, FinishReason: "stop"}, nil
}