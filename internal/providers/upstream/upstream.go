// Package upstream implements the backend contract for any provider that
// serves an OpenAI-compatible /chat/completions and /models endpoint behind a
// bearer API key: NVIDIA NIM, Groq, Cloudflare Workers AI and friends.
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// Engine talks to one OpenAI-compatible upstream. Multiple keys are rotated in
// round-robin order; quota and auth failures move to the next key.
type Engine struct {
	// Name is the provider label used for logging and credential grouping.
	Name string
	// BaseURL is the API root without a trailing slash, e.g.
	// "https://api.groq.com/openai/v1".
	BaseURL string
	// AccountPath, when set, is a provider-specific endpoint that returns the
	// signed-in account (Cloudflare answers /user instead of /models).
	AccountPath string
	// ModelsPath overrides the /models path for providers whose model catalog
	// lives elsewhere (Cloudflare keeps it under accounts/{id}/ai/models).
	ModelsPath string
	// ChatPath overrides the /chat/completions path, mirroring ModelsPath.
	ChatPath string
	// DefaultModel is used when a request omits the model.
	DefaultModel string
	Store        auth.AuthStore
	HTTP         *http.Client

	mu       sync.Mutex
	auths    []*auth.Auth
	cache    auth.AccountCache
	next     int
	models   []backend.Model
	modelsAt time.Time
}

var _ backend.BackendAPI = (*Engine)(nil)

func (e *Engine) Default() string { return e.DefaultModel }

func (e *Engine) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

// endpoint builds an absolute URL. When tmpl carries a %s it is filled with the
// account's upstream account ID (Cloudflare's account-scoped paths); otherwise
// fallback is appended to the base URL.
func (e *Engine) endpoint(tmpl, fallback string, a *auth.Auth) string {
	if tmpl != "" {
		if strings.Contains(tmpl, "%s") {
			return e.BaseURL + "/" + fmt.Sprintf(tmpl, a.AccountID)
		}
		return e.BaseURL + "/" + tmpl
	}
	return e.BaseURL + fallback
}

// NewEngine builds an engine for one provider from stored bearer keys. The
// keys carry no refresh token, so the cache refreshes them from disk only.
func NewEngine(name, baseURL string, store auth.AuthStore, auths []*auth.Auth) (*Engine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no %s accounts", name)
	}
	return &Engine{
		Name:    name,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Store:   store,
		HTTP:    &http.Client{},
		auths:   auths,
		cache:   auth.NewAccountCache(store, name),
	}, nil
}

func (e *Engine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	return len(e.auths)
}

func (e *Engine) pick() *auth.Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	a := e.auths[e.next%len(e.auths)]
	e.next++
	return a
}

func (e *Engine) snapshot() []*auth.Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	return append([]*auth.Auth(nil), e.auths...)
}

type modelList struct {
	Data []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// Models discovers the catalog from GET /models. The list is account-specific,
// so there is no static fallback.
func (e *Engine) Models(ctx context.Context) ([]backend.Model, error) {
	e.mu.Lock()
	if e.models != nil && time.Since(e.modelsAt) < 5*time.Minute {
		models := e.models
		e.mu.Unlock()
		return models, nil
	}
	e.mu.Unlock()

	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		models, err := e.fetchModelsWith(ctx, e.pick())
		if err == nil {
			e.mu.Lock()
			e.models = models
			e.modelsAt = time.Now()
			e.mu.Unlock()
			return models, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s: no accounts", e.Name)
	}
	return nil, lastErr
}

func (e *Engine) fetchModelsWith(ctx context.Context, a *auth.Auth) ([]backend.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.endpoint(e.ModelsPath, "/models", a), nil)
	if err != nil {
		return nil, err
	}
	e.authorize(req, a)
	req.Header.Set("Accept", "application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, backend.APIHTTPError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Most providers wrap the catalog in {"data":[...]}; Cloudflare answers
	// {"result":[...]} instead, so both shapes are decoded here.
	var list modelList
	if err := json.Unmarshal(body, &list); err != nil || len(list.Data) == 0 {
		var cf struct {
			Result []struct {
				Name string `json:"name"`
			} `json:"result"`
		}
		if cfErr := json.Unmarshal(body, &cf); cfErr == nil {
			for _, m := range cf.Result {
				list.Data = append(list.Data, struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					OwnedBy string `json:"owned_by"`
				}{ID: m.Name, Name: m.Name})
			}
		}
	}
	out := make([]backend.Model, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, backend.Model{ID: m.ID, Name: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s models: empty catalog", e.Name)
	}
	return out, nil
}

func (e *Engine) authorize(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	if a.ProjectID != "" {
		// Grouped keys: NVIDIA ignores it, but sending the extra header keeps
		// providers that scope requests by project working unchanged.
		req.Header.Set("OpenAI-Organization", a.ProjectID)
	}
}

// Check verifies each account against upstream.
func (e *Engine) Check(ctx context.Context) []backend.AccountCheck {
	auths := e.snapshot()
	out := make([]backend.AccountCheck, 0, len(auths))
	for _, a := range auths {
		start := time.Now()
		models, err := e.fetchModelsWith(ctx, a)
		out = append(out, backend.AccountCheck{
			Account:   a.Email,
			AccountID: a.AccountID,
			Models:    models,
			Err:       err,
			Latency:   time.Since(start),
		})
	}
	return out
}

func (e *Engine) HasModel(ctx context.Context, id string) bool {
	models, err := e.Models(ctx)
	if err != nil {
		return true
	}
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (e *Engine) Complete(ctx context.Context, model, prompt string) (*backend.Result, error) {
	res, err := e.Chat(ctx, &backend.ChatRequest{Model: model, Messages: []backend.ChatMessage{{Role: "user", Content: prompt}}}, nil)
	if err != nil {
		return nil, err
	}
	return &backend.Result{Response: res.Content, Usage: res.Usage, Status: "SUCCESS"}, nil
}

func (e *Engine) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
	var full strings.Builder
	res, err := e.Chat(ctx, &backend.ChatRequest{Model: model, Messages: []backend.ChatMessage{{Role: "user", Content: prompt}}, Stream: true}, func(d backend.ChatDelta) {
		if d.Content == "" {
			return
		}
		full.WriteString(d.Content)
		if onDelta != nil {
			onDelta(d.Content)
		}
	})
	if err != nil {
		return nil, err
	}
	return &backend.Result{Response: full.String(), Usage: res.Usage, Status: "SUCCESS"}, nil
}

// Chat always asks upstream for an SSE stream and folds it into one ChatResult,
// so streaming and non-streaming callers share a single wire path.
func (e *Engine) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	model := req.Model
	if model == "" {
		model = e.Default()
	}
	payload := map[string]any{
		"model":          model,
		"messages":       wireMessages(req.Messages),
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if tools := wireTools(req.Tools); tools != nil {
		payload["tools"] = tools
	}
	if req.ToolChoice != nil {
		payload["tool_choice"] = req.ToolChoice
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	}
	if req.ServiceTier != "" {
		payload["service_tier"] = req.ServiceTier
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		result, started, err := e.chatOnce(ctx, a, data, onDelta)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || ctx.Err() != nil {
			return nil, err
		}
		if !backend.IsQuotaError(err) && !backend.IsAuthError(err) {
			return nil, err
		}
		slog.Warn("account failed, trying next", "provider", e.Name, "err", err)
	}
	return nil, lastErr
}

// wireMessages converts unified messages to the OpenAI wire format.
func wireMessages(msgs []backend.ChatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			out = append(out, map[string]any{"role": m.Role, "content": m.Content})
		case "assistant":
			msg := map[string]any{"role": "assistant", "content": m.Content}
			if len(m.ToolCalls) > 0 {
				tcs := make([]map[string]any, 0, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					id := tc.ID
					if id == "" {
						id = fmt.Sprintf("tool_%d_%s", i, backend.NewID(""))
					}
					args := tc.Arguments
					if strings.TrimSpace(args) == "" {
						args = "{}"
					}
					tcs = append(tcs, map[string]any{
						"id": id, "type": "function",
						"function": map[string]any{"name": tc.Name, "arguments": args},
					})
				}
				msg["tool_calls"] = tcs
			}
			out = append(out, msg)
		case "tool":
			id := m.ToolCallID
			if id == "" {
				id = backend.NewID("tool_")
			}
			msg := map[string]any{"role": "tool", "tool_call_id": id, "content": m.Content}
			if m.Name != "" {
				msg["name"] = m.Name
			}
			out = append(out, msg)
		default:
			if len(m.Images) == 0 {
				out = append(out, map[string]any{"role": "user", "content": m.Content})
				continue
			}
			parts := make([]map[string]any, 0, 1+len(m.Images))
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": img.DataURL()}})
			}
			out = append(out, map[string]any{"role": "user", "content": parts})
		}
	}
	return out
}

func wireTools(tools []backend.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		fn := map[string]any{"name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if t.Parameters != nil {
			fn["parameters"] = t.Parameters
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

type wireUsage struct {
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

type chunk struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage   *wireUsage `json:"usage"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
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
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func mapFinish(reason string) string {
	switch reason {
	case "", "stop":
		return "stop"
	case "length":
		return "length"
	case "tool_calls", "function_call":
		return "tool_calls"
	}
	return reason
}

// chatOnce sends one upstream request and folds the SSE stream into a result.
func (e *Engine) chatOnce(ctx context.Context, a *auth.Auth, data []byte, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint(e.ChatPath, "/chat/completions", a), bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	e.authorize(req, a)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		httpErr := backend.APIHTTPError(resp)
		switch resp.StatusCode {
		case http.StatusPaymentRequired, http.StatusTooManyRequests:
			return nil, false, fmt.Errorf("quota: %w", httpErr)
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, false, fmt.Errorf("unauthorized: %w", httpErr)
		}
		return nil, false, httpErr
	}

	// A provider that answers with plain JSON instead of SSE (some Groq/NIM
	// routes do) is decoded whole rather than streamed.
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		return decodeJSONResponse(resp.Body, onDelta)
	}

	var (
		full    strings.Builder
		usage   backend.Usage
		finish  string
		started bool
		all     []*backend.ToolCall
		cur     = map[int]*backend.ToolCall{}
	)
	reader := bufio.NewReaderSize(resp.Body, 1024*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if payload, ok := bytes.CutPrefix(trimmed, []byte("data:")); ok {
			payload = bytes.TrimSpace(payload)
			if string(payload) != "[DONE]" {
				var ch chunk
				if json.Unmarshal(payload, &ch) == nil {
					if ch.Error != nil && ch.Error.Message != "" {
						return nil, started, fmt.Errorf("%s: %s", e.Name, ch.Error.Message)
					}
					for _, c := range ch.Choices {
						if c.Delta.Content != "" {
							full.WriteString(c.Delta.Content)
							started = true
							if onDelta != nil {
								onDelta(backend.ChatDelta{Content: c.Delta.Content})
							}
						}
						for _, tc := range c.Delta.ToolCalls {
							acc := cur[tc.Index]
							// Some upstreams reuse index 0 for parallel calls; a
							// fresh non-empty ID starts a new accumulator.
							if acc == nil || (tc.ID != "" && acc.ID != "" && tc.ID != acc.ID) {
								acc = &backend.ToolCall{Type: "function"}
								cur[tc.Index] = acc
								all = append(all, acc)
							}
							if tc.ID != "" {
								acc.ID = tc.ID
							}
							if tc.Type != "" {
								acc.Type = tc.Type
							}
							if tc.Function.Name != "" {
								acc.Name = tc.Function.Name
							}
							acc.Arguments += tc.Function.Arguments
						}
						if c.FinishReason != "" {
							finish = c.FinishReason
						}
					}
					if ch.Usage != nil {
						usage = toUsage(ch.Usage)
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
	return finishResult(&full, all, usage, finish, started, onDelta), started, nil
}

// decodeJSONResponse handles the non-streaming wire form.
func decodeJSONResponse(body io.Reader, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	var full struct {
		Error   *struct{ Message string } `json:"error"`
		Usage   *wireUsage                `json:"usage"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
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
	}
	if err := json.NewDecoder(body).Decode(&full); err != nil {
		return nil, false, err
	}
	if full.Error != nil && full.Error.Message != "" {
		return nil, false, fmt.Errorf("%s", full.Error.Message)
	}
	var (
		text   strings.Builder
		calls  []*backend.ToolCall
		finish string
		usage  backend.Usage
	)
	if full.Usage != nil {
		usage = toUsage(full.Usage)
	}
	for _, c := range full.Choices {
		if c.Message.Content != "" {
			text.WriteString(c.Message.Content)
			if onDelta != nil {
				onDelta(backend.ChatDelta{Content: c.Message.Content})
			}
		}
		for _, tc := range c.Message.ToolCalls {
			calls = append(calls, &backend.ToolCall{ID: tc.ID, Type: tc.Type, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
	}
	started := text.Len() > 0 || len(calls) > 0
	return finishResult(&text, calls, usage, finish, started, onDelta), started, nil
}

func toUsage(u *wireUsage) backend.Usage {
	return backend.Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		ThinkingTokens:  u.CompletionTokensDetails.ReasoningTokens,
		CacheReadTokens: u.PromptTokensDetails.CachedTokens,
		TotalTokens:     u.TotalTokens,
	}
}

func finishResult(text *strings.Builder, all []*backend.ToolCall, usage backend.Usage, finish string, started bool, onDelta func(backend.ChatDelta)) *backend.ChatResult {
	toolCalls := make([]backend.ToolCall, 0, len(all))
	for _, acc := range all {
		tc := *acc
		if tc.ID == "" {
			tc.ID = backend.NewID("call_")
		}
		if tc.Arguments == "" {
			tc.Arguments = "{}"
		}
		toolCalls = append(toolCalls, tc)
		if onDelta != nil {
			onDelta(backend.ChatDelta{ToolCalls: []backend.ToolCall{tc}})
		}
	}
	reason := mapFinish(finish)
	if len(toolCalls) > 0 {
		reason = "tool_calls"
	}
	if !started && text.Len() == 0 && len(toolCalls) == 0 {
		reason = mapFinish(finish)
	}
	return &backend.ChatResult{Content: text.String(), ToolCalls: toolCalls, Usage: usage, FinishReason: reason}
}
