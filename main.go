package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type BackendAPI interface {
	Models(context.Context) ([]Model, error)
	HasModel(context.Context, string) bool
	Complete(context.Context, string, string) (*Result, error)
	Stream(context.Context, string, string, func(string)) (*Result, error)
	Chat(context.Context, *ChatRequest, func(ChatDelta)) (*ChatResult, error)
	Default() string
}

type Server struct {
	backend BackendAPI
	apiKey  string
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	loginMode := len(os.Args) > 1 && os.Args[1] == "login"
	loginProvider := "antigravity"
	args := os.Args[1:]
	if loginMode {
		args = os.Args[2:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			loginProvider = args[0]
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("seavxrouter", flag.ExitOnError)
	listen := fs.String("listen", envOr("SEAVX_LISTEN", "127.0.0.1:8080"), "listen address")
	backendName := fs.String("backend", envOr("SEAVX_BACKEND", "native"), "backend: native (direct API) or binary (spawn agy)")
	authFile := fs.String("auth-file", envOr("SEAVX_AUTH_FILE", defaultAuthPath()), "credentials file for the native backend")
	apiBase := fs.String("api-base", envOr("SEAVX_API_BASE", defaultAPIBase), "Antigravity API base URL")
	binName := fs.String("agy", envOr("SEAVX_AGY_BIN", "agy"), "agy binary name or path (binary backend)")
	apiKey := fs.String("api-key", os.Getenv("SEAVX_API_KEY"), "require this bearer token on /v1/* (empty = no auth)")
	defaultModel := fs.String("default-model", envOr("SEAVX_DEFAULT_MODEL", ""), "model used when the request omits one")
	timeout := fs.Duration("timeout", 5*time.Minute, "per-request backend timeout (binary backend)")
	workRoot := fs.String("workdir", envOr("SEAVX_WORKDIR", filepath.Join(os.TempDir(), "seavxrouter")), "scratch dir for backend workspaces (binary backend)")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if loginMode {
		store := AuthStore{Path: *authFile}
		var err error
		switch loginProvider {
		case "antigravity":
			engine := &Engine{Store: store, BaseURL: *apiBase, HTTP: &http.Client{}}
			err = engine.Login(ctx)
		case "codex":
			engine := &CodexEngine{Store: store, HTTP: &http.Client{}}
			err = engine.Login(ctx)
		default:
			slog.Error("unknown login provider (want antigravity or codex)", "provider", loginProvider)
			os.Exit(1)
		}
		if err != nil {
			slog.Error("login failed", "err", err)
			os.Exit(1)
		}
		return
	}

	var backend BackendAPI
	switch *backendName {
	case "native":
		store := AuthStore{Path: *authFile}
		auths, err := store.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot load credentials (%v)\nRun: seavxrouter login [codex]\n", err)
			os.Exit(1)
		}
		var agyAuths, codexAuths []*Auth
		for _, a := range auths {
			if a.Provider == "codex" {
				codexAuths = append(codexAuths, a)
			} else {
				agyAuths = append(agyAuths, a)
			}
		}
		multi := &MultiBackend{}
		if len(agyAuths) > 0 {
			engine, err := NewEngine(store, *apiBase, agyAuths)
			if err != nil {
				slog.Error("cannot start antigravity", "err", err)
				os.Exit(1)
			}
			engine.DefaultModel = *defaultModel
			if engine.DefaultModel == "" {
				if models, err := engine.Models(ctx); err == nil {
					engine.DefaultModel = models[0].ID
				} else {
					slog.Warn("could not fetch default model", "provider", "antigravity", "err", err)
				}
			}
			multi.antigravity = engine
		}
		if len(codexAuths) > 0 {
			engine, err := NewCodexEngine(store, codexAuths)
			if err != nil {
				slog.Error("cannot start codex", "err", err)
				os.Exit(1)
			}
			engine.DefaultModel = *defaultModel
			if engine.DefaultModel == "" {
				if models, err := engine.Models(ctx); err == nil {
					engine.DefaultModel = models[0].ID
				} else {
					slog.Warn("could not fetch default model", "provider", "codex", "err", err)
				}
			}
			multi.codex = engine
		}
		slog.Info("providers ready", "antigravity", len(agyAuths), "codex", len(codexAuths))
		backend = multi
	case "binary":
		bin, err := ResolveBinary(*binName)
		if err != nil {
			slog.Error("cannot start", "err", err)
			os.Exit(1)
		}
		if err := os.MkdirAll(*workRoot, 0o700); err != nil {
			slog.Error("cannot create workdir", "err", err)
			os.Exit(1)
		}
		backend = &Backend{Bin: bin, WorkRoot: *workRoot, Timeout: *timeout, DefaultModel: *defaultModel}
	default:
		slog.Error("unknown backend", "backend", *backendName)
		os.Exit(1)
	}

	srv := &Server{backend: backend, apiKey: *apiKey}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", srv.auth(srv.handleModels))
	mux.HandleFunc("POST /v1/chat/completions", srv.auth(srv.handleChat))
	mux.HandleFunc("POST /v1/messages", srv.auth(srv.handleAnthropicMessages))
	mux.HandleFunc("POST /v1/responses", srv.auth(srv.handleResponses))

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	go func() {
		slog.Info("seavxrouter listening", "addr", *listen, "backend", *backendName, "auth", *apiKey != "")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	slog.Info("stopped")
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
