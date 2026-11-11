package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	agyClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	agyClientSecret = "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"
	agyAuthURL      = "https://accounts.google.com/o/oauth2/auth"
	agyTokenURL     = "https://oauth2.googleapis.com/token"
	agyUserAgent    = "antigravity/cli/1.5.5 linux/amd64"
	defaultAPIBase  = "https://daily-cloudcode-pa.googleapis.com"
	fallbackProject = "aicode-consumers"
)

var agyScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

func uuid4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- auth store (.seavx/auth.jsonc, array of accounts) ---

type Auth struct {
	Provider     string    `json:"provider,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	ProjectID    string    `json:"project_id,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	Tier         string    `json:"tier,omitempty"`
	Email        string    `json:"email,omitempty"`
}

type AuthStore struct{ Path string }

func (s AuthStore) Load() ([]*Auth, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	clean := bytes.TrimSpace(stripJSONC(data))
	var auths []*Auth
	if len(clean) > 0 && clean[0] == '[' {
		err = json.Unmarshal(clean, &auths)
	} else {
		var a Auth
		err = json.Unmarshal(clean, &a)
		auths = []*Auth{&a}
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path, err)
	}
	for _, a := range auths {
		if a.RefreshToken == "" {
			return nil, fmt.Errorf("%s: an account has no refresh_token; run login again", s.Path)
		}
		if a.Provider == "" {
			a.Provider = "antigravity"
		}
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("%s contains no accounts", s.Path)
	}
	return auths, nil
}

var storeMu sync.Mutex

// Save merges with the accounts already on disk so an engine holding only one
// provider's accounts never drops the others.
func (s AuthStore) Save(auths []*Auth) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	var existing []*Auth
	if _, err := os.Stat(s.Path); err == nil {
		var err error
		existing, err = s.Load()
		if err != nil {
			return fmt.Errorf("refusing to overwrite unreadable %s: %w", s.Path, err)
		}
	}
	merged := make([]*Auth, 0, len(existing)+len(auths))
	for _, old := range existing {
		replacement := old
		for _, a := range auths {
			if sameAccount(old, a) {
				replacement = a
				break
			}
		}
		merged = append(merged, replacement)
	}
	for _, a := range auths {
		found := false
		for _, old := range existing {
			if sameAccount(old, a) {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, a)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	out := append([]byte("// SeavxRouter credentials. Keep this file private.\n"), data...)
	out = append(out, '\n')
	return os.WriteFile(s.Path, out, 0o600)
}

func sameAccount(a, b *Auth) bool {
	if a.Provider != b.Provider {
		return false
	}
	if a.Email != "" && b.Email != "" {
		return strings.EqualFold(a.Email, b.Email)
	}
	return a.RefreshToken == b.RefreshToken
}

// stripJSONC removes // and /* */ comments that are outside JSON strings.
func stripJSONC(src []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

// --- native engine ---

type Engine struct {
	Store        AuthStore
	BaseURL      string
	HTTP         *http.Client
	DefaultModel string

	mu       sync.Mutex
	auths    []*Auth
	next     int
	models   []Model
	modelsAt time.Time

	sigMu sync.Mutex
	sigs  map[string]string
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

func NewEngine(store AuthStore, baseURL string, auths []*Auth) (*Engine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no antigravity accounts")
	}
	if baseURL == "" {
		baseURL = defaultAPIBase
	}
	return &Engine{Store: store, BaseURL: baseURL, HTTP: &http.Client{}, auths: auths}, nil
}

func (e *Engine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.auths)
}

func (e *Engine) pick() *Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.auths[e.next%len(e.auths)]
	e.next++
	return a
}

func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "quota") || strings.Contains(msg, "429") ||
		strings.Contains(msg, "resource_exhausted") || strings.Contains(msg, "usage_limit") ||
		strings.Contains(msg, "rate_limit") || strings.Contains(msg, "rate limit")
}

// Login runs the loopback OAuth flow and adds the account to the store.
func (e *Engine) Login(ctx context.Context) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d", port)
	state := uuid4()

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
	if open, err := exec.LookPath("xdg-open"); err == nil {
		exec.Command(open, authURL).Start()
	}

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
	auth := &Auth{
		Provider:     "antigravity",
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	if auth.RefreshToken == "" {
		return fmt.Errorf("google did not return a refresh_token")
	}
	auth.Email = e.fetchEmail(ctx, auth.AccessToken)
	auth.ProjectID, auth.Tier = e.loadCodeAssist(ctx, auth.AccessToken)

	existing, _ := e.Store.Load()
	own := make([]*Auth, 0, len(existing)+1)
	for _, a := range existing {
		if a.Provider == "antigravity" {
			own = append(own, a)
		}
	}
	merged := false
	for i, a := range own {
		if auth.Email != "" && strings.EqualFold(a.Email, auth.Email) {
			own[i] = auth
			merged = true
			break
		}
	}
	if !merged {
		own = append(own, auth)
	}
	e.mu.Lock()
	e.auths = own
	e.mu.Unlock()
	if err := e.Store.Save(own); err != nil {
		return err
	}
	fmt.Printf("Logged in as %s (tier: %s, project: %s)\n%d account(s) stored in %s\n", auth.Email, auth.Tier, auth.ProjectID, len(own), e.Store.Path)
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
func (e *Engine) token(ctx context.Context, a *Auth) (string, error) {
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

func (e *Engine) invalidate(a *Auth) {
	e.mu.Lock()
	a.Expiry = time.Time{}
	e.mu.Unlock()
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
		if lastErr == nil || !isQuotaError(lastErr) || ctx.Err() != nil {
			return lastErr
		}
		slog.Warn("account quota exhausted, trying next", "email", a.Email)
	}
	return lastErr
}

func (e *Engine) postWith(ctx context.Context, a *Auth, method string, data []byte, out any) error {
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
			return apiHTTPError(resp)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func apiHTTPError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s: %s", resp.Status, e.Error.Message)
	}
	return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
}

func (e *Engine) Models(ctx context.Context) ([]Model, error) {
	e.mu.Lock()
	if e.models != nil && time.Since(e.modelsAt) < 5*time.Minute {
		defer e.mu.Unlock()
		return e.models, nil
	}
	e.mu.Unlock()

	var data struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := e.apiPost(ctx, "fetchAvailableModels", map[string]any{}, &data); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(data.Models))
	for id := range data.Models {
		models = append(models, Model{ID: id, Name: id})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if len(models) == 0 {
		return nil, fmt.Errorf("fetchAvailableModels returned no models")
	}
	e.mu.Lock()
	e.models = models
	e.modelsAt = time.Now()
	e.mu.Unlock()
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

func (e *Engine) Complete(ctx context.Context, model, prompt string) (*Result, error) {
	return e.generate(ctx, model, prompt, nil)
}

func (e *Engine) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*Result, error) {
	return e.generate(ctx, model, prompt, onDelta)
}

func (e *Engine) Chat(ctx context.Context, req *ChatRequest, onDelta func(ChatDelta)) (*ChatResult, error) {
	model := req.Model
	if model == "" {
		model = e.DefaultModel
	}
	if model == "" {
		models, err := e.Models(ctx)
		if err != nil {
			return nil, err
		}
		model = models[0].ID
	}

	var sb [8]byte
	rand.Read(sb[:])
	sessionID := fmt.Sprintf("-%d", int64(binary.BigEndian.Uint64(sb[:])&0x7FFFFFFFFFFFFFFF))
	usedClaude := strings.Contains(strings.ToLower(model), "claude")
	inner := map[string]any{
		"contents":  chatContents(req.Messages, e.lookupSig),
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
			"requestId":   fmt.Sprintf("agent/%s/%d/%s/1", uuid4(), time.Now().UnixMilli(), uuid4()),
			"model":       model,
			"requestType": "agent",
			"request":     inner,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		result, started, err := e.chatStreamOnce(ctx, a, data, onDelta)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || !isQuotaError(err) || ctx.Err() != nil {
			return nil, err
		}
		slog.Warn("account quota exhausted, trying next", "email", a.Email)
	}
	return nil, lastErr
}

// chatContents converts unified chat messages into Antigravity/Gemini contents.
func chatContents(msgs []ChatMessage, sig func(string) string) []map[string]any {
	contents := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				parts := make([]map[string]any, 0, len(m.ToolCalls))
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
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{{"functionResponse": map[string]any{
					"name":     m.Name,
					"response": map[string]any{"output": m.Content},
				}}},
			})
		default:
			// user, system, developer: carry the text as a user turn so no
			// instruction is dropped.
			contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"text": m.Content}}})
		}
	}
	return contents
}

// chatTools converts unified tool definitions into Gemini functionDeclarations.
func chatTools(tools []ToolDefinition) []map[string]any {
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
func (e *Engine) chatStreamOnce(ctx context.Context, a *Auth, data []byte, onDelta func(ChatDelta)) (*ChatResult, bool, error) {
	var full strings.Builder
	var toolCalls []ToolCall
	var usage Usage
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
		req.Header.Set("requestId", "req-"+uuid4())
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
			return nil, started, apiHTTPError(resp)
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
									onDelta(ChatDelta{Content: part.Text})
								}
							}
							if part.FunctionCall != nil {
								args := "{}"
								if len(part.FunctionCall.Args) > 0 {
									args = string(part.FunctionCall.Args)
								}
								tc := ToolCall{ID: part.FunctionCall.ID, Type: "function", Name: part.FunctionCall.Name, Arguments: args}
								if tc.ID == "" {
									tc.ID = newID("call_")
								}
								e.rememberSig(tc.ID, part.ThoughtSignature)
								toolCalls = append(toolCalls, tc)
								started = true
								if onDelta != nil {
									onDelta(ChatDelta{ToolCalls: []ToolCall{tc}})
								}
							}
						}
						if cand.FinishReason != "" {
							finishReason = cand.FinishReason
						}
					}
					if u := chunk.Response.UsageMetadata; u != nil {
						usage = Usage{
							InputTokens:     u.PromptTokenCount,
							OutputTokens:    u.CandidatesTokenCount,
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
		return &ChatResult{
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

func (e *Engine) generate(ctx context.Context, model, prompt string, onDelta func(string)) (*Result, error) {
	if model == "" {
		model = e.DefaultModel
	}
	if model == "" {
		models, err := e.Models(ctx)
		if err != nil {
			return nil, err
		}
		model = models[0].ID
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
			"requestId":   fmt.Sprintf("agent/%s/%d/%s/1", uuid4(), time.Now().UnixMilli(), uuid4()),
			"model":       model,
			"requestType": "agent",
			"request":     inner,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		result, started, err := e.streamOnce(ctx, a, data, onDelta)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || !isQuotaError(err) || ctx.Err() != nil {
			return nil, err
		}
		slog.Warn("account quota exhausted, trying next", "email", a.Email)
	}
	return nil, lastErr
}

// streamOnce streams one request with one account; started reports whether any
// content was already delivered (which makes retrying unsafe).
func (e *Engine) streamOnce(ctx context.Context, a *Auth, data []byte, onDelta func(string)) (result *Result, started bool, err error) {
	var full strings.Builder
	var usage Usage
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
		req.Header.Set("requestId", "req-"+uuid4())
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
			return nil, started, apiHTTPError(resp)
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
						usage = Usage{
							InputTokens:     u.PromptTokenCount,
							OutputTokens:    u.CandidatesTokenCount,
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
		return &Result{Status: "SUCCESS", Response: full.String(), Usage: usage}, started, nil
	}
}

func defaultAuthPath() string {
	return ".seavx/auth.jsonc"
}
