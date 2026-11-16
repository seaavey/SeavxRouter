package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	fs := flag.NewFlagSet("seavxrouter", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:20128", "listen address")
	authFile := fs.String("auth-file", auth.DefaultPath(), "credentials file")
	apiBase := fs.String("api-base", antigravity.DefaultAPIBase, "Antigravity API base URL")
	apiKey := fs.String("api-key", "", "require this bearer token on /v1/* (empty = no auth)")
	defaultModel := fs.String("default-model", "", "model used when the request omits one")
	usageDB := fs.String("usage-db", httpapi.DefaultUsageDBPath(), `SQLite file for token usage tracking (empty or "off" disables)`)
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
	if len(accounts.opencode) > 0 {
		engine, err := opencode.NewEngine(opencode.ConfigFromEnv())
		if err != nil {
			slog.Error("cannot start opencode", "err", err)
			os.Exit(1)
		}
		defer engine.Close()
		multi.OpenCode = engine
	}
	slog.Info("providers ready", "antigravity", len(accounts.antigravity), "codex", len(accounts.codex), "codebuddy", len(accounts.codebuddy), "qoder", len(accounts.qoder), "kiro", len(accounts.kiro), "verboo", len(accounts.verboo), "nvidia", len(accounts.nvidia), "groq", len(accounts.groq), "cloudflare", len(accounts.cloudflare), "opencode", len(accounts.opencode))
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
	serverOpts := []httpapi.ServerOption{httpapi.WithCompression(compressor), httpapi.WithPricing(pricing)}
	if keyStore != nil {
		serverOpts = append(serverOpts, httpapi.WithKeys(keyStore))
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
	return nil
}

func (p providerAccounts) count(name string) int {
	return len(p.list(name))
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
	}
	if len(selected) == 0 {
		fmt.Fprintln(os.Stderr, "no providers configured; run: seavxrouter login [antigravity|codex|codebuddy|qoder|kiro|verboo|nvidia|groq|cloudflare|opencode]")
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
			failProvider(name, "unknown provider (want antigravity, codex, codebuddy, qoder, kiro, verboo, nvidia, groq, cloudflare or opencode)")
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
