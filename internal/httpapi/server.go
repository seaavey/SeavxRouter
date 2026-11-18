package httpapi

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/completions"
	"seavxrouter/internal/compress"
)

// Server exposes OpenAI-, Anthropic- and Responses-compatible HTTP endpoints
// backed by a single backend. Wire-format translation lives in the completions
// package; this type owns routing, auth, usage storage and request logging.
type Server struct {
	backend    backend.BackendAPI
	apiKey     string
	keys       *KeyStore
	usage      *UsageStore
	logs       *logBroker
	compressor *compress.Engine
	pricing    atomic.Pointer[Pricing]
	wire       *completions.Server
	hidden     *HiddenModels
}

// ServerOption customizes a Server built by NewServer.
type ServerOption func(*Server)

// WithCompression installs the tool-output compression engine.
func WithCompression(e *compress.Engine) ServerOption {
	return func(s *Server) { s.compressor = e }
}

// WithoutCompression disables compression entirely.
func WithoutCompression() ServerOption {
	return func(s *Server) { s.compressor = nil }
}

// WithPricing overrides the model price table used for cost tracking.
func WithPricing(p *Pricing) ServerOption {
	return func(s *Server) { s.pricing.Store(p) }
}

// WithHiddenModelsFile replaces the blocklist file that is otherwise loaded
// from the credentials directory.
func WithHiddenModelsFile(path string) ServerOption {
	return func(s *Server) { s.hidden = NewHiddenModels(path) }
}

// WithKeys enables tenant API keys and the /v1/keys management endpoints.
func WithKeys(ks *KeyStore) ServerOption {
	return func(s *Server) { s.keys = ks }
}

func NewServer(b backend.BackendAPI, apiKey string, usage *UsageStore, opts ...ServerOption) *Server {
	s := &Server{backend: b, apiKey: apiKey, usage: usage, logs: newLogBroker(), compressor: compress.NewDefault()}
	s.pricing.Store(DefaultPricing())
	for _, opt := range opts {
		opt(s)
	}
	if s.hidden == nil {
		s.hidden = NewHiddenModels(hiddenModelsFile(""))
	}
	s.backend = WithHiddenModels(b, s.hidden)
	s.wire = completions.New(s.backend,
		completions.WithCompression(func(r *http.Request, cr *backend.ChatRequest, endpoint string) {
			s.applyCompression(cr, endpoint)
		}),
		completions.WithUsage(s),
		completions.WithUserResolver(func(ctx context.Context) string {
			if r, _ := ctx.Value(userKey{}).(*http.Request); r != nil {
				return usageUser(r)
			}
			return "unknown"
		}),
		completions.WithLogAccessor(func(ctx context.Context) completions.LogEntry {
			entry := logFromContext(ctx)
			if entry == nil {
				return nil
			}
			return entry
		}),
	)
	return s
}

// ownerOf reports which provider serves a model ID, including provider-prefixed
// IDs; bare IDs fall back to antigravity for backends that cannot name one.
func (s *Server) ownerOf(ctx context.Context, id string) string {
	if pn, ok := s.backend.(completions.ProviderNamer); ok {
		if name := pn.Provider(ctx, id); name != "" {
			return name
		}
	}
	return "antigravity"
}

// SetPricing swaps the price table used for usage recorded from now on. It is
// safe to call while the server is serving requests.
func (s *Server) SetPricing(p *Pricing) {
	s.pricing.Store(p)
}

// RecordUsage persists one completed request's token usage. It satisfies
// completions.UsageRecorder, which reports already-translated fields rather than
// a *backend.ChatResult.
func (s *Server) RecordUsage(endpoint, model, provider, user string, u backend.Usage, status int) {
	if s.usage == nil {
		return
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	e := UsageEntry{
		Provider:         provider,
		Model:            model,
		Endpoint:         endpoint,
		User:             user,
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		ThinkingTokens:   u.ThinkingTokens,
		CachedTokens:     u.CacheReadTokens,
		TotalTokens:      total,
		CostUSD:          s.pricing.Load().Cost(model, u),
	}
	if err := s.usage.Record(e); err != nil {
		slog.Warn("usage record failed", "endpoint", endpoint, "model", model, "err", err)
	}
}

// Handler returns the HTTP router with all /v1 routes and API-key auth.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", s.auth(s.wire.HandleModels))
	mux.HandleFunc("GET /v1/models/{id...}", s.auth(s.wire.HandleModel))
	mux.HandleFunc("GET /v1/quota", s.auth(s.requireMaster(s.handleQuota)))
	mux.HandleFunc("GET /v1/usage", s.auth(s.requireMaster(s.handleUsage)))
	mux.HandleFunc("GET /v1/compression", s.auth(s.requireMaster(s.handleCompression)))
	mux.HandleFunc("GET /v1/logging", s.auth(s.requireMaster(s.handleLogging)))
	mux.HandleFunc("GET /v1/logging/stream", s.auth(s.requireMaster(s.handleLogStream)))
	mux.HandleFunc("GET /v1/logging/{id}", s.auth(s.requireMaster(s.handleLog)))
	mux.HandleFunc("POST /v1/chat/completions", s.auth(s.limits(s.logged("chat", s.wire.HandleChat))))
	mux.HandleFunc("POST /v1/messages", s.auth(s.limits(s.logged("messages", s.wire.HandleAnthropicMessages))))
	mux.HandleFunc("POST /v1/responses", s.auth(s.limits(s.logged("responses", s.wire.HandleResponses))))
	if s.keys != nil {
		mux.HandleFunc("GET /v1/keys", s.auth(s.requireKeysAdmin(s.handleListKeys)))
		mux.HandleFunc("POST /v1/keys", s.auth(s.requireKeysAdmin(s.handleCreateKey)))
		mux.HandleFunc("GET /v1/keys/{id}", s.auth(s.requireKeysAdmin(s.handleGetKey)))
		mux.HandleFunc("PATCH /v1/keys/{id}", s.auth(s.requireKeysAdmin(s.handlePatchKey)))
		mux.HandleFunc("DELETE /v1/keys/{id}", s.auth(s.requireKeysAdmin(s.handleDeleteKey)))
	}
	return mux
}

// auth validates the bearer token. The master key stays valid on every route;
// a tenant key is attached to the request context so operator routes can
// reject it and usage attribution can name the customer.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey != "" {
			token, ok := bearerToken(r)
			if !ok {
				writeError(w, &apiError{http.StatusUnauthorized, "invalid or missing API key", "invalid_request_error", "invalid_api_key"})
				return
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.apiKey)) != 1 {
				if s.keys == nil {
					writeError(w, &apiError{http.StatusUnauthorized, "invalid or missing API key", "invalid_request_error", "invalid_api_key"})
					return
				}
				key, err := s.keys.Lookup(token)
				if err != nil {
					slog.Error("api key lookup failed", "err", err)
					writeError(w, &apiError{http.StatusInternalServerError, "api key lookup failed", "server_error", "key_store_error"})
					return
				}
				if key == nil {
					writeError(w, &apiError{http.StatusUnauthorized, "invalid or missing API key", "invalid_request_error", "invalid_api_key"})
					return
				}
				r = r.WithContext(withTenantKey(r.Context(), key))
			}
		}
		start := time.Now()
		next(w, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
	}
}

func bearerToken(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return "", false
	}
	return token, true
}

// requireMaster keeps operator routes (usage, logs, quotas) on the master key.
// With no master key configured the server stays open, as before.
func (s *Server) requireMaster(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tenantKeyFromContext(r.Context()) != nil {
			writeError(w, &apiError{http.StatusForbidden, "admin API key required", "invalid_request_error", "admin_key_required"})
			return
		}
		next(w, r)
	}
}

// requireKeysAdmin gates key management: it needs -api-key so an open server
// can never mint keys that would activate later.
func (s *Server) requireKeysAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" {
			writeError(w, &apiError{http.StatusForbidden, "API key management requires -api-key", "invalid_request_error", "key_management_disabled"})
			return
		}
		if tenantKeyFromContext(r.Context()) != nil {
			writeError(w, &apiError{http.StatusForbidden, "admin API key required", "invalid_request_error", "admin_key_required"})
			return
		}
		next(w, r)
	}
}

// limits rejects inference requests from tenant keys that exhausted their
// monthly USD budget or token cap. Master keys and unlimited keys pass.
//
// The spend lookup is deliberately detached from the request context: a client
// that disconnects mid-check must not turn a cancelled query into a free pass.
// If the lookup still fails, limited tenants get 503 rather than a silent
// fail-open, because these caps are a billing control.
func (s *Server) limits(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := tenantKeyFromContext(r.Context())
		if key == nil || s.usage == nil || (key.BudgetUSD <= 0 && key.LimitToken <= 0) {
			next(w, r)
			return
		}
		spent, tokens, err := s.usage.SpendSince(context.WithoutCancel(r.Context()), tenantUser(key.Name), monthStart(time.Now()))
		if err != nil {
			slog.Error("limit check failed, rejecting request", "key", key.Name, "err", err)
			writeError(w, &apiError{
				http.StatusServiceUnavailable,
				"cannot verify usage for this API key right now, retry shortly",
				"server_error",
				"limit_check_failed",
			})
			return
		}
		switch {
		case key.BudgetUSD > 0 && spent >= key.BudgetUSD:
			writeError(w, &apiError{
				http.StatusTooManyRequests,
				fmt.Sprintf("monthly budget of $%.2f exhausted for this API key", key.BudgetUSD),
				"insufficient_quota",
				"insufficient_quota",
			})
			return
		case key.LimitToken > 0 && tokens >= key.LimitToken:
			writeError(w, &apiError{
				http.StatusTooManyRequests,
				fmt.Sprintf("monthly token limit of %d exhausted for this API key", key.LimitToken),
				"insufficient_quota",
				"insufficient_quota",
			})
			return
		}
		next(w, r)
	}
}
