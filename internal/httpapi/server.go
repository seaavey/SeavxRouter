package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
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
	usage      *UsageStore
	logs       *logBroker
	compressor *compress.Engine
	pricing    *Pricing
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
	return func(s *Server) { s.pricing = p }
}

// WithHiddenModelsFile replaces the blocklist file that is otherwise loaded
// from the credentials directory.
func WithHiddenModelsFile(path string) ServerOption {
	return func(s *Server) { s.hidden = NewHiddenModels(path) }
}

func NewServer(b backend.BackendAPI, apiKey string, usage *UsageStore, opts ...ServerOption) *Server {
	s := &Server{backend: b, apiKey: apiKey, usage: usage, logs: newLogBroker(), compressor: compress.NewDefault(), pricing: DefaultPricing()}
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
		CostUSD:          s.pricing.Cost(model, u),
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
	mux.HandleFunc("GET /v1/quota", s.auth(s.handleQuota))
	mux.HandleFunc("GET /v1/usage", s.auth(s.handleUsage))
	mux.HandleFunc("GET /v1/compression", s.auth(s.handleCompression))
	mux.HandleFunc("GET /v1/logging", s.auth(s.handleLogging))
	mux.HandleFunc("GET /v1/logging/stream", s.auth(s.handleLogStream))
	mux.HandleFunc("GET /v1/logging/{id}", s.auth(s.handleLog))
	mux.HandleFunc("POST /v1/chat/completions", s.auth(s.logged("chat", s.wire.HandleChat)))
	mux.HandleFunc("POST /v1/messages", s.auth(s.logged("messages", s.wire.HandleAnthropicMessages)))
	mux.HandleFunc("POST /v1/responses", s.auth(s.logged("responses", s.wire.HandleResponses)))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey != "" {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.apiKey)) != 1 {
				writeError(w, &apiError{http.StatusUnauthorized, "invalid or missing API key", "invalid_request_error", "invalid_api_key"})
				return
			}
		}
		start := time.Now()
		next(w, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
	}
}
