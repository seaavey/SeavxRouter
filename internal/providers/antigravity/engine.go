package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

const (
	agyClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	agyClientSecret = "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"
	agyAuthURL      = "https://accounts.google.com/o/oauth2/auth"
	agyTokenURL     = "https://oauth2.googleapis.com/token"
	agyUserAgent    = "antigravity/cli/1.5.5 linux/amd64"
	DefaultAPIBase  = "https://daily-cloudcode-pa.googleapis.com"
	fallbackProject = "aicode-consumers"
)

var agyScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// --- native engine ---

type Engine struct {
	Store        auth.AuthStore
	BaseURL      string
	HTTP         *http.Client
	DefaultModel string

	mu       sync.Mutex
	auths    []*auth.Auth
	cache    auth.AccountCache
	next     int
	models   []backend.Model
	modelsAt time.Time

	sigMu sync.Mutex
	sigs  map[string]string

	quotaMu   sync.Mutex
	quotaErr  map[*auth.Auth]time.Time
	quotaLive map[*auth.Auth]liveQuota
}

func (e *Engine) Default() string { return e.DefaultModel }

// rememberSig caches a Gemini thoughtSignature by tool-call ID so it can be
// echoed back on the next turn; Gemini 3 rejects functionCall parts without it.
func (e *Engine) rememberSig(id, sig string) {
	if id == "" || sig == "" {
		return
	}
	e.sigMu.Lock()
	defer e.sigMu.Unlock()
	if e.sigs == nil || len(e.sigs) >= 4096 {
		e.sigs = map[string]string{}
	}
	e.sigs[id] = sig
}

func (e *Engine) lookupSig(id string) string {
	e.sigMu.Lock()
	defer e.sigMu.Unlock()
	return e.sigs[id]
}

func NewEngine(store auth.AuthStore, baseURL string, auths []*auth.Auth) (*Engine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no antigravity accounts")
	}
	if baseURL == "" {
		baseURL = DefaultAPIBase
	}
	return &Engine{Store: store, BaseURL: baseURL, HTTP: &http.Client{}, auths: auths, cache: auth.NewAccountCache(store, "antigravity")}, nil
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

func (e *Engine) snapshotAuths() []*auth.Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	return append([]*auth.Auth(nil), e.auths...)
}

// Login runs the loopback OAuth flow and adds the account to the store.
func (e *Engine) Login(ctx context.Context) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d", port)
	state := backend.UUID()

	type callback struct {
		code string
		err  error
	}
	ch := make(chan callback, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			fmt.Fprintf(w, "Login failed: %s", e)
			ch <- callback{err: fmt.Errorf("oauth: %s", e)}
			return
		}
		fmt.Fprint(w, "SeavxRouter login successful. You can close this tab.")
		ch <- callback{code: q.Get("code")}
	})}
	go srv.Serve(listener)
	defer srv.Close()

	params := url.Values{
		"client_id":              {agyClientID},
		"redirect_uri":           {redirectURI},
		"scope":                  {strings.Join(agyScopes, " ")},
		"response_type":          {"code"},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"true"},
		"state":                  {state},
	}
	authURL := agyAuthURL + "?" + params.Encode()
	fmt.Println("Open this URL to sign in:\n\n" + authURL + "\n")
	auth.OpenBrowser(authURL)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var cb callback
	select {
	case cb = <-ch:
	case <-ctx.Done():
		return fmt.Errorf("login timed out")
	}
	if cb.err != nil {
		return cb.err
	}

	tok, err := e.tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {cb.code},
		"client_id":     {agyClientID},
		"client_secret": {agyClientSecret},
		"redirect_uri":  {redirectURI},
	})
	if err != nil {
		return err
	}
	acct := &auth.Auth{
		Provider:     "antigravity",
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	if acct.RefreshToken == "" {
		return fmt.Errorf("google did not return a refresh_token")
	}
	acct.Email = e.fetchEmail(ctx, acct.AccessToken)
	acct.ProjectID, acct.Tier = e.loadCodeAssist(ctx, acct.AccessToken)

	existing, _ := e.Store.Load()
	own := make([]*auth.Auth, 0, len(existing)+1)
	for _, a := range existing {
		if a.Provider == "antigravity" {
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
	fmt.Printf("Logged in as %s (tier: %s, project: %s)\n%d account(s) stored in %s\n", acct.Email, acct.Tier, acct.ProjectID, len(own), e.Store.Path)
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (e *Engine) tokenRequest(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", agyTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tok tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || tok.Error != "" {
		return nil, fmt.Errorf("token endpoint: %s: %s", tok.Error, tok.ErrorDesc)
	}
	return &tok, nil
}

func (e *Engine) fetchEmail(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var info struct {
		Email string `json:"email"`
	}
	json.NewDecoder(resp.Body).Decode(&info)
	return info.Email
}

func (e *Engine) loadCodeAssist(ctx context.Context, accessToken string) (project, tier string) {
	body, _ := json.Marshal(map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY"}})
	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1internal:loadCodeAssist", bytes.NewReader(body))
	if err != nil {
		return fallbackProject, ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", agyUserAgent)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return fallbackProject, ""
	}
	defer resp.Body.Close()
	var data struct {
		Project  string `json:"cloudaicompanionProject"`
		PaidTier struct {
			ID string `json:"id"`
		} `json:"paidTier"`
		CurrentTier struct {
			ID string `json:"id"`
		} `json:"currentTier"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&data) != nil {
		return fallbackProject, ""
	}
	tier = data.PaidTier.ID
	if tier == "" {
		tier = data.CurrentTier.ID
	}
	if data.Project != "" {
		return data.Project, tier
	}
	return fallbackProject, tier
}

// token returns a valid access token for the account, refreshing and persisting if needed.
func (e *Engine) token(ctx context.Context, a *auth.Auth) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a.Expiry.After(time.Now().Add(3 * time.Minute)) {
		return a.AccessToken, nil
	}
	tok, err := e.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {a.RefreshToken},
		"client_id":     {agyClientID},
		"client_secret": {agyClientSecret},
	})
	if err != nil {
		return "", err
	}
	a.AccessToken = tok.AccessToken
	a.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.RefreshToken != "" {
		a.RefreshToken = tok.RefreshToken
	}
	if err := e.Store.Save(e.auths); err != nil {
		slog.Warn("could not persist refreshed token", "err", err)
	}
	return a.AccessToken, nil
}

func (e *Engine) invalidate(a *auth.Auth) {
	e.mu.Lock()
	a.Expiry = time.Time{}
	e.mu.Unlock()
}

// trackQuota records the latest request outcome per account so /v1/quota can
// report accounts that recently hit a provider quota or rate limit.
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

func (e *Engine) apiPost(ctx context.Context, method string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		lastErr = e.postWith(ctx, a, method, data, out)
		e.trackQuota(a, lastErr)
		if lastErr == nil || !backend.IsQuotaError(lastErr) || ctx.Err() != nil {
			return lastErr
		}
		slog.Warn("account quota exhausted, trying next", "email", a.Email)
	}
	return lastErr
}

func (e *Engine) postWith(ctx context.Context, a *auth.Auth, method string, data []byte, out any) error {
	for attempt := 0; ; attempt++ {
		token, err := e.token(ctx, a)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1internal:"+method, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", agyUserAgent)
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			e.invalidate(a)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return backend.APIHTTPError(resp)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

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

// Check verifies each account against upstream, bypassing the Models cache.
func (e *Engine) Check(ctx context.Context) []backend.AccountCheck {
	auths := e.snapshotAuths()
	out := make([]backend.AccountCheck, 0, len(auths))
	for _, a := range auths {
		start := time.Now()
		models, err := e.modelsWith(ctx, a)
		e.trackQuota(a, err)
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

func (e *Engine) fetchModels(ctx context.Context) ([]backend.Model, error) {
	var data agyModels
	if err := e.apiPost(ctx, "fetchAvailableModels", map[string]any{}, &data); err != nil {
		return nil, err
	}
	return data.list()
}

func (e *Engine) modelsWith(ctx context.Context, a *auth.Auth) ([]backend.Model, error) {
	var data agyModels
	if err := e.postWith(ctx, a, "fetchAvailableModels", []byte("{}"), &data); err != nil {
		return nil, err
	}
	return data.list()
}

type agyModels struct {
	Models map[string]json.RawMessage `json:"models"`
}

// unadvertisedModel filters catalog entries the chat endpoint cannot use:
// internal chat/tab placeholders and disabled previews.
func unadvertisedModel(id string) bool {
	return strings.HasPrefix(id, "chat_") || strings.HasPrefix(id, "tab_") || strings.HasSuffix(id, "-disabled")
}

// PreferredDefault picks the catalog's default chat model. The catalog is
// sorted by ID, so the first entry alone would land on Claude; the standard
// Gemini flash is the intended default.
func PreferredDefault(models []backend.Model) string {
	for _, m := range models {
		if m.ID == "gemini-3.8-flash-high" {
			return m.ID
		}
	}
	if len(models) > 0 {
		return models[0].ID
	}
	return ""
}

func (m agyModels) list() ([]backend.Model, error) {
	models := make([]backend.Model, 0, len(m.Models))
	for id := range m.Models {
		if unadvertisedModel(id) {
			continue
		}
		models = append(models, backend.Model{ID: id, Name: id})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if len(models) == 0 {
		return nil, fmt.Errorf("fetchAvailableModels returned no models")
	}
	return models, nil
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
	return e.generate(ctx, model, prompt, nil)
}

func (e *Engine) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
	return e.generate(ctx, model, prompt, onDelta)
}

func (e *Engine) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	model := req.Model
	if model == "" {
		model = e.DefaultModel
	}
	if model == "" {
		models, err := e.Models(ctx)
		if err != nil {
			return nil, err
		}
		model = PreferredDefault(models)
	}

	var sb [8]byte
	rand.Read(sb[:])
	sessionID := fmt.Sprintf("-%d", int64(binary.BigEndian.Uint64(sb[:])&0x7FFFFFFFFFFFFFFF))
	usedClaude := strings.Contains(strings.ToLower(model), "claude")
	contents, err := chatContents(req.Messages, e.lookupSig)
	if err != nil {
		return nil, err
	}
	inner := map[string]any{
		"contents":  contents,
		"sessionId": sessionID,
		"labels": map[string]string{
			"last_step_index":          "1",
			"model_enum":               model,
			"trajectory_id":            sessionID,
			"used_claude":              fmt.Sprint(usedClaude),
			"used_claude_conservative": fmt.Sprint(usedClaude),
		},
		"toolConfig": map[string]any{
			"functionCallingConfig": map[string]string{"mode": "VALIDATED"},
		},
	}
	if tools := chatTools(req.Tools); tools != nil {
		inner["tools"] = tools
	}
	if req.Temperature != nil || req.MaxTokens != nil {
		gc := map[string]any{}
		if req.Temperature != nil {
			gc["temperature"] = *req.Temperature
		}
		if req.MaxTokens != nil {
			gc["maxOutputTokens"] = *req.MaxTokens
		}
		inner["generationConfig"] = gc
	}

	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		payload := map[string]any{
			"project":     a.ProjectID,
			"requestId":   fmt.Sprintf("agent/%s/%d/%s/1", backend.UUID(), time.Now().UnixMilli(), backend.UUID()),
			"model":       model,
			"requestType": "agent",
			"request":     inner,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		result, started, err := e.chatStreamOnce(ctx, a, data, onDelta)
		e.trackQuota(a, err)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || !backend.IsQuotaError(err) || ctx.Err() != nil {
			return nil, err
		}
		slog.Warn("account quota exhausted, trying next", "email", a.Email)
	}
	return nil, lastErr
}

// chatContents converts unified chat messages into Antigravity/Gemini contents.
func chatContents(msgs []backend.ChatMessage, sig func(string) string) ([]map[string]any, error) {
	contents := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				parts := make([]map[string]any, 0, len(m.ToolCalls)+1)
				if m.Content != "" {
					parts = append(parts, map[string]any{"text": m.Content})
				}
				for _, tc := range m.ToolCalls {
					var args any = map[string]any{}
					if tc.Arguments != "" {
						var parsed any
						if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
							args = parsed
						}
					}
					fc := map[string]any{"name": tc.Name, "args": args}
					if tc.ID != "" {
						fc["id"] = tc.ID
					}
					part := map[string]any{"functionCall": fc}
					if sig != nil {
						if s := sig(tc.ID); s != "" {
							part["thoughtSignature"] = s
						}
					}
					parts = append(parts, part)
				}
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			} else {
				contents = append(contents, map[string]any{"role": "model", "parts": []map[string]any{{"text": m.Content}}})
			}
		case "tool":
			// Antigravity rejects role "function"; function responses ride a user turn.
			fr := map[string]any{
				"name":     m.Name,
				"response": map[string]any{"output": m.Content},
			}
			if m.ToolCallID != "" {
				fr["id"] = m.ToolCallID
			}
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"functionResponse": fr}},
			})
		default:
			// user, system, developer: carry the text as a user turn so no
			// instruction is dropped.
			parts := make([]map[string]any, 0, 1+len(m.Images))
			if m.Content != "" || len(m.Images) == 0 {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, img := range m.Images {
				if img.Data == "" {
					return nil, fmt.Errorf("antigravity backend requires inline image data; remote image URLs are not supported")
				}
				mime := img.MIMEType
				if mime == "" {
					mime = "image/png"
				}
				parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": img.Data}})
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		}
	}
	return contents, nil
}

// chatTools converts unified tool definitions into Gemini functionDeclarations.
func chatTools(tools []backend.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		d := map[string]any{"name": t.Name}
		if t.Description != "" {
			d["description"] = t.Description
		}
		if t.Parameters != nil {
			d["parameters"] = uppercaseSchemaTypes(t.Parameters)
		}
		decls = append(decls, d)
	}
	return []map[string]any{{"functionDeclarations": decls}}
}

// uppercaseSchemaTypes uppercases JSON Schema "type" values (object -> OBJECT)
// recursively through properties and items, matching Gemini's Schema format.
func uppercaseSchemaTypes(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		switch k {
		case "type":
			if s, ok := v.(string); ok {
				out[k] = strings.ToUpper(s)
			} else {
				out[k] = v
			}
		case "properties":
			if props, ok := v.(map[string]any); ok {
				np := make(map[string]any, len(props))
				for pk, pv := range props {
					if pm, ok := pv.(map[string]any); ok {
						np[pk] = uppercaseSchemaTypes(pm)
					} else {
						np[pk] = pv
					}
				}
				out[k] = np
			} else {
				out[k] = v
			}
		case "items":
			if im, ok := v.(map[string]any); ok {
				out[k] = uppercaseSchemaTypes(im)
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

func mapFinishReason(reason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	switch reason {
	case "STOP", "":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return strings.ToLower(reason)
	}
}

type chatChunk struct {
	Response struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text             string `json:"text"`
					Thought          bool   `json:"thought"`
					ThoughtSignature string `json:"thoughtSignature"`
					FunctionCall     *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
						ID   string          `json:"id"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chatStreamOnce streams one structured chat request with one account, parsing
// both text and functionCall parts. started reports whether any output was
// already delivered (which makes retrying unsafe).
func (e *Engine) chatStreamOnce(ctx context.Context, a *auth.Auth, data []byte, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	var full strings.Builder
	var toolCalls []backend.ToolCall
	var usage backend.Usage
	finishReason := ""
	started := false
	for attempt := 0; ; attempt++ {
		token, err := e.token(ctx, a)
		if err != nil {
			return nil, started, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1internal:streamGenerateContent?alt=sse", bytes.NewReader(data))
		if err != nil {
			return nil, started, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", agyUserAgent)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("requestId", "req-"+backend.UUID())
		req.Header.Set("requestType", "agent")
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return nil, started, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			e.invalidate(a)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, started, backend.APIHTTPError(resp)
		}

		full.Reset()
		toolCalls = nil
		reader := bufio.NewReaderSize(resp.Body, 1024*1024)
		for {
			line, readErr := reader.ReadBytes('\n')
			if payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				var chunk chatChunk
				if json.Unmarshal(payload, &chunk) == nil {
					if chunk.Error != nil {
						resp.Body.Close()
						return nil, started, fmt.Errorf("upstream: %s", chunk.Error.Message)
					}
					for _, cand := range chunk.Response.Candidates {
						for _, part := range cand.Content.Parts {
							if part.Text != "" && !part.Thought {
								full.WriteString(part.Text)
								started = true
								if onDelta != nil {
									onDelta(backend.ChatDelta{Content: part.Text})
								}
							}
							if part.FunctionCall != nil {
								args := "{}"
								if len(part.FunctionCall.Args) > 0 {
									args = string(part.FunctionCall.Args)
								}
								tc := backend.ToolCall{ID: part.FunctionCall.ID, Type: "function", Name: part.FunctionCall.Name, Arguments: args}
								if tc.ID == "" {
									tc.ID = backend.NewID("call_")
								}
								e.rememberSig(tc.ID, part.ThoughtSignature)
								toolCalls = append(toolCalls, tc)
								started = true
								if onDelta != nil {
									onDelta(backend.ChatDelta{ToolCalls: []backend.ToolCall{tc}})
								}
							}
						}
						if cand.FinishReason != "" {
							finishReason = cand.FinishReason
						}
					}
					if u := chunk.Response.UsageMetadata; u != nil {
						usage = backend.Usage{
							InputTokens:     u.PromptTokenCount,
							OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
							ThinkingTokens:  u.ThoughtsTokenCount,
							CacheReadTokens: u.CachedContentTokenCount,
							TotalTokens:     u.TotalTokenCount,
						}
					}
				}
			}
			if readErr != nil {
				break
			}
		}
		resp.Body.Close()
		if ctx.Err() != nil {
			return nil, started, ctx.Err()
		}
		return &backend.ChatResult{
			Content:      full.String(),
			ToolCalls:    toolCalls,
			Usage:        usage,
			FinishReason: mapFinishReason(finishReason, len(toolCalls) > 0),
		}, started, nil
	}
}

type genChunk struct {
	Response struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (e *Engine) generate(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
	if model == "" {
		model = e.DefaultModel
	}
	if model == "" {
		models, err := e.Models(ctx)
		if err != nil {
			return nil, err
		}
		model = PreferredDefault(models)
	}

	sessionID := func() string {
		var sb [8]byte
		rand.Read(sb[:])
		return fmt.Sprintf("-%d", int64(binary.BigEndian.Uint64(sb[:])&0x7FFFFFFFFFFFFFFF))
	}()
	usedClaude := strings.Contains(strings.ToLower(model), "claude")
	inner := map[string]any{
		"contents":  []map[string]any{{"role": "user", "parts": []map[string]string{{"text": prompt}}}},
		"sessionId": sessionID,
		"labels": map[string]string{
			"last_step_index":          "1",
			"model_enum":               model,
			"trajectory_id":            sessionID,
			"used_claude":              fmt.Sprint(usedClaude),
			"used_claude_conservative": fmt.Sprint(usedClaude),
		},
		"toolConfig": map[string]any{
			"functionCallingConfig": map[string]string{"mode": "VALIDATED"},
		},
	}

	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		payload := map[string]any{
			"project":     a.ProjectID,
			"requestId":   fmt.Sprintf("agent/%s/%d/%s/1", backend.UUID(), time.Now().UnixMilli(), backend.UUID()),
			"model":       model,
			"requestType": "agent",
			"request":     inner,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		result, started, err := e.streamOnce(ctx, a, data, onDelta)
		e.trackQuota(a, err)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || !backend.IsQuotaError(err) || ctx.Err() != nil {
			return nil, err
		}
		slog.Warn("account quota exhausted, trying next", "email", a.Email)
	}
	return nil, lastErr
}

// streamOnce streams one request with one account; started reports whether any
// content was already delivered (which makes retrying unsafe).
func (e *Engine) streamOnce(ctx context.Context, a *auth.Auth, data []byte, onDelta func(string)) (result *backend.Result, started bool, err error) {
	var full strings.Builder
	var usage backend.Usage
	for attempt := 0; ; attempt++ {
		token, err := e.token(ctx, a)
		if err != nil {
			return nil, started, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1internal:streamGenerateContent?alt=sse", bytes.NewReader(data))
		if err != nil {
			return nil, started, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", agyUserAgent)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("requestId", "req-"+backend.UUID())
		req.Header.Set("requestType", "agent")
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return nil, started, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			e.invalidate(a)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, started, backend.APIHTTPError(resp)
		}

		full.Reset()
		reader := bufio.NewReaderSize(resp.Body, 1024*1024)
		for {
			line, readErr := reader.ReadBytes('\n')
			if payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				var chunk genChunk
				if json.Unmarshal(payload, &chunk) == nil {
					if chunk.Error != nil {
						resp.Body.Close()
						return nil, started, fmt.Errorf("upstream: %s", chunk.Error.Message)
					}
					for _, cand := range chunk.Response.Candidates {
						for _, part := range cand.Content.Parts {
							if part.Text != "" && !part.Thought {
								full.WriteString(part.Text)
								started = true
								if onDelta != nil {
									onDelta(part.Text)
								}
							}
						}
					}
					if u := chunk.Response.UsageMetadata; u != nil {
						usage = backend.Usage{
							InputTokens:     u.PromptTokenCount,
							OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
							ThinkingTokens:  u.ThoughtsTokenCount,
							CacheReadTokens: u.CachedContentTokenCount,
							TotalTokens:     u.TotalTokenCount,
						}
					}
				}
			}
			if readErr != nil {
				break
			}
		}
		resp.Body.Close()
		if ctx.Err() != nil {
			return nil, started, ctx.Err()
		}
		return &backend.Result{Status: "SUCCESS", Response: full.String(), Usage: usage}, started, nil
	}
}
