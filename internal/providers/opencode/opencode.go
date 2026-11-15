// Package opencode serves chat through a local OpenCode v2 server. OpenCode's
// free model tier is only available to requests that originate inside its own
// agent runtime, so the router talks to a private `opencode serve` process
// instead of calling the public inference endpoint directly.
package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

const (
	defaultBinary   = "opencode"
	defaultUsername = "opencode"
	defaultModelID  = "big-pickle"
	startupTimeout  = 20 * time.Second
	probeInterval   = 150 * time.Millisecond
	logTailLimit    = 4096
)

// Config locates the OpenCode server. An empty URL starts a private server
// with the OpenCode binary; a non-empty URL connects to an existing server.
type Config struct {
	URL      string
	Username string
	Password string
	Binary   string
	WorkDir  string
	HTTP     *http.Client
}

// ConfigFromEnv reads the supported OpenCode environment variables.
func ConfigFromEnv() Config {
	return Config{
		URL:      strings.TrimSpace(os.Getenv("OPENCODE_SERVER_URL")),
		Username: strings.TrimSpace(os.Getenv("OPENCODE_SERVER_USERNAME")),
		Password: os.Getenv("OPENCODE_SERVER_PASSWORD"),
		Binary:   strings.TrimSpace(os.Getenv("OPENCODE_BIN")),
		WorkDir:  defaultWorkDir(),
	}
}

func defaultWorkDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".seavx", "opencode")
	}
	return filepath.Join(os.TempDir(), "seavxrouter-opencode")
}

// Engine implements the backend contract against one OpenCode v2 server.
type Engine struct {
	cfg          Config
	client       *http.Client
	defaultModel string

	mu      sync.Mutex
	closed  bool
	started bool
	conn    serverConn
	proc    *serverProcess
}

type serverConn struct {
	baseURL  string
	username string
	password string
}

var (
	_ backend.BackendAPI = (*Engine)(nil)
	_ backend.Checker    = (*Engine)(nil)
)

// NewEngine validates the configuration. Auto-started servers are launched
// lazily on the first request so an unused provider costs nothing.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("opencode: invalid OPENCODE_SERVER_URL %q", cfg.URL)
		}
		cfg.URL = strings.TrimRight(cfg.URL, "/")
	}
	if cfg.Username == "" {
		cfg.Username = defaultUsername
	}
	if cfg.Binary == "" {
		cfg.Binary = defaultBinary
	}
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{}
	}
	return &Engine{cfg: cfg, client: client, defaultModel: defaultModelID}, nil
}

// Login verifies the local server and stores a marker account. The marker
// carries no secret; the server connection itself comes from the environment.
func Login(ctx context.Context, store auth.AuthStore, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	cfg := ConfigFromEnv()
	engine, err := NewEngine(cfg)
	if err != nil {
		return err
	}
	defer engine.Close()

	models, err := engine.Models(ctx)
	if err != nil {
		return fmt.Errorf("opencode login failed: %w", err)
	}
	if err := store.Save([]*auth.Auth{{Provider: "opencode", AccessToken: "local"}}); err != nil {
		return err
	}
	via := "local server (started on demand)"
	if cfg.URL != "" {
		via = cfg.URL
	}
	fmt.Fprintf(out, "opencode: %d free model(s) available via %s\nstored in %s\n", len(models), via, store.Path)
	return nil
}

func (e *Engine) Default() string { return e.defaultModel }

// Models lists chat models that OpenCode marks free and enabled.
func (e *Engine) Models(ctx context.Context) ([]backend.Model, error) {
	var resp struct {
		Data []modelInfo `json:"data"`
	}
	if err := e.doJSON(ctx, http.MethodGet, "/api/model", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]backend.Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ProviderID != "opencode" || m.Type != "chat" || !m.Enabled || !m.isFree() {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, backend.Model{ID: m.ID, Name: name})
	}
	if len(out) == 0 {
		return nil, errors.New("opencode: no free models available")
	}
	return out, nil
}

func (e *Engine) HasModel(ctx context.Context, id string) bool {
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

func (e *Engine) Complete(ctx context.Context, model, prompt string) (*backend.Result, error) {
	res, err := e.Chat(ctx, &backend.ChatRequest{
		Model:    model,
		Messages: []backend.ChatMessage{{Role: "user", Content: prompt}},
	}, nil)
	if err != nil {
		return nil, err
	}
	return &backend.Result{Response: res.Content, Usage: res.Usage, Status: "SUCCESS"}, nil
}

func (e *Engine) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
	var full strings.Builder
	res, err := e.Chat(ctx, &backend.ChatRequest{
		Model:    model,
		Messages: []backend.ChatMessage{{Role: "user", Content: prompt}},
		Stream:   true,
	}, func(d backend.ChatDelta) {
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
	text := full.String()
	if text == "" {
		text = res.Content
	}
	return &backend.Result{Response: text, Usage: res.Usage, Status: "SUCCESS"}, nil
}

// Chat flattens the conversation into one prompt, creates a throwaway session,
// and asks OpenCode for a transient generation. Session generation is the
// narrowest API that still benefits from the in-OpenCode free tier: it carries
// no agent loop, does not touch session history, and reports token usage.
func (e *Engine) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	if backend.HasImages(req.Messages) {
		return nil, errors.New("opencode: image content is not supported through the local server proxy")
	}
	model := req.Model
	if model == "" {
		model = e.Default()
	}
	prompt := backend.ChatMessagesToPrompt(req.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("opencode: empty prompt")
	}

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := e.doJSON(ctx, http.MethodPost, "/api/session", map[string]any{
		"title": "seavxrouter",
		"model": map[string]string{"providerID": "opencode", "id": model},
	}, &created); err != nil {
		return nil, err
	}
	sessionID := created.Data.ID
	if sessionID == "" {
		return nil, errors.New("opencode: server returned an empty session id")
	}
	defer e.deleteSession(sessionID)

	var generated struct {
		Data struct {
			Text   string     `json:"text"`
			Tokens tokenUsage `json:"tokens"`
		} `json:"data"`
	}
	if err := e.doJSON(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/generate", map[string]string{
		"prompt": prompt,
	}, &generated); err != nil {
		return nil, err
	}

	usage := backend.Usage{
		InputTokens:     int(generated.Data.Tokens.Input),
		OutputTokens:    int(generated.Data.Tokens.Output),
		ThinkingTokens:  int(generated.Data.Tokens.Reasoning),
		CacheReadTokens: int(generated.Data.Tokens.Cache.Read),
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.ThinkingTokens
	if onDelta != nil && generated.Data.Text != "" {
		onDelta(backend.ChatDelta{Content: generated.Data.Text})
	}
	return &backend.ChatResult{
		Content:      generated.Data.Text,
		Usage:        usage,
		FinishReason: "stop",
	}, nil
}

func (e *Engine) deleteSession(sessionID string) {
	e.mu.Lock()
	conn := e.conn
	started := e.started
	e.mu.Unlock()
	if !started {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, conn.baseURL+"/api/session/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return
	}
	if conn.password != "" {
		req.SetBasicAuth(conn.username, conn.password)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

// Check verifies the local server and returns its free catalog.
func (e *Engine) Check(ctx context.Context) []backend.AccountCheck {
	start := time.Now()
	models, err := e.Models(ctx)
	e.mu.Lock()
	address := e.conn.baseURL
	e.mu.Unlock()
	return []backend.AccountCheck{{
		Account:   "local server",
		AccountID: address,
		Models:    models,
		Err:       err,
		Latency:   time.Since(start),
	}}
}

// Close stops a server process started by this engine. It is a no-op when the
// engine points at an external server.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.proc != nil {
		e.proc.stop()
		e.proc = nil
	}
	return nil
}

type modelInfo struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Cost       []struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

func (m modelInfo) isFree() bool {
	for _, c := range m.Cost {
		if c.Input != 0 || c.Output != 0 {
			return false
		}
	}
	return true
}

type tokenUsage struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	Reasoning float64 `json:"reasoning"`
	Cache     struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}

type serverProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *serverProcess) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *serverProcess) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
}

func (e *Engine) ensure(ctx context.Context) (serverConn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return serverConn{}, errors.New("opencode: engine is closed")
	}
	if e.cfg.URL != "" {
		if !e.started {
			e.conn = serverConn{baseURL: e.cfg.URL, username: e.cfg.Username, password: e.cfg.Password}
			e.started = true
		}
		return e.conn, nil
	}
	if e.started && e.proc != nil && !e.proc.exited() {
		return e.conn, nil
	}
	e.proc = nil
	e.started = false
	if err := e.startLocked(ctx); err != nil {
		return serverConn{}, err
	}
	return e.conn, nil
}

func (e *Engine) startLocked(ctx context.Context) error {
	path, err := exec.LookPath(e.cfg.Binary)
	if err != nil {
		return fmt.Errorf("opencode: binary %q not found; install OpenCode or set OPENCODE_SERVER_URL", e.cfg.Binary)
	}
	workdir := e.cfg.WorkDir
	if workdir == "" {
		workdir = defaultWorkDir()
	}
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return fmt.Errorf("opencode: cannot create workdir: %w", err)
	}
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("opencode: cannot pick a local port: %w", err)
	}
	password, err := randomPassword()
	if err != nil {
		return fmt.Errorf("opencode: cannot generate server password: %w", err)
	}

	cmd := exec.Command(path, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"OPENCODE_SERVER_USERNAME="+defaultUsername,
		"OPENCODE_SERVER_PASSWORD="+password,
	)
	logs := &tailWriter{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opencode: cannot start server: %w", err)
	}

	proc := &serverProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		proc.err = cmd.Wait()
		close(proc.done)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitReady(ctx, proc, logs, baseURL, defaultUsername, password); err != nil {
		proc.stop()
		return err
	}
	e.conn = serverConn{baseURL: baseURL, username: defaultUsername, password: password}
	e.proc = proc
	e.started = true
	return nil
}

func waitReady(ctx context.Context, proc *serverProcess, logs *tailWriter, baseURL, username, password string) error {
	deadline, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(deadline, http.MethodGet, baseURL+"/api/info", nil)
		if err != nil {
			return fmt.Errorf("opencode: %w", err)
		}
		req.SetBasicAuth(username, password)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				return nil
			case http.StatusUnauthorized:
				return errors.New("opencode: server rejected the generated password")
			case http.StatusNotFound:
				return errors.New("opencode: server does not expose the v2 API; upgrade OpenCode")
			}
		}

		select {
		case <-proc.done:
			detail := logs.String()
			if detail != "" {
				return fmt.Errorf("opencode: server exited during startup: %v (%s)", proc.err, detail)
			}
			return fmt.Errorf("opencode: server exited during startup: %v", proc.err)
		case <-deadline.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("opencode: server startup timeout (%s)", startupTimeout)
		case <-ticker.C:
		}
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func randomPassword() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (e *Engine) doJSON(ctx context.Context, method, path string, in, out any) error {
	conn, err := e.ensure(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, conn.baseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if conn.password != "" {
		req.SetBasicAuth(conn.username, conn.password)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("opencode: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("opencode: cannot decode response: %w", err)
	}
	return nil
}

func apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var body struct {
		Tag          string `json:"_tag"`
		Message      string `json:"message"`
		Reason       string `json:"reason"`
		Status       int    `json:"status"`
		RetryAfterMs int64  `json:"retryAfterMs"`
	}
	_ = json.Unmarshal(data, &body)
	status := body.Status
	if status == 0 {
		status = resp.StatusCode
	}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	if message == "" {
		message = resp.Status
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 300 {
		message = message[:300] + "..."
	}
	label := fmt.Sprintf("opencode: %d", status)
	if body.Reason != "" {
		label += " " + body.Reason
	}
	if body.Tag != "" {
		label += " " + body.Tag
	}
	failure := &backend.HTTPError{Status: status, Message: label + ": " + message}
	switch {
	case status == http.StatusTooManyRequests || body.Reason == "rate_limit":
		return fmt.Errorf("quota: %w", failure)
	case status == http.StatusUnauthorized || status == http.StatusForbidden || body.Reason == "authentication":
		return fmt.Errorf("unauthorized: %w", failure)
	case status == http.StatusServiceUnavailable || body.Reason == "overloaded":
		return fmt.Errorf("service unavailable: %w", failure)
	case resp.StatusCode == http.StatusBadGateway && body.Reason == "provider":
		return fmt.Errorf("bad gateway: %w", failure)
	}
	return failure
}

type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > logTailLimit {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-logTailLimit:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(string(w.buf))
}
