package codebuddy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

const (
	codebuddyBaseURL = "https://www.codebuddy.ai"
	codebuddyUA      = "CLI/1.0.7 CodeBuddy/1.0.7"
)

// codebuddyCatalog is the fallback model list used when upstream discovery via
// GET /v3/config is unavailable.
var codebuddyCatalog = []string{
	"default-model",
	"fast-model",
	"balanced-model",
	"primary-model",
	"hy4-preview",
	"hy3",
	"deepseek-v4.1-flash",
	"deepseek-v4.1-flash-sg",
	"gpt-6-astra",
	"gpt-6.1-sol",
	"gpt-6-sol",
	"gpt-6-luna",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"grok-4.7",
	"gemini-3.8-flash",
	"glm-5.3-flash",
	"glm-5.3",
	"glm-5.2",
	"minimax-m3.1-flash",
	"kimi-k3.1-pro",
	"kimi-k3.1-pro-highspeed",
	"kimi-k3",
	"auto-chat",
}

var codebuddyAliases = map[string]string{
	"glm5.1":       "glm-5.1",
	"glm5.0":       "glm-5.0",
	"glm5.0-turbo": "glm-5.0-turbo",
	"glm5v-turbo":  "glm-5v-turbo",
}

type CodeBuddyEngine struct {
	Store        auth.AuthStore
	HTTP         *http.Client
	DefaultModel string
	BaseURL      string
	PollInterval time.Duration

	mu      sync.Mutex
	auths   []*auth.Auth
	cache   auth.AccountCache
	next    int
	catalog *backend.Catalog[[]backend.Model]

	quotaMu  sync.Mutex
	quotaErr map[*auth.Auth]time.Time
}

func (e *CodeBuddyEngine) apiBase() string {
	if e.BaseURL != "" {
		return strings.TrimRight(e.BaseURL, "/")
	}
	return codebuddyBaseURL
}

func NewCodeBuddyEngine(store auth.AuthStore, auths []*auth.Auth) (*CodeBuddyEngine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no codebuddy accounts")
	}
	e := &CodeBuddyEngine{Store: store, HTTP: backend.NewHTTPClient(), auths: auths, cache: auth.NewAccountCache(store, "codebuddy")}
	e.catalog = backend.NewCatalog("codebuddy", 5*time.Minute, e.fetchModels)
	return e, nil
}

func (e *CodeBuddyEngine) Default() string {
	if e.DefaultModel != "" {
		return e.DefaultModel
	}
	return "auto-chat"
}

func (e *CodeBuddyEngine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	return len(e.auths)
}

func (e *CodeBuddyEngine) pick() *auth.Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	a := e.auths[e.next%len(e.auths)]
	e.next++
	return a
}

func (e *CodeBuddyEngine) snapshotAuths() []*auth.Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	return append([]*auth.Auth(nil), e.auths...)
}

func (e *CodeBuddyEngine) trackQuota(a *auth.Auth, err error) {
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

func ModelID(model string) string {
	model = strings.TrimSpace(model)
	if alias, ok := codebuddyAliases[strings.ToLower(model)]; ok {
		return alias
	}
	return model
}

func codebuddyFallbackModels() []backend.Model {
	out := make([]backend.Model, 0, len(codebuddyCatalog))
	for _, id := range codebuddyCatalog {
		out = append(out, backend.Model{ID: id, Name: id})
	}
	return out
}

// Models discovers the account's catalog from GET /v3/config, falling back to
// the built-in list when upstream is unreachable.
func (e *CodeBuddyEngine) Models(ctx context.Context) ([]backend.Model, error) {
	models, err := e.catalog.Get(ctx)
	if err != nil || len(models) == 0 {
		if err != nil {
			slog.Warn("codebuddy model discovery failed; using built-in catalog", "err", err)
		}
		return codebuddyFallbackModels(), nil
	}
	return models, nil
}

// Check verifies each account against upstream.
func (e *CodeBuddyEngine) Check(ctx context.Context) []backend.AccountCheck {
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

func (e *CodeBuddyEngine) fetchModels(ctx context.Context) ([]backend.Model, error) {
	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		models, err := e.fetchModelsWith(ctx, a)
		if err == nil {
			return models, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no codebuddy accounts")
	}
	return nil, lastErr
}

func (e *CodeBuddyEngine) fetchModelsWith(ctx context.Context, a *auth.Auth) ([]backend.Model, error) {
	if e.HTTP == nil {
		return nil, fmt.Errorf("no http client")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", e.apiBase()+"/v3/config", nil)
	if err != nil {
		return nil, err
	}
	e.setModelHeaders(req, a)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, backend.APIHTTPError(resp)
	}
	var cfg codebuddyConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.Code != 0 {
		return nil, fmt.Errorf("codebuddy config: code %d %s", cfg.Code, strings.TrimSpace(cfg.Msg))
	}
	models := cfg.cliModels()
	if len(models) == 0 {
		return nil, fmt.Errorf("codebuddy config: no cli models")
	}
	return models, nil
}

func (e *CodeBuddyEngine) setModelHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Request-ID", backend.NewID(""))
	req.Header.Set("X-Domain", codebuddyDomain(e.apiBase()))
	req.Header.Set("User-Agent", codebuddyUA)
	req.Header.Set("X-Product", "SaaS")
	if a.AccountID != "" {
		req.Header.Set("X-User-Id", a.AccountID)
	}
}

type codebuddyConfig struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		Agents []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		} `json:"agents"`
		Models []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Disabled bool   `json:"disabled"`
		} `json:"models"`
	} `json:"data"`
}

// cliModels resolves the "cli" agent's model list against the catalog metadata,
// dropping disabled entries.
func (c *codebuddyConfig) cliModels() []backend.Model {
	if c.Data == nil {
		return nil
	}
	names := make(map[string]string, len(c.Data.Models))
	disabled := make(map[string]bool, len(c.Data.Models))
	for _, m := range c.Data.Models {
		names[m.ID] = m.Name
		disabled[m.ID] = m.Disabled
	}
	var ids []string
	for _, agent := range c.Data.Agents {
		if agent.Name == "cli" {
			ids = agent.Models
			break
		}
	}
	if len(ids) == 0 {
		for _, m := range c.Data.Models {
			ids = append(ids, m.ID)
		}
	}
	out := make([]backend.Model, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] || disabled[id] {
			continue
		}
		seen[id] = true
		name := names[id]
		if name == "" {
			name = id
		}
		out = append(out, backend.Model{ID: id, Name: name})
	}
	return out
}

func (e *CodeBuddyEngine) HasModel(ctx context.Context, id string) bool {
	id = ModelID(id)
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

func (e *CodeBuddyEngine) Complete(ctx context.Context, model, prompt string) (*backend.Result, error) {
	res, err := e.Chat(ctx, &backend.ChatRequest{Model: model, Messages: []backend.ChatMessage{{Role: "user", Content: prompt}}}, nil)
	if err != nil {
		return nil, err
	}
	return &backend.Result{Response: res.Content, Usage: res.Usage, Status: "SUCCESS"}, nil
}

func (e *CodeBuddyEngine) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
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

func (e *CodeBuddyEngine) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	model := req.Model
	if model == "" {
		model = e.Default()
	}
	payload := map[string]any{
		"model":    ModelID(model),
		"messages": codebuddyMessages(req.Messages),
		"stream":   true,
	}
	if tools := codebuddyTools(req.Tools); tools != nil {
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
			result.Account = a.Label()
			return result, nil
		}
		lastErr = err
		if started || ctx.Err() != nil {
			return nil, err
		}
		if !backend.IsQuotaError(err) && !backend.IsAuthError(err) {
			return nil, err
		}
		slog.Warn("codebuddy account failed, trying next", "email", a.Email, "err", err)
	}
	return nil, lastErr
}

// codebuddyMessages converts unified messages to the OpenAI wire format the
// /v2/chat/completions gateway accepts. Structured Anthropic-style tool_use
// blocks are rejected with "unsupported content type", so tool history is
// sent as assistant tool_calls plus role "tool" result messages.
func codebuddyMessages(msgs []backend.ChatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			out = append(out, map[string]any{"role": "system", "content": m.Content})
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
	// CodeBuddy rejects any conversation not starting with a system prompt and
	// requires at least two messages, so synthesize a leading system turn.
	if len(out) > 0 && out[0]["role"] != "system" {
		out = append([]map[string]any{{"role": "system", "content": "You are a helpful assistant."}}, out...)
	}
	return out
}

func codebuddyTools(tools []backend.ToolDefinition) []map[string]any {
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

// toolCallAcc accumulates streamed tool-call fragments without quadratic
// string concatenation.
type toolCallAcc struct {
	*backend.ToolCall
	args strings.Builder
}

type codebuddyChunk struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
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

func mapCodebuddyFinish(reason string) string {
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

// chatStreamOnce sends one upstream request. CodeBuddy always answers with an
// OpenAI-style SSE stream even for non-streaming clients, so every response is
// parsed incrementally and aggregated here.
func (e *CodeBuddyEngine) chatStreamOnce(ctx context.Context, a *auth.Auth, data []byte, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", e.apiBase()+"/v2/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	e.setHeaders(req, a)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, backend.APIHTTPError(resp)
	}
	var (
		full    strings.Builder
		usage   backend.Usage
		finish  string
		started bool
		all     []*toolCallAcc
		cur     = map[int]*toolCallAcc{}
	)
	reader := backend.AcquireSSEReader(resp.Body)
	defer backend.ReleaseSSEReader(reader)
	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if payload, ok := bytes.CutPrefix(trimmed, []byte("data:")); ok {
			payload = bytes.TrimSpace(payload)
			if !bytes.Equal(payload, []byte("[DONE]")) {
				var chunk codebuddyChunk
				if json.Unmarshal(payload, &chunk) == nil {
					if chunk.Error != nil && chunk.Error.Message != "" {
						return nil, started, fmt.Errorf("codebuddy: %s", chunk.Error.Message)
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
							// CodeBuddy reuses index 0 for parallel calls; a fresh
							// non-empty ID starts a new accumulator.
							if acc == nil || (tc.ID != "" && acc.ID != "" && tc.ID != acc.ID) {
								acc = &toolCallAcc{ToolCall: &backend.ToolCall{Type: "function"}}
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
							acc.args.WriteString(tc.Function.Arguments)
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
		tc := *acc.ToolCall
		tc.Arguments = acc.args.String()
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
	reason := mapCodebuddyFinish(finish)
	if len(toolCalls) > 0 {
		reason = "tool_calls"
	}
	return &backend.ChatResult{Content: full.String(), ToolCalls: toolCalls, Usage: usage, FinishReason: reason}, started, nil
}

func (e *CodeBuddyEngine) setHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Conversation-ID", backend.UUID())
	req.Header.Set("X-Conversation-Request-ID", backend.NewID(""))
	req.Header.Set("X-Conversation-Message-ID", backend.NewID(""))
	req.Header.Set("X-Request-ID", backend.NewID(""))
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-IDE-Type", "CLI")
	req.Header.Set("X-IDE-Name", "CLI")
	req.Header.Set("X-IDE-Version", "1.0.7")
	req.Header.Set("X-Domain", codebuddyDomain(e.apiBase()))
	req.Header.Set("User-Agent", codebuddyUA)
	req.Header.Set("X-Product", "SaaS")
	userID := a.AccountID
	if userID == "" {
		userID = "anonymous"
	}
	req.Header.Set("X-User-Id", userID)
}

func codebuddyDomain(base string) string {
	rest, ok := strings.CutPrefix(base, "https://")
	if !ok {
		rest, ok = strings.CutPrefix(base, "http://")
	}
	if !ok {
		return "www.codebuddy.ai"
	}
	if i := strings.IndexAny(rest, "/:"); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return "www.codebuddy.ai"
	}
	return rest
}

type codebuddyAuthState struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	} `json:"data"`
}

type codebuddyTokenData struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		AccessToken  string `json:"accessToken"`
		TokenType    string `json:"tokenType"`
		ExpiresIn    int    `json:"expiresIn"`
		RefreshToken string `json:"refreshToken"`
		Scope        string `json:"scope"`
		Domain       string `json:"domain"`
	} `json:"data"`
}

// codebuddyClaims decodes the payload of a JWT-encoded access token.
func codebuddyClaims(token string) (email, subject string, expiry time.Time) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", "", time.Time{}
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", time.Time{}
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Sub               string `json:"sub"`
		Exp               int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return "", "", time.Time{}
	}
	email = claims.Email
	if email == "" {
		email = claims.PreferredUsername
	}
	if claims.Exp > 0 {
		expiry = time.Unix(claims.Exp, 0)
	}
	return email, claims.Sub, expiry
}

// Login starts the CodeBuddy CLI device flow: fetch a state/auth URL pair,
// then poll the token endpoint until the browser sign-in completes.
func (e *CodeBuddyEngine) Login(ctx context.Context) error {
	nonce := backend.NewID("")
	stateURL := e.apiBase() + "/v2/plugin/auth/state?platform=CLI&nonce=" + nonce
	body, _ := json.Marshal(map[string]string{"nonce": nonce})
	req, err := http.NewRequestWithContext(ctx, "POST", stateURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	codebuddyAuthHeaders(req, false)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return err
	}
	var state codebuddyAuthState
	err = json.NewDecoder(resp.Body).Decode(&state)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || state.Code != 0 || state.Data == nil || state.Data.State == "" || state.Data.AuthURL == "" {
		return fmt.Errorf("codebuddy auth state: %s (code %d)", strings.TrimSpace(state.Msg), state.Code)
	}

	fmt.Println("Open this URL to sign in to CodeBuddy:\n\n" + state.Data.AuthURL + "\n")
	auth.OpenBrowser(state.Data.AuthURL)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	interval := e.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	tokenURL := e.apiBase() + "/v2/plugin/auth/token?state=" + state.Data.State
	var tok codebuddyTokenData
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", tokenURL, nil)
		if err != nil {
			return err
		}
		codebuddyAuthHeaders(req, true)
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return err
		}
		json.NewDecoder(resp.Body).Decode(&tok)
		resp.Body.Close()
		if tok.Code == 0 && tok.Data != nil && tok.Data.AccessToken != "" {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("login timed out")
		case <-time.After(interval):
		}
	}

	acct := &auth.Auth{Provider: "codebuddy", AccessToken: tok.Data.AccessToken, RefreshToken: tok.Data.RefreshToken}
	acct.Email, acct.AccountID, acct.Expiry = codebuddyClaims(tok.Data.AccessToken)
	if acct.Expiry.IsZero() && tok.Data.ExpiresIn > 0 {
		acct.Expiry = time.Now().Add(time.Duration(tok.Data.ExpiresIn) * time.Second)
	}
	if acct.AccountID == "" {
		acct.AccountID = tok.Data.Domain
	}

	existing, _ := e.Store.Load()
	own := make([]*auth.Auth, 0, len(existing)+1)
	for _, a := range existing {
		if a.Provider == "codebuddy" {
			own = append(own, a)
		}
	}
	merged := false
	for i, a := range own {
		if acct.Email != "" && strings.EqualFold(a.Email, acct.Email) {
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
	name := acct.Email
	if name == "" {
		name = acct.AccountID
	}
	fmt.Printf("Logged in as %s\n%d account(s) stored in %s\n", name, len(own), e.Store.Path)
	return nil
}

func codebuddyAuthHeaders(req *http.Request, poll bool) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Request-ID", backend.NewID(""))
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("X-Domain", codebuddyDomain(codebuddyBaseURL))
	if poll {
		req.Header.Del("Content-Type")
		span := backend.NewID("")
		req.Header.Set("b3", req.Header.Get("X-Request-ID")+"-"+span+"-1-")
		req.Header.Set("X-B3-TraceId", req.Header.Get("X-Request-ID"))
		req.Header.Set("X-B3-SpanId", span)
		req.Header.Set("X-B3-Sampled", "1")
	}
	req.Header.Set("User-Agent", codebuddyUA)
	req.Header.Set("X-Product", "SaaS")
}
