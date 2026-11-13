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
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"seavxrouter/internal/agy"
	"seavxrouter/internal/antigravity"
	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/codebuddy"
	"seavxrouter/internal/codex"
	"seavxrouter/internal/compress"
	"seavxrouter/internal/dashboard"
	"seavxrouter/internal/httpapi"
	"seavxrouter/internal/kiro"
	"seavxrouter/internal/multi"
	"seavxrouter/internal/qoder"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func compressionEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "none", "false", "0", "no":
		return false
	}
	return true
}

// localServerURL turns a listen address into a URL the tui can dial.
func localServerURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func serverStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".seavx", "server.url")
	}
	return filepath.Join(home, ".seavx", "server.url")
}

var probeClient = &http.Client{Timeout: 750 * time.Millisecond}

func serverAlive(url string) bool {
	resp, err := probeClient.Get(url + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// writeServerState records the running server's URL so `seavxrouter tui`
// finds it without flags, even when the server runs on a custom port.
func writeServerState(url string) {
	if url == "" {
		return
	}
	path := serverStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(url+"\n"), 0o600)
}

func clearServerState(url string) {
	path := serverStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) == url {
		_ = os.Remove(path)
	}
}

// defaultTUIServer is where the tui looks when no server URL was recorded.
const defaultTUIServer = "http://localhost:20128"

// resolveServer picks where the tui dials: an explicit -server flag (or
// SEAVX_SERVER) wins, then the live URL recorded by a running server, then
// the preferred default, then the address derived from -listen.
func resolveServer(listen, flagValue, statePath, preferred string) string {
	if flagValue != "" {
		return flagValue
	}
	if data, err := os.ReadFile(statePath); err == nil {
		if url := strings.TrimSpace(string(data)); url != "" && serverAlive(url) {
			return url
		}
	}
	if preferred != "" && serverAlive(preferred) {
		return preferred
	}
	return localServerURL(listen)
}

func main() {
	loginMode := len(os.Args) > 1 && os.Args[1] == "login"
	checkMode := len(os.Args) > 1 && os.Args[1] == "check"
	tuiMode := len(os.Args) > 1 && os.Args[1] == "tui"
	loginProvider := "antigravity"
	loginRegion := ""
	var checkProviders []string
	args := os.Args[1:]
	if loginMode {
		args = os.Args[2:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			loginProvider = args[0]
			args = args[1:]
		}
		if loginProvider == "qoder" || loginProvider == "kiro" {
			if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				loginRegion = args[0]
				args = args[1:]
			}
		}
	}
	if checkMode {
		args = os.Args[2:]
		for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			checkProviders = append(checkProviders, args[0])
			args = args[1:]
		}
	}
	if tuiMode {
		args = os.Args[2:]
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
	compressFlag := fs.String("compress", envOr("SEAVX_COMPRESS", "on"), "compress large tool-result output: on|off")
	compressMinBytes := fs.Int("compress-min-bytes", envInt("SEAVX_COMPRESS_MIN_BYTES", 2000), "minimum tool-result bytes to compress")
	pricingFile := fs.String("pricing", os.Getenv("SEAVX_PRICING"), "JSON file with model prices in USD per 1M tokens (empty = models.dev catalog)")
	refresh := fs.Duration("refresh", 2*time.Second, "tui auto-refresh interval")
	groupBy := fs.String("group-by", "day", "tui grouping: day, provider, model, user or endpoint")
	once := fs.Bool("once", false, "tui: print one snapshot and exit")
	serverURL := fs.String("server", os.Getenv("SEAVX_SERVER"), "tui: running server URL for SSE live logs (empty = auto-detect, then "+defaultTUIServer+"; off = disable)")
	timeout := fs.Duration("timeout", 5*time.Minute, "per-request backend timeout (binary backend)")
	workRoot := fs.String("workdir", envOr("SEAVX_WORKDIR", filepath.Join(os.TempDir(), "seavxrouter")), "scratch dir for backend workspaces (binary backend)")
	fs.Parse(args)
	if checkMode {
		checkProviders = append(checkProviders, fs.Args()...)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if checkMode {
		slog.SetLogLoggerLevel(slog.LevelWarn)
		os.Exit(runCheck(ctx, *authFile, *apiBase, checkProviders))
	}

	if tuiMode {
		server := resolveServer(*listen, *serverURL, serverStatePath(), defaultTUIServer)
		dash, err := dashboard.Open(dashboard.Options{
			UsageDB: *usageDB,
			Refresh: *refresh,
			GroupBy: *groupBy,
			Once:    *once,
			Server:  server,
			APIKey:  *apiKey,
		})
		if err != nil {
			slog.Error("cannot start tui", "err", err)
			os.Exit(1)
		}
		defer dash.Close()
		if err := dash.Run(ctx); err != nil {
			slog.Error("tui failed", "err", err)
			os.Exit(1)
		}
		return
	}

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
		case "kiro":
			engine := &kiro.Engine{Store: store, HTTP: &http.Client{}, Region: loginRegion}
			err = engine.Login(ctx)
		default:
			slog.Error("unknown login provider (want antigravity, codex, codebuddy, qoder or kiro)", "provider", loginProvider)
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
			fmt.Fprintf(os.Stderr, "cannot load credentials (%v)\nRun: seavxrouter login [antigravity|codex|codebuddy|qoder|kiro]\n", err)
			os.Exit(1)
		}
		accounts := groupAuths(auths)
		multi := &multi.MultiBackend{}
		if len(accounts.antigravity) > 0 {
			engine, err := antigravity.NewEngine(store, *apiBase, accounts.antigravity)
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
		if len(accounts.codex) > 0 {
			engine, err := codex.NewCodexEngine(store, accounts.codex)
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
		if len(accounts.codebuddy) > 0 {
			engine, err := codebuddy.NewCodeBuddyEngine(store, accounts.codebuddy)
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
		if len(accounts.qoder) > 0 {
			engine, err := qoder.NewQoderEngine(store, accounts.qoder)
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
		if len(accounts.kiro) > 0 {
			engine, err := kiro.NewKiroEngine(store, accounts.kiro)
			if err != nil {
				slog.Error("cannot start kiro", "err", err)
				os.Exit(1)
			}
			engine.DefaultModel = *defaultModel
			if engine.DefaultModel == "" {
				engine.DefaultModel = engine.Default()
			}
			multi.Kiro = engine
		}
		slog.Info("providers ready", "antigravity", len(accounts.antigravity), "codex", len(accounts.codex), "codebuddy", len(accounts.codebuddy), "qoder", len(accounts.qoder), "kiro", len(accounts.kiro))
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

	compressor := compress.New(compress.Options{Enabled: compressionEnabled(*compressFlag), MinBytes: *compressMinBytes})
	slog.Info("compression", "enabled", compressor.Enabled(), "min_bytes", *compressMinBytes)
	pricing := httpapi.DefaultPricing()
	if p, err := httpapi.ModelsDevPricing(""); err != nil {
		slog.Warn("models.dev pricing unavailable, using built-in defaults", "err", err)
	} else {
		pricing = p
		slog.Info("pricing loaded from models.dev", "cache", httpapi.DefaultPricingCachePath())
	}
	if *pricingFile != "" {
		p, err := pricing.MergeFile(*pricingFile)
		if err != nil {
			slog.Warn("cannot load pricing file, keeping current prices", "err", err)
		} else {
			pricing = p
			slog.Info("pricing overrides loaded", "file", *pricingFile)
		}
	}
	srv := httpapi.NewServer(provider, *apiKey, usageStore, httpapi.WithCompression(compressor), httpapi.WithPricing(pricing))

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("cannot listen", "addr", *listen, "err", err)
		os.Exit(1)
	}
	servedAt := localServerURL(*listen)
	writeServerState(servedAt)
	defer clearServerState(servedAt)

	go func() {
		slog.Info("seavxrouter listening", "addr", *listen, "backend", *backendName, "auth", *apiKey != "")
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

var providerNames = []string{"antigravity", "codex", "codebuddy", "qoder", "kiro"}

type providerAccounts struct {
	antigravity []*auth.Auth
	codex       []*auth.Auth
	codebuddy   []*auth.Auth
	qoder       []*auth.Auth
	kiro        []*auth.Auth
}

func groupAuths(auths []*auth.Auth) providerAccounts {
	var accounts providerAccounts
	for _, a := range auths {
		switch a.Provider {
		case "codex":
			accounts.codex = append(accounts.codex, a)
		case "codebuddy":
			accounts.codebuddy = append(accounts.codebuddy, a)
		case "qoder":
			accounts.qoder = append(accounts.qoder, a)
		case "kiro":
			accounts.kiro = append(accounts.kiro, a)
		default:
			accounts.antigravity = append(accounts.antigravity, a)
		}
	}
	return accounts
}

func (p providerAccounts) count(name string) int {
	switch name {
	case "antigravity":
		return len(p.antigravity)
	case "codex":
		return len(p.codex)
	case "codebuddy":
		return len(p.codebuddy)
	case "qoder":
		return len(p.qoder)
	case "kiro":
		return len(p.kiro)
	}
	return 0
}

func isProvider(name string) bool {
	for _, p := range providerNames {
		if p == name {
			return true
		}
	}
	return false
}

func newChecker(name string, accounts providerAccounts, store auth.AuthStore, apiBase string) (backend.Checker, error) {
	switch name {
	case "antigravity":
		return antigravity.NewEngine(store, apiBase, accounts.antigravity)
	case "codex":
		return codex.NewCodexEngine(store, accounts.codex)
	case "codebuddy":
		return codebuddy.NewCodeBuddyEngine(store, accounts.codebuddy)
	case "qoder":
		return qoder.NewQoderEngine(store, accounts.qoder)
	case "kiro":
		return kiro.NewKiroEngine(store, accounts.kiro)
	}
	return nil, fmt.Errorf("unknown provider %q", name)
}

// checkTimeout bounds one provider's live credential and model check.
const checkTimeout = 45 * time.Second

func runCheck(ctx context.Context, authFile, apiBase string, providers []string) int {
	store := auth.AuthStore{Path: authFile}
	auths, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot load credentials (%v)\nRun: seavxrouter login [antigravity|codex|codebuddy|qoder|kiro]\n", err)
		return 1
	}
	accounts := groupAuths(auths)

	selected := []string{}
	seen := map[string]bool{}
	for _, name := range providers {
		if !seen[name] {
			seen[name] = true
			selected = append(selected, name)
		}
	}
	if len(selected) == 0 {
		for _, name := range providerNames {
			if accounts.count(name) > 0 {
				selected = append(selected, name)
			}
		}
	}
	if len(selected) == 0 {
		fmt.Fprintln(os.Stderr, "no providers configured; run: seavxrouter login [antigravity|codex|codebuddy|qoder|kiro]")
		return 1
	}

	fmt.Printf("checking providers: %s\n\n", strings.Join(selected, ", "))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	providersFailed := 0
	accountsOK, accountsFailed := 0, 0

	failProvider := func(name, detail string) {
		fmt.Fprintf(tw, "%s\n  fail\t%s\n", name, detail)
		providersFailed++
	}

	for _, name := range selected {
		if !isProvider(name) {
			failProvider(name, "unknown provider (want antigravity, codex, codebuddy, qoder or kiro)")
			continue
		}
		if accounts.count(name) == 0 {
			failProvider(name, "not configured; run: seavxrouter login "+name)
			continue
		}
		checker, err := newChecker(name, accounts, store, apiBase)
		if err != nil {
			failProvider(name, err.Error())
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
		results := checker.Check(checkCtx)
		cancel()
		if len(results) == 0 {
			failProvider(name, "no accounts")
			continue
		}
		fmt.Fprintf(tw, "%s\n", name)
		for i, r := range results {
			label := r.Account
			if label == "" {
				label = r.AccountID
			}
			if label == "" {
				label = fmt.Sprintf("account #%d", i+1)
			}
			latency := r.Latency.Round(time.Millisecond)
			if r.Err != nil {
				accountsFailed++
				detail := strings.Join(strings.Fields(r.Err.Error()), " ")
				fmt.Fprintf(tw, "  fail\t%s\t\t%s\t%s\n", label, detail, latency)
				continue
			}
			accountsOK++
			fmt.Fprintf(tw, "  ok\t%s\t\t%d model(s)\t%s\n", label, len(r.Models), latency)
		}
		tw.Flush()
	}
	tw.Flush()

	fmt.Println()
	if total := accountsOK + accountsFailed; total > 0 {
		fmt.Printf("%d account(s): %d ok, %d failed\n", total, accountsOK, accountsFailed)
	}
	if providersFailed > 0 {
		fmt.Printf("%d provider(s) failed\n", providersFailed)
	}
	if providersFailed > 0 || accountsFailed > 0 {
		return 1
	}
	fmt.Printf("all %d provider(s) ok\n", len(selected))
	return 0
}
