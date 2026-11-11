package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	codexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAuthURL     = "https://auth.openai.com/oauth/authorize"
	codexTokenURL    = "https://auth.openai.com/oauth/token"
	codexRedirectURI = "http://localhost:1455/auth/callback"
	codexAPIBase     = "https://chatgpt.com/backend-api/codex"
	codexOriginator  = "codex_cli_rs"
	codexVersion     = "0.159.0"
	codexUserAgent   = "codex_cli_rs/" + codexVersion + " (Linux; x86_64)"
)

type CodexEngine struct {
	Store        AuthStore
	HTTP         *http.Client
	DefaultModel string

	mu       sync.Mutex
	auths    []*Auth
	next     int
	models   []Model
	modelsAt time.Time
}

func NewCodexEngine(store AuthStore, auths []*Auth) (*CodexEngine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no codex accounts")
	}
	return &CodexEngine{Store: store, HTTP: &http.Client{}, auths: auths}, nil
}

func (e *CodexEngine) Default() string { return e.DefaultModel }

func (e *CodexEngine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.auths)
}

func (e *CodexEngine) pick() *Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.auths[e.next%len(e.auths)]
	e.next++
	return a
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Login runs the OpenAI OAuth flow with PKCE on the fixed Codex callback port.
func (e *CodexEngine) Login(ctx context.Context) error {
	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return fmt.Errorf("cannot listen on port 1455 (is codex running?): %w", err)
	}
	var vb [48]byte
	rand.Read(vb[:])
	verifier := base64.RawURLEncoding.EncodeToString(vb[:])
	state := uuid4()

	type callback struct {
		code string
		err  error
	}
	ch := make(chan callback, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
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
		fmt.Fprint(w, "SeavxRouter Codex login successful. You can close this tab.")
		ch <- callback{code: q.Get("code")}
	})}
	go srv.Serve(listener)
	defer srv.Close()

	params := url.Values{
		"client_id":                  {codexClientID},
		"response_type":              {"code"},
		"redirect_uri":               {codexRedirectURI},
		"scope":                      {"openid email profile offline_access"},
		"state":                      {state},
		"code_challenge":             {pkceChallenge(verifier)},
		"code_challenge_method":      {"S256"},
		"prompt":                     {"login"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
	}
	authURL := codexAuthURL + "?" + params.Encode()
	fmt.Println("Open this URL to sign in to OpenAI:\n\n" + authURL + "\n")
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
		"client_id":     {codexClientID},
		"code":          {cb.code},
		"redirect_uri":  {codexRedirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		return err
	}
	auth := &Auth{
		Provider:     "codex",
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	if auth.RefreshToken == "" {
		return fmt.Errorf("openai did not return a refresh_token")
	}
	auth.Email, auth.AccountID, auth.Tier = codexIDTokenClaims(tok.IDToken)

	existing, _ := e.Store.Load()
	own := make([]*Auth, 0, len(existing)+1)
	for _, a := range existing {
		if a.Provider == "codex" {
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
	fmt.Printf("Logged in as %s (plan: %s)\n%d account(s) stored in %s\n", auth.Email, auth.Tier, len(own), e.Store.Path)
	return nil
}

func codexIDTokenClaims(idToken string) (email, accountID, plan string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", ""
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
			Plan      string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", "", ""
	}
	return claims.Email, claims.Auth.AccountID, claims.Auth.Plan
}

type codexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (e *CodexEngine) tokenRequest(ctx context.Context, form url.Values) (*codexTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tok codexTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || tok.Error != "" {
		return nil, fmt.Errorf("token endpoint: %s: %s", tok.Error, tok.ErrorDesc)
	}
	return &tok, nil
}

func (e *CodexEngine) token(ctx context.Context, a *Auth) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a.Expiry.After(time.Now().Add(3 * time.Minute)) {
		return a.AccessToken, nil
	}
	tok, err := e.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {a.RefreshToken},
		"client_id":     {codexClientID},
		"scope":         {"openid profile email"},
	})
	if err != nil {
		return "", err
	}
	a.AccessToken = tok.AccessToken
	a.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.RefreshToken != "" {
		a.RefreshToken = tok.RefreshToken
	}
	if _, accountID, _ := codexIDTokenClaims(tok.IDToken); accountID != "" {
		a.AccountID = accountID
	}
	if err := e.Store.Save(e.auths); err != nil {
		slog.Warn("could not persist refreshed token", "err", err)
	}
	return a.AccessToken, nil
}

func (e *CodexEngine) setHeaders(req *http.Request, token string, a *Auth, stream bool) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Originator", codexOriginator)
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Session-Id", uuid4())
	if a.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", a.AccountID)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
}

func (e *CodexEngine) Models(ctx context.Context) ([]Model, error) {
	e.mu.Lock()
	if e.models != nil && time.Since(e.modelsAt) < 5*time.Minute {
		defer e.mu.Unlock()
		return e.models, nil
	}
	e.mu.Unlock()

	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		models, err := e.modelsWith(ctx, a)
		if err == nil {
			e.mu.Lock()
			e.models = models
			e.modelsAt = time.Now()
			e.mu.Unlock()
			return models, nil
		}
		lastErr = err
		if !isQuotaError(err) || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (e *CodexEngine) modelsWith(ctx context.Context, a *Auth) ([]Model, error) {
	token, err := e.token(ctx, a)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", codexAPIBase+"/models?client_version="+codexVersion, nil)
	if err != nil {
		return nil, err
	}
	e.setHeaders(req, token, a, false)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiHTTPError(resp)
	}
	var data struct {
		Models []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(data.Models))
	for _, m := range data.Models {
		id := m.ID
		if id == "" {
			id = m.Slug
		}
		if id != "" {
			models = append(models, Model{ID: id, Name: id})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if len(models) == 0 {
		return nil, fmt.Errorf("codex /models returned no models")
	}
	return models, nil
}

func (e *CodexEngine) HasModel(ctx context.Context, id string) bool {
	models, err := e.Models(ctx)
	if err != nil {
		return false
	}
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (e *CodexEngine) Complete(ctx context.Context, model, prompt string) (*Result, error) {
	return e.generate(ctx, model, prompt, nil)
}

func (e *CodexEngine) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*Result, error) {
	return e.generate(ctx, model, prompt, onDelta)
}

type codexEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Response *struct {
		Usage *struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			TotalTokens         int `json:"total_tokens"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

func (e *CodexEngine) generate(ctx context.Context, model, prompt string, onDelta func(string)) (*Result, error) {
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
	payload := map[string]any{
		"model":        model,
		"instructions": "",
		"input": []map[string]any{{
			"type":    "message",
			"role":    "user",
			"content": []map[string]string{{"type": "input_text", "text": prompt}},
		}},
		"stream": true,
		"store":  false,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for i, n := 0, e.count(); i < n; i++ {
		a := e.pick()
		result, started, err := e.streamOnce(ctx, a, data, onDelta)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || !isQuotaError(err) || ctx.Err() != nil {
			return nil, err
		}
		slog.Warn("codex account quota exhausted, trying next", "email", a.Email)
	}
	return nil, lastErr
}

func (e *CodexEngine) streamOnce(ctx context.Context, a *Auth, data []byte, onDelta func(string)) (result *Result, started bool, err error) {
	var full strings.Builder
	var usage Usage
	for attempt := 0; ; attempt++ {
		token, err := e.token(ctx, a)
		if err != nil {
			return nil, started, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", codexAPIBase+"/responses", bytes.NewReader(data))
		if err != nil {
			return nil, started, err
		}
		e.setHeaders(req, token, a, true)
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return nil, started, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			e.mu.Lock()
			a.Expiry = time.Time{}
			e.mu.Unlock()
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
				var ev codexEvent
				if json.Unmarshal(payload, &ev) == nil {
					switch ev.Type {
					case "response.output_text.delta":
						if ev.Delta != "" {
							full.WriteString(ev.Delta)
							started = true
							if onDelta != nil {
								onDelta(ev.Delta)
							}
						}
					case "response.completed", "response.done":
						if ev.Response != nil && ev.Response.Usage != nil {
							u := ev.Response.Usage
							usage = Usage{
								InputTokens:    u.InputTokens,
								OutputTokens:   u.OutputTokens,
								ThinkingTokens: u.OutputTokensDetails.ReasoningTokens,
								TotalTokens:    u.TotalTokens,
							}
						}
					case "response.failed", "error":
						msg := ev.Message
						if ev.Error != nil && ev.Error.Message != "" {
							msg = ev.Error.Message
						} else if ev.Response != nil && ev.Response.Error != nil {
							msg = ev.Response.Error.Message
						}
						resp.Body.Close()
						return nil, started, fmt.Errorf("codex: %s", msg)
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
