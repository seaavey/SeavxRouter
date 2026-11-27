package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// This file carries the Anthropic Messages API wire form. A custom provider
// configured with sdk "anthropic" runs through the same Engine: account
// rotation, catalog caching and probing are shared, only the wire differs.

// apiRoot returns the URL prefix for an SDK-specific path. Operators paste a
// base URL with or without the trailing version segment, so both forms land on
// the documented endpoint.
func (e *Engine) apiRoot(version string) string {
	base := strings.TrimRight(e.BaseURL, "/")
	if strings.HasSuffix(base, "/"+version) {
		return base
	}
	return base + "/" + version
}

func (e *Engine) anthropicURL(path string) string {
	return e.apiRoot("v1") + path
}

// anthropicHeaders applies the Messages API auth and version headers.
func anthropicHeaders(req *http.Request, a *auth.Auth) {
	if a.AccessToken != "" {
		req.Header.Set("x-api-key", a.AccessToken)
	}
	req.Header.Set("anthropic-version", AnthropicVersion)
}

type anthropicModelEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type anthropicModelList struct {
	Data    []anthropicModelEntry `json:"data"`
	HasMore bool                  `json:"has_more"`
	LastID  string                `json:"last_id"`
}

// fetchAnthropicModels reads GET /v1/models, following the cursor until the
// catalog is complete. Anthropic caps a page at 1000 entries, so one request
// covers every real account, but paging keeps larger gateways correct.
func (e *Engine) fetchAnthropicModels(ctx context.Context, a *auth.Auth) ([]backend.Model, error) {
	out := make([]backend.Model, 0, 32)
	after := ""
	for page := 0; page < 20; page++ {
		url := e.anthropicURL("/models?limit=1000")
		if after != "" {
			url += "&after_id=" + after
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		anthropicHeaders(req, a)
		req.Header.Set("Accept", "application/json")
		resp, err := e.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, anthropicStatusError(e.Name, resp.StatusCode, body)
		}
		if readErr != nil {
			return nil, readErr
		}
		var list anthropicModelList
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("%s models: %w", e.Name, err)
		}
		for _, m := range list.Data {
			if m.ID == "" {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = m.ID
			}
			out = append(out, backend.Model{ID: m.ID, Name: name})
		}
		if !list.HasMore || list.LastID == "" || list.LastID == after {
			break
		}
		after = list.LastID
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s models: empty catalog", e.Name)
	}
	return out, nil
}

// anthropicStatusError classifies a non-2xx response the way the OpenAI path
// does, so account rotation and probe messaging keep working.
func anthropicStatusError(name string, status int, body []byte) error {
	msg := anthropicErrorMessage(body)
	detail := fmt.Sprintf("%s %d", http.StatusText(status), status)
	if msg != "" {
		detail = fmt.Sprintf("%s %d: %s", http.StatusText(status), status, msg)
	}
	httpErr := &backend.HTTPError{Status: status, Message: detail}
	switch status {
	case http.StatusPaymentRequired, http.StatusTooManyRequests:
		return fmt.Errorf("quota: %w", httpErr)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("unauthorized: %w", httpErr)
	}
	return httpErr
}

func anthropicErrorMessage(body []byte) string {
	var envelope struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error != nil {
		if envelope.Error.Message != "" {
			return envelope.Error.Message
		}
		return envelope.Error.Type
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 200 {
		trimmed = trimmed[:200] + "..."
	}
	return trimmed
}

// anthropicChat serves a ChatRequest over the Messages API, rotating accounts
// on auth and quota failures exactly like the OpenAI path.
func (e *Engine) anthropicChat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	model := req.Model
	if model == "" {
		model = e.Default()
	}
	payload, err := anthropicPayload(model, req)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		result, started, err := e.anthropicOnce(ctx, a, model, data, onDelta)
		if err == nil {
			e.trackQuota(a, nil)
			result.Account = a.Label()
			return result, nil
		}
		e.trackQuota(a, err)
		lastErr = err
		if started || ctx.Err() != nil {
			return nil, err
		}
		if !backend.IsQuotaError(err) && !backend.IsAuthError(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s: no accounts", e.Name)
	}
	return nil, lastErr
}

func anthropicPayload(model string, req *backend.ChatRequest) (map[string]any, error) {
	var system []string
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if m.Content != "" {
				system = append(system, m.Content)
			}
			continue
		case "tool", "function":
			id := m.ToolCallID
			if id == "" {
				id = backend.NewID("toolu_")
			}
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": id,
					"content":     m.Content,
				}},
			})
			continue
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		blocks := make([]map[string]any, 0, 1+len(m.ToolCalls))
		if m.Content != "" || len(m.Images) == 0 {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
		}
		for _, img := range m.Images {
			if img.URL != "" {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": img.URL}})
				continue
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": img.MIMEType, "data": img.Data,
			}})
		}
		if len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Arguments)
				if len(input) == 0 || !json.Valid(input) {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input,
				})
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}

	// The Messages API requires max_tokens, and requires it above the token
	// count the model will emit.
	maxTokens := 4096
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	payload := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   messages,
		"stream":     true,
	}
	if len(system) > 0 {
		payload["system"] = strings.Join(system, "\n\n")
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tool := map[string]any{"name": t.Name, "input_schema": schema}
			if t.Description != "" {
				tool["description"] = t.Description
			}
			tools = append(tools, tool)
		}
		payload["tools"] = tools
	}
	return payload, nil
}

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
		InputTokens  int `json:"input_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *Engine) anthropicOnce(ctx context.Context, a *auth.Auth, model string, data []byte, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.anthropicURL("/messages"), bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	anthropicHeaders(req, a)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, false, anthropicStatusError(e.Name, resp.StatusCode, body)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		return decodeAnthropicMessage(resp.Body, onDelta)
	}

	var (
		text     strings.Builder
		usage    backend.Usage
		finish   string
		started  bool
		calls    []*backend.ToolCall
		partials = map[int]*strings.Builder{}
		names    = map[int]string{}
		ids      = map[int]string{}
	)
	reader := bufio.NewReaderSize(resp.Body, 32*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if payload, ok := bytes.CutPrefix(trimmed, []byte("data:")); ok {
			payload = bytes.TrimSpace(payload)
			var ev anthropicStreamEvent
			if len(payload) > 0 && json.Unmarshal(payload, &ev) == nil {
				if ev.Error != nil && ev.Error.Message != "" {
					return nil, started, fmt.Errorf("%s: %s", e.Name, ev.Error.Message)
				}
				switch ev.Type {
				case "message_start":
					if ev.Message != nil && ev.Message.Usage != nil {
						usage.InputTokens = ev.Message.Usage.InputTokens
					}
				case "content_block_start":
					if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
						ids[ev.Index] = ev.ContentBlock.ID
						names[ev.Index] = ev.ContentBlock.Name
						if partials[ev.Index] == nil {
							partials[ev.Index] = &strings.Builder{}
						}
					}
				case "content_block_delta":
					if ev.Delta == nil {
						break
					}
					switch ev.Delta.Type {
					case "text_delta":
						if ev.Delta.Text != "" {
							text.WriteString(ev.Delta.Text)
							started = true
							if onDelta != nil {
								onDelta(backend.ChatDelta{Content: ev.Delta.Text})
							}
						}
					case "thinking_delta":
						if ev.Delta.Thinking != "" && onDelta != nil {
							onDelta(backend.ChatDelta{Thinking: ev.Delta.Thinking})
						}
					case "input_json_delta":
						if b := partials[ev.Index]; b != nil {
							b.WriteString(ev.Delta.PartialJSON)
							started = true
						}
					}
				case "message_delta":
					if ev.Delta != nil && ev.Delta.StopReason != "" {
						finish = ev.Delta.StopReason
					}
					if ev.Usage != nil {
						usage.OutputTokens = ev.Usage.OutputTokens
						if ev.Usage.InputTokens > 0 {
							usage.InputTokens = ev.Usage.InputTokens
						}
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return nil, started, ctx.Err()
	}
	// Tool calls carry their arguments as accumulated JSON fragments, so they
	// are emitted as one delta each once the stream closes.
	for index, name := range names {
		args := "{}"
		if b := partials[index]; b != nil && b.Len() > 0 {
			args = b.String()
		}
		id := ids[index]
		if id == "" {
			id = backend.NewID("toolu_")
		}
		calls = append(calls, &backend.ToolCall{ID: id, Type: "function", Name: name, Arguments: args})
		started = true
		if onDelta != nil {
			onDelta(backend.ChatDelta{ToolCalls: []backend.ToolCall{{ID: id, Type: "function", Name: name, Arguments: args}}})
		}
	}
	toolCalls := make([]backend.ToolCall, 0, len(calls))
	for _, c := range calls {
		toolCalls = append(toolCalls, *c)
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return &backend.ChatResult{
		Content:      text.String(),
		ToolCalls:    toolCalls,
		Usage:        usage,
		FinishReason: anthropicFinish(finish),
	}, started, nil
}

// decodeAnthropicMessage handles a provider that answers with one JSON body
// instead of an event stream.
func decodeAnthropicMessage(body io.Reader, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	var full struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
			Thinking string          `json:"thinking"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&full); err != nil {
		return nil, false, err
	}
	if full.Error != nil && full.Error.Message != "" {
		return nil, false, fmt.Errorf("%s", full.Error.Message)
	}
	var (
		text  strings.Builder
		calls []backend.ToolCall
	)
	for _, blk := range full.Content {
		switch blk.Type {
		case "text":
			text.WriteString(blk.Text)
			if blk.Text != "" && onDelta != nil {
				onDelta(backend.ChatDelta{Content: blk.Text})
			}
		case "thinking":
			if blk.Thinking != "" && onDelta != nil {
				onDelta(backend.ChatDelta{Thinking: blk.Thinking})
			}
		case "tool_use":
			args := "{}"
			if len(blk.Input) > 0 {
				args = string(blk.Input)
			}
			id := blk.ID
			if id == "" {
				id = backend.NewID("toolu_")
			}
			calls = append(calls, backend.ToolCall{ID: id, Type: "function", Name: blk.Name, Arguments: args})
			if onDelta != nil {
				onDelta(backend.ChatDelta{ToolCalls: []backend.ToolCall{calls[len(calls)-1]}})
			}
		}
	}
	var usage backend.Usage
	if full.Usage != nil {
		usage = backend.Usage{
			InputTokens:  full.Usage.InputTokens,
			OutputTokens: full.Usage.OutputTokens,
			TotalTokens:  full.Usage.InputTokens + full.Usage.OutputTokens,
		}
	}
	started := text.Len() > 0 || len(calls) > 0
	return &backend.ChatResult{
		Content:      text.String(),
		ToolCalls:    calls,
		Usage:        usage,
		FinishReason: anthropicFinish(full.StopReason),
	}, started, nil
}

func anthropicFinish(reason string) string {
	switch reason {
	case "", "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	}
	return reason
}

// Quota reports per-account availability so a custom provider appears in the
// dashboard directory with the same shape as a built-in one.
func (e *Engine) Quota() auth.ProviderQuota {
	auths := e.snapshot()
	e.quotaMu.Lock()
	limited := make(map[*auth.Auth]time.Time, len(e.quotaSeen))
	for a, at := range e.quotaSeen {
		limited[a] = at
	}
	e.quotaMu.Unlock()
	q := auth.ProviderQuota{
		Provider:     e.Name,
		BaseURL:      e.BaseURL,
		SDK:          e.sdkName(),
		DefaultModel: e.Default(),
		Accounts:     make([]auth.AccountQuota, 0, len(auths)),
	}
	for _, a := range auths {
		q.Accounts = append(q.Accounts, auth.AccountStatus(e.Name, a, limited))
	}
	return q
}

func (e *Engine) sdkName() string {
	if e.anthropic() {
		return "anthropic"
	}
	return "openai"
}

// trackQuota remembers which accounts recently hit a quota failure, mirroring
// the built-in providers' quota reporting.
func (e *Engine) trackQuota(a *auth.Auth, err error) {
	if a == nil {
		return
	}
	e.quotaMu.Lock()
	defer e.quotaMu.Unlock()
	if e.quotaSeen == nil {
		e.quotaSeen = map[*auth.Auth]time.Time{}
	}
	if err != nil && backend.IsQuotaError(err) {
		e.quotaSeen[a] = time.Now()
		return
	}
	delete(e.quotaSeen, a)
}
