package main

import (
	"context"
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

	"seavxrouter/internal/agy"
	"seavxrouter/internal/antigravity"
	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/codebuddy"
	"seavxrouter/internal/codex"
	"seavxrouter/internal/httpapi"
	"seavxrouter/internal/multi"
	"seavxrouter/internal/qoder"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	loginMode := len(os.Args) > 1 && os.Args[1] == "login"
	loginProvider := "antigravity"
	loginRegion := ""
	args := os.Args[1:]
	if loginMode {
		args = os.Args[2:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			loginProvider = args[0]
			args = args[1:]
		}
		if loginProvider == "qoder" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			loginRegion = args[0]
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("seavxrouter", flag.ExitOnError)
	listen := fs.String("listen", envOr("SEAVX_LISTEN", "127.0.0.1:8080"), "listen address")
	backendName := fs.String("backend", envOr("SEAVX_BACKEND", "native"), "backend: native (direct API) or binary (spawn agy)")
	authFile := fs.String("auth-file", envOr("SEAVX_AUTH_FILE", auth.DefaultPath()), "credentials file for the native backend")
	apiBase := fs.String("api-base", envOr("SEAVX_API_BASE", antigravity.DefaultAPIBase), "Antigravity API base URL")
	binName := fs.String("agy", envOr("SEAVX_AGY_BIN", "agy"), "agy binary name or path (binary backend)")
	apiKey := fs.String("api-key", os.Getenv("SEAVX_API_KEY"), "require this bearer token on /v1/* (empty = no auth)")
	defaultModel := fs.String("default-model", envOr("SEAVX_DEFAULT_MODEL", ""), "model used when the request omits one")
	usageDB := fs.String("usage-db", envOr("SEAVX_USAGE_DB", httpapi.DefaultUsageDBPath()), `SQLite file for token usage tracking (empty or "off" disables)`)
	timeout := fs.Duration("timeout", 5*time.Minute, "per-request backend timeout (binary backend)")
	workRoot := fs.String("workdir", envOr("SEAVX_WORKDIR", filepath.Join(os.TempDir(), "seavxrouter")), "scratch dir for backend workspaces (binary backend)")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if loginMode {
		store := auth.AuthStore{Path: *authFile}
		var err error
		switch loginProvider {
		case "antigravity":
			engine := &antigravity.Engine{Store: store, BaseURL: *apiBase, HTTP: &http.Client{}}
			err = engine.Login(ctx)
		case "codex":
			engine := &codex.CodexEngine{Store: store, HTTP: &http.Client{}}
			err = engine.Login(ctx)
		case "codebuddy":
			engine := &codebuddy.CodeBuddyEngine{Store: store, HTTP: &http.Client{}}
			err = engine.Login(ctx)
		case "qoder":
			engine := &qoder.QoderEngine{Store: store, HTTP: &http.Client{}, Region: loginRegion}
			err = engine.Login(ctx)
		default:
			slog.Error("unknown login provider (want antigravity, codex, codebuddy or qoder)", "provider", loginProvider)
			os.Exit(1)
		}
		if err != nil {
			slog.Error("login failed", "err", err)
			os.Exit(1)
		}
		return
	}

	var provider backend.BackendAPI
	switch *backendName {
	case "native":
		store := auth.AuthStore{Path: *authFile}
		auths, err := store.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot load credentials (%v)\nRun: seavxrouter login [antigravity|codex|codebuddy|qoder]\n", err)
			os.Exit(1)
		}
		var agyAuths, codexAuths, cbAuths, qAuths []*auth.Auth
		for _, a := range auths {
			switch a.Provider {
			case "codex":
				codexAuths = append(codexAuths, a)
			case "codebuddy":
				cbAuths = append(cbAuths, a)
			case "qoder":
				qAuths = append(qAuths, a)
			default:
				agyAuths = append(agyAuths, a)
			}
		}
		multi := &multi.MultiBackend{}
		if len(agyAuths) > 0 {
			engine, err := antigravity.NewEngine(store, *apiBase, agyAuths)
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
			multi.Antigravity = engine
		}
		if len(codexAuths) > 0 {
			engine, err := codex.NewCodexEngine(store, codexAuths)
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
			multi.Codex = engine
		}
		if len(cbAuths) > 0 {
			engine, err := codebuddy.NewCodeBuddyEngine(store, cbAuths)
			if err != nil {
				slog.Error("cannot start codebuddy", "err", err)
				os.Exit(1)
			}
			engine.DefaultModel = *defaultModel
			if engine.DefaultModel == "" {
				engine.DefaultModel = engine.Default()
			}
			multi.CodeBuddy = engine
		}
		if len(qAuths) > 0 {
			engine, err := qoder.NewQoderEngine(store, qAuths)
			if err != nil {
				slog.Error("cannot start qoder", "err", err)
				os.Exit(1)
			}
			engine.DefaultModel = *defaultModel
			if engine.DefaultModel == "" {
				engine.DefaultModel = engine.Default()
			}
			multi.Qoder = engine
		}
		slog.Info("providers ready", "antigravity", len(agyAuths), "codex", len(codexAuths), "codebuddy", len(cbAuths), "qoder", len(qAuths))
		provider = multi
	case "binary":
		bin, err := agy.ResolveBinary(*binName)
		if err != nil {
			slog.Error("cannot start", "err", err)
			os.Exit(1)
		}
		if err := os.MkdirAll(*workRoot, 0o700); err != nil {
			slog.Error("cannot create workdir", "err", err)
			os.Exit(1)
		}
		provider = &agy.Backend{Bin: bin, WorkRoot: *workRoot, Timeout: *timeout, DefaultModel: *defaultModel}
	default:
		slog.Error("unknown backend", "backend", *backendName)
		os.Exit(1)
	}

	var usageStore *httpapi.UsageStore
	switch *usageDB {
	case "", "off", "none":
		slog.Info("usage tracking disabled")
	default:
		store, err := httpapi.OpenUsageStore(*usageDB)
		if err != nil {
			slog.Error("cannot open usage db", "err", err)
			os.Exit(1)
		}
		defer store.Close()
		usageStore = store
		slog.Info("usage tracking enabled", "db", *usageDB)
	}

	srv := httpapi.NewServer(provider, *apiKey, usageStore)

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
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
