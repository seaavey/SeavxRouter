package completions

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"seavxrouter/internal/backend"
)

// --- shared request parsing ---

type textMessage struct {
	Role string
	Text string
}

func parseContent(raw json.RawMessage) (string, []backend.ImagePart, error) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil, nil
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, &apiError{http.StatusBadRequest, "content must be a string or an array of content parts", "invalid_request_error", "invalid_content"}
	}
	var b strings.Builder
	var images []backend.ImagePart
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text", "output_text":
			b.WriteString(p.Text)
		case "image_url", "input_image":
			img, err := parseImageField(p.ImageURL)
			if err != nil {
				return "", nil, err
			}
			images = append(images, img)
		default:
			return "", nil, &apiError{http.StatusBadRequest, "unsupported content part type: " + p.Type + " (only text and images are supported)", "invalid_request_error", "unsupported_content"}
		}
	}
	return b.String(), images, nil
}

// parseImageField accepts both wire forms: an object {"url": ...} from Chat
// Completions and a bare URL string from Responses input_image parts.
func parseImageField(raw json.RawMessage) (backend.ImagePart, error) {
	if len(raw) == 0 {
		return backend.ImagePart{}, &apiError{http.StatusBadRequest, "image part is missing image_url", "invalid_request_error", "invalid_content"}
	}
	var u string
	if json.Unmarshal(raw, &u) == nil {
		return parseImageURL(u)
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.URL == "" {
		return backend.ImagePart{}, &apiError{http.StatusBadRequest, "image_url must be a URL string or an object with url", "invalid_request_error", "invalid_content"}
	}
	return parseImageURL(obj.URL)
}

func parseImageURL(u string) (backend.ImagePart, error) {
	if u == "" {
		return backend.ImagePart{}, &apiError{http.StatusBadRequest, "image URL is empty", "invalid_request_error", "invalid_content"}
	}
	if data, ok := strings.CutPrefix(u, "data:"); ok {
		meta, payload, found := strings.Cut(data, ",")
		if !found || !strings.HasSuffix(meta, ";base64") || payload == "" {
			return backend.ImagePart{}, &apiError{http.StatusBadRequest, "image data URLs must look like data:<mime>;base64,<data>", "invalid_request_error", "invalid_content"}
		}
		mime := strings.TrimSuffix(meta, ";base64")
		if !strings.HasPrefix(mime, "image/") {
			return backend.ImagePart{}, &apiError{http.StatusBadRequest, "unsupported image media type: " + mime, "invalid_request_error", "unsupported_content"}
		}
		return backend.ImagePart{MIMEType: mime, Data: payload}, nil
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return backend.ImagePart{URL: u}, nil
	}
	return backend.ImagePart{}, &apiError{http.StatusBadRequest, "image URL must be a data: URI or an http(s) URL", "invalid_request_error", "invalid_content"}
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
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func mapUsage(u backend.Usage) openAIUsage {
	ou := openAIUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	ou.PromptTokensDetails.CachedTokens = u.CacheReadTokens
	ou.CompletionTokensDetails.ReasoningTokens = u.ThinkingTokens
	return ou
}

// --- GET /v1/models ---

func (s *Server) HandleModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.backend.Models(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "created": now, "owned_by": s.ownerOf(r.Context(), m.ID),
		})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

func (s *Server) HandleModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.backend.HasModel(r.Context(), id) {
		writeError(w, &apiError{http.StatusNotFound, "model not found: " + id, "invalid_request_error", "model_not_found"})
		return
	}
	writeJSON(w, map[string]any{
		"id": id, "object": "model", "created": time.Now().Unix(), "owned_by": s.ownerOf(r.Context(), id),
	})
}

// --- POST /v1/chat/completions ---

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role       string           `json:"role"`
		Content    json.RawMessage  `json:"content"`
		Name       string           `json:"name"`
		ToolCallID string           `json:"tool_call_id"`
		ToolCalls  []openAIToolCall `json:"tool_calls"`
	} `json:"messages"`
	Tools       []openAITool `json:"tools"`
	ToolChoice  any          `json:"tool_choice"`
	Stream      bool         `json:"stream"`
	Temperature *float64     `json:"temperature"`
	MaxTokens   *int         `json:"max_tokens"`
	ServiceTier string       `json:"service_tier"`

	MaxCompletionTokens *int `json:"max_completion_tokens"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// buildChatRequest converts an OpenAI chat request into the unified backend.ChatRequest.
func buildChatRequest(req chatRequest, model string) (*backend.ChatRequest, error) {
	maxTokens := req.MaxTokens
	if req.MaxCompletionTokens != nil {
		maxTokens = req.MaxCompletionTokens
	}
	cr := &backend.ChatRequest{
		Model:       model,
		Stream:      req.Stream,
		ToolChoice:  req.ToolChoice,
		Temperature: req.Temperature,
		MaxTokens:   maxTokens,
		ServiceTier: req.ServiceTier,
	}
	toolNames := map[string]string{}
	for _, m := range req.Messages {
		text, images, err := parseContent(m.Content)
		if err != nil {
			return nil, err
		}
		cm := backend.ChatMessage{Role: m.Role, Content: text, Images: images, Name: m.Name, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, backend.ToolCall{ID: tc.ID, Type: tc.Type, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
			toolNames[tc.ID] = tc.Function.Name
		}
		if cm.Role == "tool" && cm.Name == "" {
			cm.Name = toolNames[cm.ToolCallID]
		}
		cr.Messages = append(cr.Messages, cm)
	}
	for _, t := range req.Tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		cr.Tools = append(cr.Tools, backend.ToolDefinition{Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	return cr, nil
}

// openAIToolCalls renders unified tool calls in OpenAI wire form.
func openAIToolCalls(calls []backend.ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, tc := range calls {
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		out = append(out, map[string]any{
			"id":       tc.ID,
			"type":     typ,
			"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
		})
	}
	return out
}

func (s *Server) HandleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, err)
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
	s.setRequestMeta(r, model, req.Stream)
	cr, err := buildChatRequest(req, model)
	if err != nil {
		writeError(w, err)
		return
	}
	s.applyCompression(r, cr, "chat")
	created := time.Now().Unix()
	id := backend.NewID("chatcmpl-")

	if !req.Stream {
		result, err := s.backend.Chat(r.Context(), cr, nil)
		if err != nil {
			writeError(w, err)
			return
		}
		s.recordResult(r, "chat", model, result)
		var content any = strings.TrimRight(result.Content, "\n")
		finish := result.FinishReason
		if finish == "" {
			finish = "stop"
		}
		if len(result.ToolCalls) > 0 {
			if content == "" {
				content = nil
			}
			finish = "tool_calls"
		}
		message := map[string]any{"role": "assistant", "content": content}
		if len(result.ToolCalls) > 0 {
			message["tool_calls"] = openAIToolCalls(result.ToolCalls)
		}
		writeJSON(w, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}},
			"usage":   mapUsage(result.Usage),
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

	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	chunk := func(delta map[string]any, finish any) {
		payload := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	usageChunk := func(u openAIUsage) {
		payload := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{}, "usage": u,
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	chunk(map[string]any{"role": "assistant"}, nil)
	toolIndex := 0
	result, err := s.backend.Chat(r.Context(), cr, func(d backend.ChatDelta) {
		if d.Thinking != "" {
			chunk(map[string]any{"reasoning_content": d.Thinking}, nil)
		}
		if d.Content != "" {
			chunk(map[string]any{"content": d.Content}, nil)
		}
		if len(d.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(d.ToolCalls))
			for _, tc := range d.ToolCalls {
				tcs = append(tcs, map[string]any{
					"index":    toolIndex,
					"id":       tc.ID,
					"type":     "function",
					"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
				})
				toolIndex++
			}
			chunk(map[string]any{"tool_calls": tcs}, nil)
		}
	})
	if err != nil {
		slog.Warn("chat stream failed", "err", err)
		s.logFailure(r, failureStatus(err), err)
		typ, code := "backend_error", "backend_error"
		if backend.IsQuotaError(err) {
			typ, code = "insufficient_quota", "insufficient_quota"
		}
		data, _ := json.Marshal(map[string]any{
			"error": map[string]any{"message": err.Error(), "type": typ, "code": code},
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	s.recordResult(r, "chat", model, result)
	finish := result.FinishReason
	if finish == "" {
		finish = "stop"
	}
	if len(result.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	chunk(map[string]any{}, finish)
	if includeUsage {
		usageChunk(mapUsage(result.Usage))
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// --- POST /v1/responses ---

type responsesRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Instructions string          `json:"instructions"`
	ServiceTier  string          `json:"service_tier"`
	Stream       bool            `json:"stream"`
}

type responsesInputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

// parseResponsesOutput renders a function_call_output payload as text. The
// common form is a string; content-part arrays are concatenated.
func parseResponsesOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

func parseResponsesInput(raw json.RawMessage) ([]backend.ChatMessage, error) {
	if len(raw) == 0 {
		return nil, &apiError{http.StatusBadRequest, "input is required", "invalid_request_error", "missing_input"}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []backend.ChatMessage{{Role: "user", Content: s}}, nil
	}
	var items []responsesInputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &apiError{http.StatusBadRequest, "input must be a string or an array of messages", "invalid_request_error", "invalid_input"}
	}
	msgs := make([]backend.ChatMessage, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case "function_call":
			args := it.Arguments
			if args == "" {
				args = "{}"
			}
			call := backend.ToolCall{ID: it.CallID, Type: "function", Name: it.Name, Arguments: args}
			if n := len(msgs); n > 0 && msgs[n-1].Role == "assistant" && len(msgs[n-1].Images) == 0 {
				msgs[n-1].ToolCalls = append(msgs[n-1].ToolCalls, call)
				continue
			}
			msgs = append(msgs, backend.ChatMessage{Role: "assistant", ToolCalls: []backend.ToolCall{call}})
		case "function_call_output":
			msgs = append(msgs, backend.ChatMessage{Role: "tool", ToolCallID: it.CallID, Content: parseResponsesOutput(it.Output)})
		default:
			text, images, err := parseContent(it.Content)
			if err != nil {
				return nil, err
			}
			role := it.Role
			if role == "" {
				role = "user"
			}
			msgs = append(msgs, backend.ChatMessage{Role: role, Content: text, Images: images})
		}
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
	out := responsesOutput{ID: backend.NewID("msg_"), Type: "message", Status: status, Role: "assistant"}
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

func responsesUsage(u backend.Usage) map[string]any {
	return map[string]any{
		"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "total_tokens": u.TotalTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": u.ThinkingTokens},
	}
}

func (s *Server) HandleResponses(w http.ResponseWriter, r *http.Request) {
	var req responsesRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, err)
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
	s.setRequestMeta(r, model, req.Stream)
	if req.Instructions != "" {
		msgs = append([]backend.ChatMessage{{Role: "system", Content: req.Instructions}}, msgs...)
	}
	cr := &backend.ChatRequest{Model: model, Messages: msgs, Stream: req.Stream, ServiceTier: req.ServiceTier}
	s.applyCompression(r, cr, "responses")
	id := backend.NewID("resp_")

	if !req.Stream {
		result, err := s.backend.Chat(r.Context(), cr, nil)
		if err != nil {
			writeError(w, err)
			return
		}
		s.recordResult(r, "responses", model, result)
		text := strings.TrimRight(result.Content, "\n")
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
	result, err := s.backend.Chat(r.Context(), cr, func(d backend.ChatDelta) {
		if d.Content == "" {
			return
		}
		full.WriteString(d.Content)
		send("response.output_text.delta", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "delta": d.Content})
	})
	if err != nil {
		slog.Warn("responses stream failed", "err", err)
		s.logFailure(r, failureStatus(err), err)
		send("response.failed", map[string]any{"response": responseObject(id, model, "failed", []responsesOutput{}, nil)})
		return
	}
	s.recordResult(r, "responses", model, result)

	text := strings.TrimRight(full.String(), "\n")
	done := newResponsesOutput(text, "completed")
	done.ID = item.ID
	send("response.output_text.done", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "text": text})
	send("response.content_part.done", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "part": done.Content[0]})
	send("response.output_item.done", map[string]any{"output_index": 0, "item": done})
	send("response.completed", map[string]any{"response": responseObject(id, model, "completed", []responsesOutput{done}, responsesUsage(result.Usage))})
}
