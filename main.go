package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/compress"
	"seavxrouter/internal/dashboard"
	"seavxrouter/internal/httpapi"
	"seavxrouter/internal/multi"
	"seavxrouter/internal/providers/antigravity"
	"seavxrouter/internal/providers/codebuddy"
	"seavxrouter/internal/providers/codex"
	"seavxrouter/internal/providers/kiro"
	"seavxrouter/internal/providers/opencode"
	"seavxrouter/internal/providers/qoder"
	"seavxrouter/internal/providers/upstream"
	"seavxrouter/internal/providers/verboo"
)

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

// resolveServer picks where the tui dials: an explicit -server flag wins,
// then the live URL recorded by a running server, then the preferred default,
// then the address derived from -listen.
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
	keyMode := len(os.Args) > 1 && os.Args[1] == "key"
	providerMode := len(os.Args) > 1 && os.Args[1] == "provider"
	comboMode := len(os.Args) > 1 && os.Args[1] == "combo"
	serviceMode := len(os.Args) > 1 && os.Args[1] == "service"
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
	if keyMode {
		os.Exit(runKeyCmd(os.Args[2:], os.Stdout, os.Stderr))
	}
	if providerMode {
		os.Exit(runProviderCmd(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if comboMode {
		os.Exit(runComboCmd(os.Args[2:], os.Stdout, os.Stderr))
	}
	if serviceMode {
		os.Exit(runServiceCmd(os.Args[2:], os.Stdout, os.Stderr))
	}
	fs := flag.NewFlagSet("seavxrouter", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:20128", "listen address")
	authFile := fs.String("auth-file", auth.DefaultPath(), "credentials file")
	combosFile := fs.String("combos", "", "JSONC file with model combos (empty = combos.jsonc next to the credentials file)")
	apiBase := fs.String("api-base", antigravity.DefaultAPIBase, "Antigravity API base URL")
	apiKey := fs.String("api-key", "", "require this bearer token on /v1/* (empty = no auth)")
	defaultModel := fs.String("default-model", "", "model used when the request omits one")
	usageDB := fs.String("usage-db", httpapi.DefaultUsageDBPath(), `SQLite file for token usage tracking (empty or "off" disables)`)
	dashboardDB := fs.String("dashboard-db", "", "SQLite file for dashboard login (empty disables web dashboard authentication routes)")
	keysDB := fs.String("keys-db", httpapi.DefaultKeysDBPath(), `SQLite file for tenant API keys (empty or "off" disables)`)
	compressFlag := fs.String("compress", "on", "compress large tool-result output: on|off")
	compressMinBytes := fs.Int("compress-min-bytes", 2000, "minimum tool-result bytes to compress")
	pricingFile := fs.String("pricing", "", "JSON file with model prices in USD per 1M tokens (empty = models.dev catalog)")
	refresh := fs.Duration("refresh", 2*time.Second, "tui auto-refresh interval")
	groupBy := fs.String("group-by", "day", "tui grouping: day, hour, provider, model, user or endpoint")
	once := fs.Bool("once", false, "tui: print one snapshot and exit")
	serverURL := fs.String("server", "", "tui: running server URL for SSE live logs (empty = auto-detect, then "+defaultTUIServer+"; off = disable)")
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
		case "verboo":
			engine := &verboo.Engine{Store: store, HTTP: &http.Client{}}
			err = engine.Login(ctx)
		case "opencode":
			err = opencode.Login(ctx, store, os.Stdout)
		case "nvidia", "groq", "cloudflare":
			p := upstream.Providers[loginProvider]
			err = upstream.Login(ctx, p, store, os.Stdin, os.Stdout, "")
		default:
			slog.Error("unknown login provider (want antigravity, codex, codebuddy, qoder, kiro, verboo, nvidia, groq, cloudflare or opencode)", "provider", loginProvider)
			os.Exit(1)
		}
		if err != nil {
			slog.Error("login failed", "err", err)
			os.Exit(1)
		}
		return
	}

	var provider backend.BackendAPI
	store := auth.AuthStore{Path: *authFile}
	auths, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot load credentials (%v)\nRun: seavxrouter login [antigravity|codex|codebuddy|qoder|kiro|verboo|nvidia|groq|cloudflare|opencode]\n", err)
		os.Exit(1)
	}
	accounts := groupAuths(auths)
	combosPath := *combosFile
	if combosPath == "" {
		combosPath = defaultCombosPath(*authFile)
	}
	multi := &multi.MultiBackend{Combos: multi.NewCombos(combosPath)}
	if len(accounts.antigravity) > 0 {
		engine, err := antigravity.NewEngine(store, *apiBase, accounts.antigravity)
		if err != nil {
			slog.Error("cannot start antigravity", "err", err)
			os.Exit(1)
		}
		engine.DefaultModel = *defaultModel
		if engine.DefaultModel == "" {
			if models, err := engine.Models(ctx); err == nil {
				engine.DefaultModel = antigravity.PreferredDefault(models)
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
	if len(accounts.verboo) > 0 {
		engine, err := verboo.NewEngine(store, accounts.verboo)
		if err != nil {
			slog.Error("cannot start verboo", "err", err)
			os.Exit(1)
		}
		engine.DefaultModel = *defaultModel
		if engine.DefaultModel == "" {
			if models, err := engine.Models(ctx); err == nil && len(models) > 0 {
				engine.DefaultModel = models[0].ID
			} else if err != nil {
				slog.Warn("could not fetch default model", "provider", "verboo", "err", err)
			}
		}
		multi.Verboo = engine
	}
	if len(accounts.nvidia) > 0 {
		engine, err := upstream.NewEngine("nvidia", upstream.Providers["nvidia"].ResolveBaseURL(), store, accounts.nvidia)
		if err != nil {
			slog.Error("cannot start nvidia", "err", err)
			os.Exit(1)
		}
		engine.DefaultModel = pickDefault(*defaultModel, upstream.Providers["nvidia"].DefaultModel)
		multi.NVIDIA = engine
	}
	if len(accounts.groq) > 0 {
		engine, err := upstream.NewEngine("groq", upstream.Providers["groq"].ResolveBaseURL(), store, accounts.groq)
		if err != nil {
			slog.Error("cannot start groq", "err", err)
			os.Exit(1)
		}
		engine.DefaultModel = pickDefault(*defaultModel, upstream.Providers["groq"].DefaultModel)
		multi.Groq = engine
	}
	if len(accounts.cloudflare) > 0 {
		engine, err := upstream.NewEngine("cloudflare", upstream.Providers["cloudflare"].ResolveBaseURL(), store, accounts.cloudflare)
		if err != nil {
			slog.Error("cannot start cloudflare", "err", err)
			os.Exit(1)
		}
		engine.ModelsPath = "accounts/%s/ai/models/search"
		engine.ChatPath = "accounts/%s/ai/v1/chat/completions"
		engine.DefaultModel = pickDefault(*defaultModel, upstream.Providers["cloudflare"].DefaultModel)
		multi.Cloudflare = engine
	}
	multi.Custom = map[string]backend.BackendAPI{}
	for _, name := range accounts.customNames() {
		customAuths := accounts.custom[name]
		base, err := customBaseURL(name, customAuths)
		if err != nil {
			slog.Error("invalid custom provider", "provider", name, "err", err)
			os.Exit(1)
		}
		engine, err := upstream.NewEngine(name, base, store, customAuths)
		if err != nil {
			slog.Error("cannot start "+name, "err", err)
			os.Exit(1)
		}
		engine.DefaultModel = *defaultModel
		if engine.DefaultModel == "" {
			defaultCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			models, err := engine.Models(defaultCtx)
			cancel()
			if err == nil && len(models) > 0 {
				engine.DefaultModel = models[0].ID
			} else if err != nil {
				slog.Warn("could not fetch default model", "provider", name, "err", err)
			}
		}
		multi.Custom[name] = engine
	}
	if len(accounts.opencode) > 0 {
		engine, err := opencode.NewEngine(opencode.ConfigFromEnv())
		if err != nil {
			slog.Error("cannot start opencode", "err", err)
			os.Exit(1)
		}
		defer engine.Close()
		multi.OpenCode = engine
	}
	slog.Info("providers ready", "antigravity", len(accounts.antigravity), "codex", len(accounts.codex), "codebuddy", len(accounts.codebuddy), "qoder", len(accounts.qoder), "kiro", len(accounts.kiro), "verboo", len(accounts.verboo), "nvidia", len(accounts.nvidia), "groq", len(accounts.groq), "cloudflare", len(accounts.cloudflare), "opencode", len(accounts.opencode), "custom", len(accounts.custom))
	if names := multi.Combos.Names(); len(names) > 0 {
		slog.Info("model combos loaded", "file", combosPath, "combos", len(names))
	}
	provider = multi

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

	var keyStore *httpapi.KeyStore
	switch *keysDB {
	case "", "off", "none":
		slog.Info("api key store disabled")
	default:
		ks, err := httpapi.OpenKeyStore(*keysDB)
		if err != nil {
			slog.Error("cannot open api key db", "err", err)
			os.Exit(1)
		}
		defer ks.Close()
		keyStore = ks
		slog.Info("api key store enabled", "db", *keysDB)
	}
	if keyStore != nil {
		keys, err := keyStore.List()
		if err != nil {
			slog.Warn("cannot list api keys", "err", err)
		} else {
			active, budgeted := 0, 0
			for _, k := range keys {
				if k.RevokedAt == nil {
					active++
				}
				if k.BudgetUSD > 0 {
					budgeted++
				}
			}
			if active > 0 && *apiKey == "" {
				slog.Warn("tenant API keys exist but -api-key is not set; multi-tenant auth is disabled")
			}
			if budgeted > 0 && usageStore == nil {
				slog.Warn("per-key budgets are not enforced because usage tracking is disabled")
			}
		}
	}

	compressor := compress.New(compress.Options{Enabled: compressionEnabled(*compressFlag), MinBytes: *compressMinBytes})
	slog.Info("compression", "enabled", compressor.Enabled(), "min_bytes", *compressMinBytes)
	serverOpts := []httpapi.ServerOption{httpapi.WithCompression(compressor)}
	if keyStore != nil {
		serverOpts = append(serverOpts, httpapi.WithKeys(keyStore))
	}
	if *dashboardDB != "" {
		store, err := httpapi.OpenDashboardStore(*dashboardDB)
		if err != nil {
			slog.Error("cannot open dashboard db", "err", err)
			os.Exit(1)
		}
		defer store.Close()
		serverOpts = append(serverOpts, httpapi.WithDashboard(store))
	}
	srv := httpapi.NewServer(provider, *apiKey, usageStore, serverOpts...)

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
		slog.Info("seavxrouter listening", "addr", *listen, "auth", *apiKey != "")
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			stop()
		}
	}()

	// Pricing is advisory for cost reporting, so the catalog fetch runs after
	// the listener is up: requests use the built-in table until it lands.
	go func() {
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
		srv.SetPricing(pricing)
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	slog.Info("stopped")
}

// pickDefault returns the configured override when set, otherwise the
// provider's built-in default model.
func pickDefault(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

var providerNames = []string{"antigravity", "codex", "codebuddy", "qoder", "kiro", "verboo", "nvidia", "groq", "cloudflare", "opencode"}

type providerAccounts struct {
	antigravity []*auth.Auth
	codex       []*auth.Auth
	codebuddy   []*auth.Auth
	qoder       []*auth.Auth
	kiro        []*auth.Auth
	verboo      []*auth.Auth
	nvidia      []*auth.Auth
	groq        []*auth.Auth
	cloudflare  []*auth.Auth
	opencode    []*auth.Auth
	// custom collects user-defined providers (auth entries carrying a
	// base_url), grouped by their model prefix.
	custom map[string][]*auth.Auth
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
		case "verboo":
			accounts.verboo = append(accounts.verboo, a)
		case "nvidia":
			accounts.nvidia = append(accounts.nvidia, a)
		case "groq":
			accounts.groq = append(accounts.groq, a)
		case "cloudflare":
			accounts.cloudflare = append(accounts.cloudflare, a)
		case "opencode":
			accounts.opencode = append(accounts.opencode, a)
		default:
			if a.BaseURL != "" {
				if accounts.custom == nil {
					accounts.custom = map[string][]*auth.Auth{}
				}
				accounts.custom[a.Provider] = append(accounts.custom[a.Provider], a)
				continue
			}
			accounts.antigravity = append(accounts.antigravity, a)
		}
	}
	return accounts
}

func (p providerAccounts) list(name string) []*auth.Auth {
	switch name {
	case "antigravity":
		return p.antigravity
	case "codex":
		return p.codex
	case "codebuddy":
		return p.codebuddy
	case "qoder":
		return p.qoder
	case "kiro":
		return p.kiro
	case "verboo":
		return p.verboo
	case "nvidia":
		return p.nvidia
	case "groq":
		return p.groq
	case "cloudflare":
		return p.cloudflare
	case "opencode":
		return p.opencode
	}
	return p.custom[name]
}

func (p providerAccounts) count(name string) int {
	return len(p.list(name))
}

func (p providerAccounts) customNames() []string {
	names := make([]string, 0, len(p.custom))
	for name := range p.custom {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isBuiltinProvider(name string) bool {
	for _, p := range providerNames {
		if p == name {
			return true
		}
	}
	return false
}

func isProvider(name string, accounts providerAccounts) bool {
	if _, ok := accounts.custom[name]; ok {
		return true
	}
	return isBuiltinProvider(name)
}

func newChecker(name string, accounts providerAccounts, store auth.AuthStore, apiBase string) (backend.Checker, error) {
	if customAuths, ok := accounts.custom[name]; ok {
		base, err := customBaseURL(name, customAuths)
		if err != nil {
			return nil, err
		}
		return upstream.NewEngine(name, base, store, customAuths)
	}
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
	case "verboo":
		return verboo.NewEngine(store, accounts.verboo)
	case "opencode":
		return opencode.NewEngine(opencode.ConfigFromEnv())
	case "nvidia", "groq", "cloudflare":
		return upstream.NewProviderEngine(upstream.Providers[name], store, accounts.list(name))
	}
	return nil, fmt.Errorf("unknown provider %q", name)
}

// checkTimeout bounds one provider's live credential and model check.
const checkTimeout = 45 * time.Second

func runCheck(ctx context.Context, authFile, apiBase string, providers []string) int {
	store := auth.AuthStore{Path: authFile}
	auths, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot load credentials (%v)\nRun: seavxrouter login [antigravity|codex|codebuddy|qoder|kiro|verboo|nvidia|groq|cloudflare|opencode]\n", err)
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
		selected = append(selected, accounts.customNames()...)
	}
	if len(selected) == 0 {
		fmt.Fprintln(os.Stderr, "no providers configured; run: seavxrouter login [antigravity|codex|codebuddy|qoder|kiro|verboo|nvidia|groq|cloudflare|opencode] or seavxrouter provider add <name> -base-url URL")
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
		if !isProvider(name, accounts) {
			failProvider(name, "unknown provider (built-ins: antigravity, codex, codebuddy, qoder, kiro, verboo, nvidia, groq, cloudflare, opencode; custom see: seavxrouter provider list)")
			continue
		}
		if accounts.count(name) == 0 {
			failProvider(name, "not configured; run: seavxrouter login "+name+" or seavxrouter provider add "+name+" -base-url URL")
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
		if closer, ok := checker.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
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

// runKeyCmd handles `seavxrouter key create|list|revoke` against the tenant
// key database. Flags may follow the positional name or id.
func runKeyCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: seavxrouter key create <name> [-budget USD] [-limit-token N] [-keys-db path]")
		fmt.Fprintln(stderr, "       seavxrouter key list [-keys-db path]")
		fmt.Fprintln(stderr, "       seavxrouter key revoke <id> [-keys-db path]")
		return 2
	}
	action := args[0]
	switch action {
	case "create", "list", "revoke":
	default:
		fmt.Fprintf(stderr, "unknown key action %q (want create, list or revoke)\n", action)
		return 2
	}

	rest := args[1:]
	positional := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		positional = rest[0]
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("seavxrouter key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keysDB := fs.String("keys-db", httpapi.DefaultKeysDBPath(), "SQLite file with tenant API keys")
	budget := fs.Float64("budget", 0, "create: monthly budget in USD (0 = unlimited)")
	limitToken := fs.Int64("limit-token", 0, "create: monthly total token limit (0 = unlimited)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	var id int64
	switch action {
	case "create":
		if positional == "" {
			fmt.Fprintln(stderr, "usage: seavxrouter key create <name> [-budget USD] [-limit-token N]")
			return 2
		}
	case "list":
		if positional != "" {
			fmt.Fprintf(stderr, "unexpected argument %q\n", positional)
			return 2
		}
	default: // revoke
		if positional == "" {
			fmt.Fprintln(stderr, "usage: seavxrouter key revoke <id>")
			return 2
		}
		parsed, err := strconv.ParseInt(positional, 10, 64)
		if err != nil || parsed <= 0 {
			fmt.Fprintf(stderr, "invalid API key id %q\n", positional)
			return 2
		}
		id = parsed
	}

	store, err := httpapi.OpenKeyStore(*keysDB)
	if err != nil {
		fmt.Fprintf(stderr, "cannot open api key db: %v\n", err)
		return 1
	}
	defer store.Close()

	switch action {
	case "create":
		key, plaintext, err := store.Create(positional, *budget, *limitToken)
		if err != nil {
			fmt.Fprintf(stderr, "cannot create API key: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "created API key %q (id %d)\n", key.Name, key.ID)
		fmt.Fprintf(stdout, "key: %s\n", plaintext)
		fmt.Fprintln(stdout, "store this key now; it will not be shown again")
		return 0
	case "list":
		keys, err := store.List()
		if err != nil {
			fmt.Fprintf(stderr, "cannot list API keys: %v\n", err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tBUDGET\tTOKENS\tCREATED\tSTATUS")
		for _, k := range keys {
			status := "active"
			if k.RevokedAt != nil {
				status = "revoked"
			}
			budget := "unlimited"
			if k.BudgetUSD > 0 {
				budget = fmt.Sprintf("$%.2f/mo", k.BudgetUSD)
			}
			tokens := "unlimited"
			if k.LimitToken > 0 {
				tokens = fmt.Sprintf("%d/mo", k.LimitToken)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Prefix, budget, tokens, k.CreatedAt, status)
		}
		tw.Flush()
		if len(keys) == 0 {
			fmt.Fprintln(stdout, "no API keys")
		}
		return 0
	}

	key, err := store.Revoke(id)
	if errors.Is(err, httpapi.ErrKeyNotFound) {
		fmt.Fprintf(stderr, "API key %d not found\n", id)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "cannot revoke API key: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "revoked API key %d (%s)\n", key.ID, key.Name)
	return 0
}

var customProviderNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// runProviderCmd handles `seavxrouter provider add|list|remove` for custom
// OpenAI-compatible endpoints. The base URL and bearer key live in auth.jsonc;
// model IDs use "<name>/<model>".
func runProviderCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		providerUsage(stderr)
		return 2
	}
	action := args[0]
	switch action {
	case "add", "list", "remove":
	default:
		fmt.Fprintf(stderr, "unknown provider action %q (want add, list or remove)\n", action)
		return 2
	}
	if stdin == nil {
		stdin = os.Stdin
	}

	rest := args[1:]
	var positional []string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("seavxrouter provider", flag.ContinueOnError)
	fs.SetOutput(stderr)
	authFile := fs.String("auth-file", auth.DefaultPath(), "credentials file")
	baseURL := fs.String("base-url", "", "add: upstream API base URL, e.g. https://api.example.com/v1")
	apiKey := fs.String("api-key", "", "add: upstream bearer API key (prompted when empty)")
	probeModel := fs.String("model", "", "add: model ID used for the chat validation smoke test")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	positional = append(positional, fs.Args()...)

	want := 1
	if action == "list" {
		want = 0
	}
	if len(positional) != want {
		if want == 1 {
			fmt.Fprintf(stderr, "usage: seavxrouter provider %s <name> [flags]\n", action)
		} else {
			fmt.Fprintln(stderr, "usage: seavxrouter provider list [-auth-file path]")
		}
		return 2
	}

	store := auth.AuthStore{Path: *authFile}
	switch action {
	case "add":
		return runProviderAdd(store, positional[0], *baseURL, *apiKey, *probeModel, stdin, stdout, stderr)
	case "list":
		return runProviderList(store, stdout, stderr)
	default:
		return runProviderRemove(store, positional[0], stdout, stderr)
	}
}

func providerUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: seavxrouter provider add <name> -base-url URL [-api-key KEY] [-model ID] [-auth-file path]")
	fmt.Fprintln(w, "       seavxrouter provider list [-auth-file path]")
	fmt.Fprintln(w, "       seavxrouter provider remove <name> [-auth-file path]")
}

func normalizeCustomProviderName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if !customProviderNameRE.MatchString(name) {
		return "", fmt.Errorf("provider name %q must start with a letter and use only a-z, 0-9, _ or - (max 32 chars)", raw)
	}
	if isBuiltinProvider(name) {
		return "", fmt.Errorf("provider name %q is built in; use: seavxrouter login %s", name, name)
	}
	switch name {
	case "cf", "auto", "default", "combo":
		return "", fmt.Errorf("provider name %q is reserved", name)
	}
	return name, nil
}

func normalizeCustomBaseURL(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid base URL %q: want http(s)://host[/path]", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid base URL %q: remove the query string and fragment", raw)
	}
	return base, nil
}

// customBaseURL validates the endpoint shared by a custom provider's accounts.
func customBaseURL(name string, auths []*auth.Auth) (string, error) {
	if len(auths) == 0 {
		return "", fmt.Errorf("no %s accounts", name)
	}
	base, err := normalizeCustomBaseURL(auths[0].BaseURL)
	if err != nil {
		return "", err
	}
	for _, a := range auths[1:] {
		if a.BaseURL != auths[0].BaseURL {
			return "", fmt.Errorf("accounts use different base URLs (%s and %s); remove the provider and add it again", auths[0].BaseURL, a.BaseURL)
		}
	}
	return base, nil
}

func readPrompt(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprint(out, label)
	line, err := reader.ReadString('\n')
	value := strings.TrimSpace(line)
	if value == "" && err != nil {
		return "", err
	}
	return value, nil
}

func runProviderAdd(store auth.AuthStore, rawName, rawBaseURL, rawKey, rawProbeModel string, stdin io.Reader, stdout, stderr io.Writer) int {
	name, err := normalizeCustomProviderName(rawName)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	reader := bufio.NewReader(stdin)
	base := strings.TrimSpace(rawBaseURL)
	if base == "" {
		base, err = readPrompt(reader, stdout, "Base URL: ")
		if err != nil {
			fmt.Fprintln(stderr, "a base URL is required")
			return 2
		}
	}
	base, err = normalizeCustomBaseURL(base)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	key := strings.TrimSpace(rawKey)
	if key == "" {
		key, err = readPrompt(reader, stdout, "API key: ")
		if err != nil {
			fmt.Fprintln(stderr, "an API key is required")
			return 2
		}
	}
	if key == "" {
		fmt.Fprintln(stderr, "an API key is required")
		return 2
	}

	existing, err := store.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "cannot load credentials: %v\n", err)
		return 1
	}
	for _, a := range existing {
		if a.Provider != name {
			continue
		}
		if a.BaseURL == "" {
			fmt.Fprintf(stderr, "a stored account named %q exists without a base_url; remove it first\n", name)
			return 1
		}
		if a.BaseURL != base {
			fmt.Fprintf(stderr, "provider %q already points at %s; remove it first: seavxrouter provider remove %s\n", name, a.BaseURL, name)
			return 1
		}
	}

	// Validation is mandatory: the key must authenticate against the base URL,
	// the endpoint must return a non-empty model catalog, and a tiny chat
	// completion must pass before anything is stored. -model pins that
	// completion to a model the operator knows their plan can use.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	models, note, err := upstream.ProbeKey(ctx, name, base, key, strings.TrimSpace(rawProbeModel))
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "cannot verify %s at %s: %v\n", name, base, err)
		fmt.Fprintln(stderr, "check the base URL and API key, then try again")
		return 1
	}

	acct := &auth.Auth{Provider: name, AccessToken: key, BaseURL: base}
	if err := store.Save([]*auth.Auth{acct}); err != nil {
		fmt.Fprintf(stderr, "cannot save credentials: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "verified %s: %d model(s) available\n", name, len(models))
	if note != "" {
		fmt.Fprintf(stdout, "warning: %s\n", note)
	}
	fmt.Fprintf(stdout, "added custom provider %q at %s\n", name, base)
	fmt.Fprintf(stdout, "model IDs: %s/<model>\n", name)
	fmt.Fprintf(stdout, "restart a running server to activate the provider, then run: seavxrouter check %s\n", name)
	return 0
}

func runProviderList(store auth.AuthStore, stdout, stderr io.Writer) int {
	auths, err := store.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "no custom providers")
			return 0
		}
		fmt.Fprintf(stderr, "cannot load credentials: %v\n", err)
		return 1
	}
	type entry struct {
		baseURL string
		keys    int
	}
	byName := map[string]*entry{}
	for _, a := range auths {
		if a.BaseURL == "" || isBuiltinProvider(a.Provider) {
			continue
		}
		e := byName[a.Provider]
		if e == nil {
			e = &entry{baseURL: a.BaseURL}
			byName[a.Provider] = e
		}
		e.keys++
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(stdout, "no custom providers")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tBASE URL\tKEYS")
	for _, name := range names {
		e := byName[name]
		fmt.Fprintf(tw, "%s\t%s\t%d\n", name, e.baseURL, e.keys)
	}
	tw.Flush()
	return 0
}

func runProviderRemove(store auth.AuthStore, rawName string, stdout, stderr io.Writer) int {
	name, err := normalizeCustomProviderName(rawName)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	removed, err := store.RemoveProvider(name)
	if err != nil {
		fmt.Fprintf(stderr, "cannot remove provider: %v\n", err)
		return 1
	}
	if removed == 0 {
		fmt.Fprintf(stderr, "no custom provider %q\n", name)
		return 1
	}
	fmt.Fprintf(stdout, "removed custom provider %q (%d key(s))\n", name, removed)
	fmt.Fprintln(stdout, "restart a running server to drop the provider")
	return 0
}

// defaultCombosPath keeps combos.jsonc next to the credentials file, like the
// hidden-models blocklist.
func defaultCombosPath(authPath string) string {
	return filepath.Join(filepath.Dir(authPath), "combos.jsonc")
}

func comboUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: seavxrouter combo add <name> -models ID,ID,... [-auth-file path] [-combos path]")
	fmt.Fprintln(w, "       seavxrouter combo list [-auth-file path] [-combos path]")
	fmt.Fprintln(w, "       seavxrouter combo remove <name> [-auth-file path] [-combos path]")
}

func normalizeComboName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if !customProviderNameRE.MatchString(name) {
		return "", fmt.Errorf("combo name %q must start with a letter and use only a-z, 0-9, _ or - (max 32 chars)", raw)
	}
	return name, nil
}

// parseComboModels splits the -models value, preserving order and dropping
// blank entries so "a, b" and "a,b" behave alike.
func parseComboModels(raw string) []string {
	var out []string
	for _, member := range strings.Split(raw, ",") {
		if member = strings.TrimSpace(member); member != "" {
			out = append(out, member)
		}
	}
	return out
}

// runComboCmd handles `seavxrouter combo add|list|remove`. A combo is an
// ordered fallback list requested as combo/<name>; the running server re-reads
// the file when its mtime changes.
func runComboCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		comboUsage(stderr)
		return 2
	}
	action := args[0]
	switch action {
	case "add", "list", "remove":
	default:
		fmt.Fprintf(stderr, "unknown combo action %q (want add, list or remove)\n", action)
		return 2
	}

	rest := args[1:]
	var positional []string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("seavxrouter combo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	authFile := fs.String("auth-file", auth.DefaultPath(), "credentials file; combos.jsonc sits next to it")
	combosFile := fs.String("combos", "", "combos file (empty = combos.jsonc next to the credentials file)")
	models := fs.String("models", "", "add: comma-separated model IDs in fallback order")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	positional = append(positional, fs.Args()...)

	want := 1
	if action == "list" {
		want = 0
	}
	if len(positional) != want {
		comboUsage(stderr)
		return 2
	}
	path := *combosFile
	if path == "" {
		path = defaultCombosPath(*authFile)
	}

	entries, err := multi.LoadCombosFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	switch action {
	case "add":
		name, err := normalizeComboName(positional[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		members := parseComboModels(*models)
		if len(members) == 0 {
			fmt.Fprintln(stderr, "at least one model is required: -models provider/model,provider/model")
			return 2
		}
		for _, member := range members {
			if strings.HasPrefix(member, multi.ComboPrefix) {
				fmt.Fprintf(stderr, "combo members must name real models, not other combos: %s\n", member)
				return 2
			}
		}
		entries[name] = members
		if err := multi.SaveCombosFile(path, entries); err != nil {
			fmt.Fprintf(stderr, "cannot save combos: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "combo %q: %s\n", name, strings.Join(members, " -> "))
		fmt.Fprintf(stdout, "model ID: %s%s\n", multi.ComboPrefix, name)
		fmt.Fprintln(stdout, "a running server picks up combos.jsonc changes without a restart")
		return 0
	case "list":
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Fprintln(stdout, "no combos")
			return 0
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tMODELS")
		for _, name := range names {
			fmt.Fprintf(tw, "%s\t%s\n", name, strings.Join(entries[name], " -> "))
		}
		tw.Flush()
		return 0
	default: // remove
		name, err := normalizeComboName(positional[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if _, ok := entries[name]; !ok {
			fmt.Fprintf(stderr, "no combo %q\n", name)
			return 1
		}
		delete(entries, name)
		if err := multi.SaveCombosFile(path, entries); err != nil {
			fmt.Fprintf(stderr, "cannot save combos: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "removed combo %q\n", name)
		return 0
	}
}
