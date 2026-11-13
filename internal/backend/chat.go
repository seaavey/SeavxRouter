package backend

import (
	"context"
	"fmt"
)

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

// ImagePart is one image attached to a chat message. Inline images carry
// base64 Data plus MIMEType; remote images carry URL instead.
type ImagePart struct {
	MIMEType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

// DataURL renders inline images as a data: URL for backends that accept
// images in OpenAI/Responses wire form, or returns the remote URL unchanged.
func (p ImagePart) DataURL() string {
	if p.URL != "" {
		return p.URL
	}
	return "data:" + p.MIMEType + ";base64," + p.Data
}

// ChatMessage is one turn in a structured conversation. ToolCalls is set on
// assistant turns that invoke tools; ToolCallID/Name are set on tool results.
type ChatMessage struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	Images     []ImagePart `json:"images,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

// HasImages reports whether any message carries image content.
func HasImages(msgs []ChatMessage) bool {
	for _, m := range msgs {
		if len(m.Images) > 0 {
			return true
		}
	}
	return false
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
	ServiceTier string           `json:"service_tier,omitempty"`
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

// ChatMessagesToPrompt flattens structured messages into a single text prompt
// for backends without native tool calling.
func ChatMessagesToPrompt(msgs []ChatMessage) string {
	tm := make([]textMessage, 0, len(msgs))
	for _, m := range msgs {
		tm = append(tm, textMessage{Role: m.Role, Text: m.Content})
	}
	return buildPrompt("", tm)
}

// ChatViaText serves a ChatRequest through a backend's plain-text
// Complete/Stream path. It preserves existing text behavior for backends that
// do not implement native function calling.
func ChatViaText(ctx context.Context, b BackendAPI, req *ChatRequest, onDelta func(ChatDelta)) (*ChatResult, error) {
	if HasImages(req.Messages) {
		return nil, fmt.Errorf("this backend does not support image content; use a native provider such as antigravity or codex for vision requests")
	}
	prompt := ChatMessagesToPrompt(req.Messages)
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
