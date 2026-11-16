// Package completions serves the OpenAI- and Anthropic-compatible completion
// endpoints (/v1/chat/completions, /v1/responses, /v1/messages) on top of a
// provider-neutral backend. Routing, auth and usage storage stay in httpapi;
// this package only owns request/response translation and streaming.
package completions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"seavxrouter/internal/backend"
)

// UsageRecorder persists token usage for one completed request.
type UsageRecorder interface {
	RecordUsage(endpoint, model, provider, user string, usage backend.Usage, status int)
}

// PriceTable prices a model in USD per 1M tokens.
type PriceTable interface {
	Price(model string) (input, output float64, ok bool)
}

// LogEntry is the in-flight request log record the handlers annotate.
type LogEntry interface {
	SetModel(model, provider string)
	SetResult(content string, toolCalls []backend.ToolCall, finishReason string)
	SetUsage(u backend.Usage)
	SetError(status int, message string)
}

// LogAccessor returns the in-flight log entry attached to a request context, or
// nil when request logging is disabled.
type LogAccessor func(ctx context.Context) LogEntry

// UserResolver names the caller a request is attributed to.
type UserResolver func(ctx context.Context) string

// userFromCtx is the installed resolver; it defaults to an empty name so tests
// and embedders that never set one still record usage.
var userFromCtx UserResolver = func(context.Context) string { return "unknown" }

// WithUserResolver installs the caller-attribution resolver.
func WithUserResolver(f UserResolver) Option {
	return func(s *Server) {
		if f != nil {
			userFromCtx = f
		}
	}
}

// UserFromContext reports the caller the installed resolver attributes ctx to.
func UserFromContext(ctx context.Context) string {
	if userFromCtx == nil {
		return "unknown"
	}
	return userFromCtx(ctx)
}

// CompressFunc rewrites oversized tool results in a request and reports the
// saving to whatever recorder the host owns. Returning false means nothing was
// compressed.
type CompressFunc func(r *http.Request, cr *backend.ChatRequest, endpoint string)

// Server translates wire formats. It holds no HTTP routes of its own.
type Server struct {
	backend    backend.BackendAPI
	compress   CompressFunc
	pricing    PriceTable
	usage      UsageRecorder
	logFromCtx LogAccessor
}

// Option customizes a Server.
type Option func(*Server)

// WithCompression installs the tool-output compression hook.
func WithCompression(f CompressFunc) Option {
	return func(s *Server) { s.compress = f }
}

// WithPricing sets the model price table used for cost reporting.
func WithPricing(p PriceTable) Option {
	return func(s *Server) { s.pricing = p }
}

// WithUsage attaches the token-usage recorder.
func WithUsage(u UsageRecorder) Option {
	return func(s *Server) { s.usage = u }
}

// WithLogAccessor attaches the request-log accessor, which lets handlers report
// upstream failures on an already-streamed response.
func WithLogAccessor(f LogAccessor) Option {
	return func(s *Server) { s.logFromCtx = f }
}

// New builds a Server for one backend.
func New(b backend.BackendAPI, opts ...Option) *Server {
	s := &Server{backend: b}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type apiError struct {
	status int
	msg    string
	typ    string
	code   string
}

func (e *apiError) Error() string { return e.msg }

func writeError(w http.ResponseWriter, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = upstreamError(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": ae.msg, "type": ae.typ, "code": ae.code},
	})
}

// upstreamError classifies a provider failure. A quota rejection surfaces only
// after the provider rotated through every account, so it means "nothing left
// right now": return 429 so clients back off or switch models instead of
// treating it as a gateway fault.
func upstreamError(err error) *apiError {
	if backend.IsQuotaError(err) {
		return &apiError{http.StatusTooManyRequests, err.Error(), "insufficient_quota", "insufficient_quota"}
	}
	return &apiError{http.StatusBadGateway, err.Error(), "backend_error", "backend_error"}
}

// failureStatus is the status recorded when a failure arrives after the
// streaming response has already sent its 200 headers.
func failureStatus(err error) int {
	if backend.IsQuotaError(err) {
		return http.StatusTooManyRequests
	}
	return backend.StatusCode(err)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ownerOf reports which provider serves a model ID, including provider-prefixed
// IDs. It returns "" when the backend cannot say.
func (s *Server) ownerOf(ctx context.Context, id string) string {
	if pn, ok := s.backend.(ProviderNamer); ok {
		if name := pn.Provider(ctx, id); name != "" {
			return name
		}
	}
	// A prefixed ID names its own provider even when the backend cannot resolve
	// it (for example a catalog entry that vanished between list and lookup).
	if name, _, ok := strings.Cut(id, "/"); ok && name != "" {
		return name
	}
	return fallbackProvider
}

// fallbackProvider labels models from backends that cannot name a provider,
// matching httpapi's historical default.
const fallbackProvider = "antigravity"

// ProviderNamer lets a backend report which provider serves a model ID.
type ProviderNamer interface {
	Provider(context.Context, string) string
}

func (s *Server) resolveModel(w http.ResponseWriter, r *http.Request, requested string) (string, bool) {
	model := requested
	if model == "" || model == "auto" || model == "default" {
		model = s.backend.Default()
	}
	if model != "" && !s.backend.HasModel(r.Context(), model) {
		writeError(w, &apiError{http.StatusNotFound, "model not found: " + model, "invalid_request_error", "model_not_found"})
		return "", false
	}
	return model, true
}

// logFailure records a backend failure on the in-flight request log. Streaming
// handlers call it after the 200 header was already sent, so the log reports
// the real upstream status instead of a fake success.
func (s *Server) logFailure(r *http.Request, status int, err error) {
	if s.logFromCtx == nil {
		return
	}
	entry := s.logFromCtx(r.Context())
	if entry == nil {
		return
	}
	entry.SetError(status, err.Error())
}

// applyCompression rewrites oversized tool results through the host hook.
func (s *Server) applyCompression(r *http.Request, cr *backend.ChatRequest, endpoint string) {
	if s.compress == nil {
		return
	}
	s.compress(r, cr, endpoint)
}

// recordResult reports a completed request to the usage recorder and the
// in-flight log entry.
func (s *Server) recordResult(r *http.Request, endpoint, model string, result *backend.ChatResult) {
	provider := s.ownerOf(r.Context(), model)
	if s.logFromCtx != nil {
		if entry := s.logFromCtx(r.Context()); entry != nil {
			entry.SetModel(model, provider)
			entry.SetUsage(result.Usage)
			entry.SetResult(result.Content, result.ToolCalls, result.FinishReason)
		}
	}
	if s.usage != nil {
		s.usage.RecordUsage(endpoint, model, provider, UserFromContext(r.Context()), result.Usage, http.StatusOK)
	}
}
