package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// --- POST /v1/messages (Anthropic Messages API) ---

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      json.RawMessage    `json:"system"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools"`
	Stream      bool               `json:"stream"`
	MaxTokens   *int               `json:"max_tokens"`
	Temperature *float64           `json:"temperature"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func parseAnthropicSystem(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", &apiError{http.StatusBadRequest, "system must be a string or an array of text blocks", "invalid_request_error", "invalid_system"}
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	return b.String(), nil
}

func parseToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []anthropicBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Type == "text" {
				b.WriteString(bl.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// anthropicToMessages converts Anthropic messages into unified chat messages,
// mapping assistant tool_use blocks to ToolCalls and user tool_result blocks to
// tool results (resolving the function name from the originating tool_use id).
func anthropicToMessages(msgs []anthropicMessage) ([]ChatMessage, error) {
	var out []ChatMessage
	toolNames := map[string]string{}
	for _, m := range msgs {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			out = append(out, ChatMessage{Role: m.Role, Content: s})
			continue
		}
		var blocks []anthropicBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			return nil, &apiError{http.StatusBadRequest, "content must be a string or an array of content blocks", "invalid_request_error", "invalid_content"}
		}
		if m.Role == "assistant" {
			cm := ChatMessage{Role: "assistant"}
			var text strings.Builder
			for _, bl := range blocks {
				switch bl.Type {
				case "text":
					text.WriteString(bl.Text)
				case "tool_use":
					args := "{}"
					if len(bl.Input) > 0 {
						args = string(bl.Input)
					}
					cm.ToolCalls = append(cm.ToolCalls, ToolCall{ID: bl.ID, Type: "function", Name: bl.Name, Arguments: args})
					toolNames[bl.ID] = bl.Name
				}
			}
			cm.Content = text.String()
			out = append(out, cm)
			continue
		}
		var text strings.Builder
		var results []ChatMessage
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				text.WriteString(bl.Text)
			case "tool_result":
				results = append(results, ChatMessage{
					Role:       "tool",
					ToolCallID: bl.ToolUseID,
					Name:       toolNames[bl.ToolUseID],
					Content:    parseToolResultContent(bl.Content),
				})
			}
		}
		out = append(out, results...)
		if text.Len() > 0 {
			out = append(out, ChatMessage{Role: "user", Content: text.String()})
		}
	}
	return out, nil
}

func buildAnthropicChatRequest(req anthropicRequest, model string) (*ChatRequest, error) {
	cr := &ChatRequest{Model: model, Stream: req.Stream, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	sys, err := parseAnthropicSystem(req.System)
	if err != nil {
		return nil, err
	}
	if sys != "" {
		cr.Messages = append(cr.Messages, ChatMessage{Role: "system", Content: sys})
	}
	msgs, err := anthropicToMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	cr.Messages = append(cr.Messages, msgs...)
	for _, t := range req.Tools {
		cr.Tools = append(cr.Tools, ToolDefinition{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})
	}
	return cr, nil
}

func anthropicStopReason(result *ChatResult) string {
	if len(result.ToolCalls) > 0 {
		return "tool_use"
	}
	if result.FinishReason == "length" {
		return "max_tokens"
	}
	return "end_turn"
}

func anthropicUsage(u Usage) map[string]any {
	return map[string]any{
		"input_tokens":            u.InputTokens,
		"output_tokens":           u.OutputTokens,
		"cache_read_input_tokens": u.CacheReadTokens,
	}
}

func anthropicMessageObject(id, model string, result *ChatResult) map[string]any {
	content := []map[string]any{}
	if result.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": result.Content})
	}
	for _, tc := range result.ToolCalls {
		var input any = map[string]any{}
		if tc.Arguments != "" {
			var parsed any
			if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
				input = parsed
			}
		}
		content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input})
	}
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content":       content,
		"stop_reason":   anthropicStopReason(result),
		"stop_sequence": nil,
		"usage":         anthropicUsage(result.Usage),
	}
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	var req anthropicRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
		writeError(w, &apiError{http.StatusBadRequest, "invalid JSON body: " + err.Error(), "invalid_request_error", "invalid_json"})
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, &apiError{http.StatusBadRequest, "messages must not be empty", "invalid_request_error", "missing_messages"})
		return
	}
	model, ok := s.resolveModel(w, r, req.Model)
	if !ok {
		return
	}
	cr, err := buildAnthropicChatRequest(req, model)
	if err != nil {
		writeError(w, err)
		return
	}
	id := newID("msg_")

	if !req.Stream {
		result, err := s.backend.Chat(r.Context(), cr, nil)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, anthropicMessageObject(id, model, result))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, &apiError{http.StatusInternalServerError, "streaming unsupported", "server_error", "streaming_unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload map[string]any) {
		payload["type"] = event
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	send("message_start", map[string]any{"message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
	}})

	blockIdx := 0
	textOpen := false
	closeText := func() {
		if textOpen {
			send("content_block_stop", map[string]any{"index": blockIdx})
			blockIdx++
			textOpen = false
		}
	}

	result, err := s.backend.Chat(r.Context(), cr, func(d ChatDelta) {
		if d.Content != "" {
			if !textOpen {
				send("content_block_start", map[string]any{"index": blockIdx, "content_block": map[string]any{"type": "text", "text": ""}})
				textOpen = true
			}
			send("content_block_delta", map[string]any{"index": blockIdx, "delta": map[string]any{"type": "text_delta", "text": d.Content}})
		}
		if len(d.ToolCalls) > 0 {
			closeText()
			for _, tc := range d.ToolCalls {
				send("content_block_start", map[string]any{"index": blockIdx, "content_block": map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": map[string]any{}}})
				send("content_block_delta", map[string]any{"index": blockIdx, "delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Arguments}})
				send("content_block_stop", map[string]any{"index": blockIdx})
				blockIdx++
			}
		}
	})
	if err != nil {
		slog.Warn("anthropic stream failed", "err", err)
		send("error", map[string]any{"error": map[string]any{"type": "backend_error", "message": err.Error()}})
		return
	}

	closeText()
	if blockIdx == 0 {
		send("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		send("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": ""}})
		send("content_block_stop", map[string]any{"index": 0})
	}
	send("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": anthropicStopReason(result), "stop_sequence": nil},
		"usage": anthropicUsage(result.Usage),
	})
	send("message_stop", map[string]any{})
}
