// Package kiro implements the Kiro (AWS CodeWhisperer) chat backend.
package kiro

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

const (
	kiroDefaultRegion       = "us-east-1"
	kiroRuntimeHostTemplate = "https://runtime.%s.kiro.dev"
	kiroOIDCHostTemplate    = "https://oidc.%s.amazonaws.com"
	kiroDesktopHostTemplate = "https://prod.%s.auth.desktop.kiro.dev"
	kiroStartURL            = "https://view.awsapps.com/start"
	kiroTarget              = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	kiroDefaultModel        = "auto"
	kiroMaxInputTokens      = 200000
)

// kiroCatalog mirrors the model IDs the Kiro runtime accepts; the runtime has
// no list endpoint, so this is a static fallback list.
var kiroCatalog = []string{
	"auto",
	"claude-sonnet-4",
	"claude-sonnet-4.5",
	"claude-sonnet-4.6",
	"claude-haiku-4.5",
	"claude-opus-4.5",
	"claude-opus-4.6",
	"claude-opus-4.7",
	"deepseek-3.2",
	"glm-5",
	"minimax-m2.1",
	"minimax-m2.5",
	"qwen3-coder-next",
}

var (
	kiroContextSuffixRe  = regexp.MustCompile(`(?i)\[\d+[mk]\]$`)
	kiroVersionedModelRe = regexp.MustCompile(`^(claude-(?:haiku|sonnet|opus)-\d+)-(\d{1,2})(?:-(?:\d{8}|latest|\d+|thinking|high|low|medium))?$`)
	kiroPlainModelRe     = regexp.MustCompile(`^(claude-(?:haiku|sonnet|opus)-\d+)(?:-\d{8})?$`)
	kiroLegacyModelRe    = regexp.MustCompile(`^claude-(\d+)-(\d+)-(haiku|sonnet|opus)(?:-(?:\d{8}|latest|\d+))?$`)
	kiroDatedModelRe     = regexp.MustCompile(`^(claude-(?:\d+\.\d+-)?(?:haiku|sonnet|opus)(?:-\d+\.\d+)?)-\d{8}$`)
	kiroInvertedModelRe  = regexp.MustCompile(`^claude-(\d+)\.(\d+)-(haiku|sonnet|opus)(?:-.+)?$`)
)

// Engine talks to the Kiro runtime API using AWS SSO OIDC (Builder ID) or Kiro
// Desktop credentials.
type Engine struct {
	Store        auth.AuthStore
	HTTP         *http.Client
	DefaultModel string

	// BaseURL, OIDCBase and AuthBase override the runtime, SSO OIDC and desktop
	// auth hosts; tests point them at httptest servers.
	BaseURL      string
	OIDCBase     string
	AuthBase     string
	StartURL     string
	Region       string
	PollInterval time.Duration

	mu    sync.Mutex
	auths []*auth.Auth
	cache auth.AccountCache
	next  int

	quotaMu  sync.Mutex
	quotaErr map[*auth.Auth]time.Time
}

var _ backend.BackendAPI = (*Engine)(nil)

// NewKiroEngine builds an engine over stored Kiro accounts.
func NewKiroEngine(store auth.AuthStore, auths []*auth.Auth) (*Engine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no kiro accounts")
	}
	return &Engine{Store: store, HTTP: &http.Client{}, auths: auths, cache: auth.NewAccountCache(store, "kiro")}, nil
}

func (e *Engine) Default() string {
	if e.DefaultModel != "" {
		return ModelID(e.DefaultModel)
	}
	return kiroDefaultModel
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

func (e *Engine) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

// ModelID normalizes client-supplied model names to the IDs the Kiro runtime
// accepts: dotted Claude version, no context-window suffix.
func ModelID(name string) string {
	name = strings.TrimSpace(kiroContextSuffixRe.ReplaceAllString(strings.TrimSpace(name), ""))
	if name == "" {
		return ""
	}
	lower := strings.ToLower(name)
	switch lower {
	case "auto-kiro", "kiro-auto":
		return "auto"
	}
	if m := kiroVersionedModelRe.FindStringSubmatch(lower); m != nil {
		return m[1] + "." + m[2]
	}
	if m := kiroPlainModelRe.FindStringSubmatch(lower); m != nil {
		return m[1]
	}
	if m := kiroLegacyModelRe.FindStringSubmatch(lower); m != nil {
		return "claude-" + m[1] + "." + m[2] + "-" + m[3]
	}
	if m := kiroDatedModelRe.FindStringSubmatch(lower); m != nil {
		return m[1]
	}
	if m := kiroInvertedModelRe.FindStringSubmatch(lower); m != nil {
		return "claude-" + m[3] + "-" + m[1] + "." + m[2]
	}
	return name
}

func (e *Engine) Models(context.Context) ([]backend.Model, error) {
	out := make([]backend.Model, 0, len(kiroCatalog))
	for _, id := range kiroCatalog {
		out = append(out, backend.Model{ID: id, Name: id})
	}
	return out, nil
}

func (e *Engine) HasModel(_ context.Context, id string) bool {
	id = ModelID(id)
	for _, m := range kiroCatalog {
		if m == id {
			return true
		}
	}
	return false
}

// Check verifies each account has usable credentials, refreshing only tokens
// that are about to expire; Kiro has no model-list endpoint, so the static
// catalog is reported on success.
func (e *Engine) Check(ctx context.Context) []backend.AccountCheck {
	auths := e.snapshotAuths()
	catalog, _ := e.Models(ctx)
	out := make([]backend.AccountCheck, 0, len(auths))
	for _, a := range auths {
		start := time.Now()
		_, err := e.token(ctx, a)
		check := backend.AccountCheck{
			Account:   a.Email,
			AccountID: a.AccountID,
			Err:       err,
			Latency:   time.Since(start),
		}
		if err == nil {
			check.Models = catalog
		}
		out = append(out, check)
	}
	return out
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
	model = ModelID(model)

	var lastErr error
	n := e.count()
	for i := 0; i < n; i++ {
		a := e.pick()
		data, err := buildKiroPayload(model, req, a.ProfileARN)
		if err != nil {
			return nil, err
		}
		result, started, err := e.chatOnce(ctx, a, data, onDelta)
		e.trackQuota(a, err)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || !backend.IsQuotaError(err) || ctx.Err() != nil {
			return nil, err
		}
		slog.Warn("kiro account quota exhausted, trying next", "email", a.Email)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no kiro accounts")
	}
	return nil, lastErr
}

func (e *Engine) chatOnce(ctx context.Context, a *auth.Auth, data []byte, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	started := false
	emit := func(d backend.ChatDelta) {
		if d.Content != "" || len(d.ToolCalls) > 0 {
			started = true
		}
		if onDelta != nil {
			onDelta(d)
		}
	}
	for attempt := 0; ; attempt++ {
		token, err := e.token(ctx, a)
		if err != nil {
			return nil, started, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiHost(a)+"/generateAssistantResponse", bytes.NewReader(data))
		if err != nil {
			return nil, started, err
		}
		kiroSetHeaders(req, token)
		resp, err := e.client().Do(req)
		if err != nil {
			return nil, started, err
		}
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && attempt == 0 {
			resp.Body.Close()
			// A stale token can surface as 403 on the runtime host; force one
			// refresh before giving up.
			e.mu.Lock()
			a.Expiry = time.Time{}
			e.mu.Unlock()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, started, kiroHTTPError(resp)
		}
		parser := &kiroStreamParser{}
		buf := make([]byte, 32*1024)
		var readErr error
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				parser.feed(buf[:n], emit)
			}
			if err != nil {
				readErr = err
				break
			}
		}
		resp.Body.Close()
		if ctx.Err() != nil {
			return nil, started, ctx.Err()
		}
		if readErr != nil && readErr != io.EOF {
			return nil, started, readErr
		}
		tools := parser.finish(emit)
		usage := estimateUsage(len(data), parser.content.String(), parser.contextPct)
		finish := "stop"
		if len(tools) > 0 {
			finish = "tool_calls"
		}
		return &backend.ChatResult{
			Content:      parser.content.String(),
			ToolCalls:    tools,
			Usage:        usage,
			FinishReason: finish,
		}, started, nil
	}
}

// estimateUsage approximates token counts: the runtime only reports context
// usage percentage, so input tokens derive from that when present.
func estimateUsage(promptBytes int, output string, contextPct float64) backend.Usage {
	out := len([]rune(output)) / 4
	if output != "" && out == 0 {
		out = 1
	}
	in := promptBytes / 4
	if contextPct > 0 {
		total := int(contextPct / 100 * kiroMaxInputTokens)
		if total > out {
			in = total - out
		} else {
			in = 0
		}
	}
	return backend.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
}

func (e *Engine) apiHost(a *auth.Auth) string {
	if e.BaseURL != "" {
		return strings.TrimRight(e.BaseURL, "/")
	}
	region := a.Region
	if region == "" {
		region = kiroDefaultRegion
	}
	return fmt.Sprintf(kiroRuntimeHostTemplate, region)
}

func kiroHTTPError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var body struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil {
		msg := body.Message
		if msg == "" {
			msg = body.Error
		}
		if msg != "" {
			return fmt.Errorf("%s: %s", resp.Status, msg)
		}
	}
	return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
}

var kiroFingerprint = sync.OnceValue(func() string {
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte(host + "-" + os.Getenv("USER") + "-seavxrouter"))
	return hex.EncodeToString(sum[:])
})

func kiroSetHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	req.Header.Set("x-amz-target", kiroTarget)
	req.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/win32#10.0.19044 lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-0.7.45-"+kiroFingerprint())
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroIDE-0.7.45-"+kiroFingerprint())
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("amz-sdk-invocation-id", backend.UUID())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")
}
