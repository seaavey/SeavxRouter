package verboo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

const verbooBaseURL = "https://code.verboo.ai/router/v1"

type Engine struct {
	Store        auth.AuthStore
	HTTP         *http.Client
	DefaultModel string
	BaseURL      string

	Stdin io.Reader
	Out   io.Writer

	mu       sync.Mutex
	auths    []*auth.Auth
	cache    auth.AccountCache
	next     int
	models   []backend.Model
	modelsAt time.Time

	quotaMu  sync.Mutex
	quotaErr map[*auth.Auth]time.Time
}

var _ backend.BackendAPI = (*Engine)(nil)

func (e *Engine) apiBase() string {
	if e.BaseURL != "" {
		return strings.TrimRight(e.BaseURL, "/")
	}
	return verbooBaseURL
}

func (e *Engine) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

func NewEngine(store auth.AuthStore, auths []*auth.Auth) (*Engine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no verboo accounts")
	}
	return &Engine{Store: store, HTTP: &http.Client{}, auths: auths, cache: auth.NewAccountCache(store, "verboo")}, nil
}

func (e *Engine) Default() string { return e.DefaultModel }

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

func (e *Engine) snapshotAuths() []*auth.Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	return append([]*auth.Auth(nil), e.auths...)
}

func (e *Engine) trackQuota(a *auth.Auth, err error) {
	e.quotaMu.Lock()
	defer e.quotaMu.Unlock()
	if err == nil {
		delete(e.quotaErr, a)
		return
	}
	if !backend.IsQuotaError(err) {
		return
	}
	if e.quotaErr == nil {
		e.quotaErr = map[*auth.Auth]time.Time{}
	}
	e.quotaErr[a] = time.Now()
}

type modelList struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

// Models discovers the account's catalog from GET /models. The list is
// account-specific, so there is no static fallback.
func (e *Engine) Models(ctx context.Context) ([]backend.Model, error) {
	e.mu.Lock()
	if e.models != nil && time.Since(e.modelsAt) < 5*time.Minute {
		models := e.models
		e.mu.Unlock()
		return models, nil
	}
	e.mu.Unlock()

	models, err := e.fetchModels(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.models = models
	e.modelsAt = time.Now()
	e.mu.Unlock()
	return models, nil
}

func (e *Engine) fetchModels(ctx context.Context) ([]backend.Model, error) {
	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		models, err := e.fetchModelsWith(ctx, e.pick())
		if err == nil {
			return models, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no verboo accounts")
	}
	return nil, lastErr
}

func (e *Engine) fetchModelsWith(ctx context.Context, a *auth.Auth) ([]backend.Model, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", e.apiBase()+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, backend.APIHTTPError(resp)
	}
	var list modelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	out := make([]backend.Model, 0, len(list.Data))
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
	if len(out) == 0 {
		return nil, fmt.Errorf("verboo models: empty catalog")
	}
	return out, nil
}

// Check verifies each account against upstream.
func (e *Engine) Check(ctx context.Context) []backend.AccountCheck {
	auths := e.snapshotAuths()
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

func (e *Engine) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	model := req.Model
	if model == "" {
		model = e.Default()
	}
	payload := map[string]any{
		"model":          model,
		"messages":       verbooMessages(req.Messages),
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if tools := verbooTools(req.Tools); tools != nil {
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
		result, started, err := e.chatStreamOnce(ctx, a, data, onDelta)
		e.trackQuota(a, err)
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
		slog.Warn("verboo account failed, trying next", "err", err)
	}
	return nil, lastErr
}

// verbooMessages converts unified messages to the OpenAI wire format the
// Verboo router accepts. Reasoning content from prior turns is dropped; only
// the visible assistant text and tool calls are replayed.
func verbooMessages(msgs []backend.ChatMessage) []map[string]any {
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

func verbooTools(tools []backend.ToolDefinition) []map[string]any {
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

type verbooUsage struct {
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

type verbooChunk struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage   *verbooUsage `json:"usage"`
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

// chatStreamOnce sends one upstream request. The router always answers with an
// OpenAI-style SSE stream, so every response is parsed incrementally here.
func (e *Engine) chatStreamOnce(ctx context.Context, a *auth.Auth, data []byte, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", e.apiBase()+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := backend.APIHTTPError(resp)
		// 402 (payment) and 429 (rate limit) are per-account conditions worth
		// rotating on; 401/403 mark a key that cannot serve requests.
		switch resp.StatusCode {
		case http.StatusPaymentRequired, http.StatusTooManyRequests:
			return nil, false, fmt.Errorf("quota: %w", err)
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, false, fmt.Errorf("unauthorized: %w", err)
		}
		return nil, false, err
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
				var chunk verbooChunk
				if json.Unmarshal(payload, &chunk) == nil {
					if chunk.Error != nil && chunk.Error.Message != "" {
						return nil, started, fmt.Errorf("verboo: %s", chunk.Error.Message)
					}
					for _, c := range chunk.Choices {
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
					if chunk.Usage != nil {
						u := chunk.Usage
						usage = backend.Usage{
							InputTokens:     u.PromptTokens,
							OutputTokens:    u.CompletionTokens,
							ThinkingTokens:  u.CompletionTokensDetails.ReasoningTokens,
							CacheReadTokens: u.PromptTokensDetails.CachedTokens,
							TotalTokens:     u.TotalTokens,
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
		started = true
		if onDelta != nil {
			onDelta(backend.ChatDelta{ToolCalls: []backend.ToolCall{tc}})
		}
	}
	reason := mapFinish(finish)
	if len(toolCalls) > 0 {
		reason = "tool_calls"
	}
	return &backend.ChatResult{Content: full.String(), ToolCalls: toolCalls, Usage: usage, FinishReason: reason}, started, nil
}

// Login validates an API key from VERBOO_API_KEY or stdin, then stores it.
func (e *Engine) Login(ctx context.Context) error {
	in := e.Stdin
	if in == nil {
		in = os.Stdin
	}
	out := e.Out
	if out == nil {
		out = os.Stdout
	}
	key := strings.TrimSpace(os.Getenv("VERBOO_API_KEY"))
	if key == "" {
		fmt.Fprint(out, "Verboo API key (https://code.verboo.ai/en/api-keys): ")
		line, err := bufio.NewReader(in).ReadString('\n')
		key = strings.TrimSpace(line)
		if key == "" && err != nil {
			return err
		}
	}
	if key == "" {
		return fmt.Errorf("no Verboo API key provided")
	}
	if !strings.HasPrefix(key, "vbk_") {
		fmt.Fprintln(out, `note: Verboo API keys usually start with "vbk_"`)
	}
	models, err := e.fetchModelsWith(ctx, &auth.Auth{AccessToken: key})
	if err != nil {
		return fmt.Errorf("verboo login failed: %w", err)
	}

	acct := &auth.Auth{Provider: "verboo", AccessToken: key}
	existing, _ := e.Store.Load()
	own := make([]*auth.Auth, 0, len(existing)+1)
	for _, a := range existing {
		if a.Provider == "verboo" {
			own = append(own, a)
		}
	}
	merged := false
	for i, a := range own {
		if a.AccessToken == key {
			own[i] = acct
			merged = true
			break
		}
	}
	if !merged {
		own = append(own, acct)
	}
	e.mu.Lock()
	e.auths = own
	e.mu.Unlock()
	if err := e.Store.Save(own); err != nil {
		return err
	}
	fmt.Fprintf(out, "Verboo key accepted: %d model(s) available\n%d key(s) stored in %s\n", len(models), len(own), e.Store.Path)
	return nil
}
