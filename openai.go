package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func newID(prefix string) string {
	var b [12]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

type apiError struct {
	status int
	msg    string
	typ    string
	code   string
}

func (e *apiError) Error() string { return e.msg }

func writeError(w http.ResponseWriter, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = &apiError{http.StatusBadGateway, err.Error(), "backend_error", "backend_error"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": ae.msg, "type": ae.typ, "code": ae.code},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// --- shared request parsing ---

type textMessage struct {
	Role string
	Text string
}

func parseContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", &apiError{http.StatusBadRequest, "content must be a string or an array of text parts", "invalid_request_error", "invalid_content"}
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text", "output_text":
			b.WriteString(p.Text)
		default:
			return "", &apiError{http.StatusBadRequest, "unsupported content part type: " + p.Type + " (only text is supported)", "invalid_request_error", "unsupported_content"}
		}
	}
	return b.String(), nil
}

func buildPrompt(instructions string, msgs []textMessage) string {
	if instructions == "" && len(msgs) == 1 && msgs[0].Role == "user" {
		return msgs[0].Text
	}
	var b strings.Builder
	if instructions != "" {
		b.WriteString(instructions)
		b.WriteString("\n\n")
	}
	for _, m := range msgs {
		var label string
		switch m.Role {
		case "system", "developer":
			label = "[System instructions]\n"
		case "user":
			label = "User: "
		case "assistant":
			label = "Assistant: "
		case "tool":
			label = "Tool result: "
		default:
			continue
		}
		b.WriteString(label)
		b.WriteString(m.Text)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply to the final user message.")
	return b.String()
}

type openAIUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func mapUsage(u Usage) openAIUsage {
	ou := openAIUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	ou.CompletionTokensDetails.ReasoningTokens = u.ThinkingTokens
	return ou
}

func (s *Server) resolveModel(w http.ResponseWriter, r *http.Request, requested string) (string, bool) {
	model := requested
	if model == "" || model == "auto" || model == "default" {
		model = s.backend.Default()
	}
	if model != "" && !s.backend.HasModel(r.Context(), model) {
		writeError(w, &apiError{http.StatusNotFound, "model not found: " + model, "invalid_request_error", "model_not_found"})
		return "", false
	}
	return model, true
}

// --- GET /v1/models ---

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.backend.Models(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "created": now, "owned_by": "antigravity",
		})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

// --- POST /v1/chat/completions ---

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
		writeError(w, &apiError{http.StatusBadRequest, "invalid JSON body: " + err.Error(), "invalid_request_error", "invalid_json"})
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, &apiError{http.StatusBadRequest, "messages must not be empty", "invalid_request_error", "missing_messages"})
		return
	}
	msgs := make([]textMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		text, err := parseContent(m.Content)
		if err != nil {
			writeError(w, err)
			return
		}
		msgs = append(msgs, textMessage{Role: m.Role, Text: text})
	}
	model, ok := s.resolveModel(w, r, req.Model)
	if !ok {
		return
	}
	prompt := buildPrompt("", msgs)
	created := time.Now().Unix()
	id := newID("chatcmpl-")

	if !req.Stream {
		result, err := s.backend.Complete(r.Context(), model, prompt)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": model,
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant", "content": strings.TrimRight(result.Response, "\n"),
				},
				"finish_reason": "stop",
			}},
			"usage": mapUsage(result.Usage),
		})
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

	chunk := func(delta map[string]any, finish any, usage any) {
		payload := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if usage != nil {
			payload["usage"] = usage
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	chunk(map[string]any{"role": "assistant"}, nil, nil)
	result, err := s.backend.Stream(r.Context(), model, prompt, func(delta string) {
		chunk(map[string]any{"content": delta}, nil, nil)
	})
	if err != nil {
		slog.Warn("chat stream failed", "err", err)
		data, _ := json.Marshal(map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "backend_error", "code": "backend_error"},
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	chunk(map[string]any{}, "stop", mapUsage(result.Usage))
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// --- POST /v1/responses ---

type responsesRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Instructions string          `json:"instructions"`
	Stream       bool            `json:"stream"`
}

func parseResponsesInput(raw json.RawMessage) ([]textMessage, error) {
	if len(raw) == 0 {
		return nil, &apiError{http.StatusBadRequest, "input is required", "invalid_request_error", "missing_input"}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []textMessage{{Role: "user", Text: s}}, nil
	}
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &apiError{http.StatusBadRequest, "input must be a string or an array of messages", "invalid_request_error", "invalid_input"}
	}
	msgs := make([]textMessage, 0, len(items))
	for _, it := range items {
		text, err := parseContent(it.Content)
		if err != nil {
			return nil, err
		}
		role := it.Role
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, textMessage{Role: role, Text: text})
	}
	return msgs, nil
}

type responsesOutput struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Role    string `json:"role"`
	Content []struct {
		Type        string   `json:"type"`
		Text        string   `json:"text"`
		Annotations []string `json:"annotations"`
	} `json:"content"`
}

func newResponsesOutput(text, status string) responsesOutput {
	out := responsesOutput{ID: newID("msg_"), Type: "message", Status: status, Role: "assistant"}
	out.Content = append(out.Content, struct {
		Type        string   `json:"type"`
		Text        string   `json:"text"`
		Annotations []string `json:"annotations"`
	}{Type: "output_text", Text: text, Annotations: []string{}})
	return out
}

func responseObject(id, model, status string, output []responsesOutput, usage any) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "created_at": time.Now().Unix(),
		"status": status, "model": model, "output": output, "usage": usage, "error": nil,
	}
}

func responsesUsage(u Usage) map[string]any {
	return map[string]any{
		"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "total_tokens": u.TotalTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": u.ThinkingTokens},
	}
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	var req responsesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
		writeError(w, &apiError{http.StatusBadRequest, "invalid JSON body: " + err.Error(), "invalid_request_error", "invalid_json"})
		return
	}
	msgs, err := parseResponsesInput(req.Input)
	if err != nil {
		writeError(w, err)
		return
	}
	model, ok := s.resolveModel(w, r, req.Model)
	if !ok {
		return
	}
	prompt := buildPrompt(req.Instructions, msgs)
	id := newID("resp_")

	if !req.Stream {
		result, err := s.backend.Complete(r.Context(), model, prompt)
		if err != nil {
			writeError(w, err)
			return
		}
		text := strings.TrimRight(result.Response, "\n")
		writeJSON(w, responseObject(id, model, "completed", []responsesOutput{newResponsesOutput(text, "completed")}, responsesUsage(result.Usage)))
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

	item := newResponsesOutput("", "in_progress")
	send("response.created", map[string]any{"response": responseObject(id, model, "in_progress", []responsesOutput{}, nil)})
	send("response.output_item.added", map[string]any{"output_index": 0, "item": item})
	send("response.content_part.added", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "part": item.Content[0]})

	var full strings.Builder
	result, err := s.backend.Stream(r.Context(), model, prompt, func(delta string) {
		full.WriteString(delta)
		send("response.output_text.delta", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "delta": delta})
	})
	if err != nil {
		slog.Warn("responses stream failed", "err", err)
		send("response.failed", map[string]any{"response": responseObject(id, model, "failed", []responsesOutput{}, nil)})
		return
	}

	text := strings.TrimRight(full.String(), "\n")
	done := newResponsesOutput(text, "completed")
	done.ID = item.ID
	send("response.output_text.done", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "text": text})
	send("response.content_part.done", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "part": done.Content[0]})
	send("response.output_item.done", map[string]any{"output_index": 0, "item": done})
	send("response.completed", map[string]any{"response": responseObject(id, model, "completed", []responsesOutput{done}, responsesUsage(result.Usage))})
}
