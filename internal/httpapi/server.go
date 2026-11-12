package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"seavxrouter/internal/backend"
)

// Server exposes OpenAI-, Anthropic- and Responses-compatible HTTP endpoints
// backed by a single backend.
type Server struct {
	backend backend.BackendAPI
	apiKey  string
	usage   *UsageStore
}

func NewServer(b backend.BackendAPI, apiKey string, usage *UsageStore) *Server {
	return &Server{backend: b, apiKey: apiKey, usage: usage}
}

// Handler returns the HTTP router with all /v1 routes and API-key auth.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", s.auth(s.handleModels))
	mux.HandleFunc("GET /v1/models/{id...}", s.auth(s.handleModel))
	mux.HandleFunc("GET /v1/quota", s.auth(s.handleQuota))
	mux.HandleFunc("GET /v1/usage", s.auth(s.handleUsage))
	mux.HandleFunc("POST /v1/chat/completions", s.auth(s.handleChat))
	mux.HandleFunc("POST /v1/messages", s.auth(s.handleAnthropicMessages))
	mux.HandleFunc("POST /v1/responses", s.auth(s.handleResponses))
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
