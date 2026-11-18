package upstream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// Provider pins one vendor's endpoint, default model and key format. Every
// provider here exposes an OpenAI-compatible surface, so the engine serves
// them all; only the constants differ.
type Provider struct {
	// Name is the provider label stored on credentials ("nvidia", "groq",
	// "cloudflare").
	Name string
	// BaseURL is the API root, no trailing slash.
	BaseURL string
	// DefaultModel is the fallback when a request names no model.
	DefaultModel string
	// KeyEnv is the environment variable login reads before prompting.
	KeyEnv string
	// KeyPrefix is a soft prefix hint shown during login; keys from this
	// provider do not always carry it.
	KeyPrefix string
	// SignupURL is printed with the login prompt.
	SignupURL string
	// AccountIDEnv names the variable that carries a Cloudflare account ID,
	// which the request path needs. Empty for providers without one.
	AccountIDEnv string
	// BaseURLEnv optionally overrides BaseURL at runtime, which lets the same
	// binary target a test upstream.
	BaseURLEnv string
}

// ResolveBaseURL returns the provider's base URL, honoring its override env var.
func (p Provider) ResolveBaseURL() string {
	if p.BaseURLEnv != "" {
		if v := strings.TrimSpace(os.Getenv(p.BaseURLEnv)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return p.BaseURL
}

// Providers lists the free-tier OpenAI-compatible vendors wired into the router.
var Providers = map[string]Provider{
	"nvidia": {
		Name:         "nvidia",
		BaseURLEnv:   "NVIDIA_BASE_URL",
		BaseURL:      "https://integrate.api.nvidia.com/v1",
		DefaultModel: "deepseek-ai/deepseek-v4.1-flash",
		KeyEnv:       "NVIDIA_API_KEY",
		KeyPrefix:    "nvapi-",
		SignupURL:    "https://build.nvidia.com",
	},
	"groq": {
		Name:         "groq",
		BaseURLEnv:   "GROQ_BASE_URL",
		BaseURL:      "https://api.groq.com/openai/v1",
		DefaultModel: "openai/gpt-oss-120b",
		KeyEnv:       "GROQ_API_KEY",
		KeyPrefix:    "gsk_",
		SignupURL:    "https://console.groq.com/keys",
	},
	"cloudflare": {
		Name:         "cloudflare",
		BaseURLEnv:   "CLOUDFLARE_BASE_URL",
		BaseURL:      "https://api.cloudflare.com/client/v4",
		DefaultModel: "@cf/meta/llama-3.1-8b-instruct",
		KeyEnv:       "CLOUDFLARE_API_TOKEN",
		SignupURL:    "https://dash.cloudflare.com/profile/api-tokens",
		AccountIDEnv: "CLOUDFLARE_ACCOUNT_ID",
	},
}

// NewProviderEngine builds an engine from a provider definition, applying the
// account-scoped path layout when the provider needs an account ID.
func NewProviderEngine(p Provider, store auth.AuthStore, auths []*auth.Auth) (*Engine, error) {
	e, err := NewEngine(p.Name, p.ResolveBaseURL(), store, auths)
	if err != nil {
		return nil, err
	}
	e.DefaultModel = p.DefaultModel
	if p.AccountIDEnv != "" {
		e.ModelsPath = "accounts/%s/ai/models/search"
		e.ChatPath = "accounts/%s/ai/v1/chat/completions"
	}
	return e, nil
}

// Login validates a bearer key from the provider's env var or stdin, then
// stores it alongside the provider's other accounts.
func Login(ctx context.Context, p Provider, store auth.AuthStore, in io.Reader, out io.Writer, accountID string) error {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	key := strings.TrimSpace(os.Getenv(p.KeyEnv))
	if key == "" {
		fmt.Fprintf(out, "%s API key (%s): ", p.Name, p.SignupURL)
		line, err := bufio.NewReader(in).ReadString('\n')
		key = strings.TrimSpace(line)
		if key == "" && err != nil {
			return err
		}
	}
	if key == "" {
		return fmt.Errorf("no %s API key provided", p.Name)
	}
	if p.KeyPrefix != "" && !strings.HasPrefix(key, p.KeyPrefix) {
		fmt.Fprintf(out, "note: %s keys usually start with %q\n", p.Name, p.KeyPrefix)
	}
	if p.AccountIDEnv != "" && accountID == "" {
		accountID = strings.TrimSpace(os.Getenv(p.AccountIDEnv))
	}
	if p.AccountIDEnv != "" && accountID == "" {
		fmt.Fprintf(out, "Cloudflare account ID: ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		accountID = strings.TrimSpace(line)
		if accountID == "" {
			return fmt.Errorf("cloudflare: account ID is required")
		}
	}

	acct := &auth.Auth{Provider: p.Name, AccessToken: key, AccountID: accountID}
	models, err := probeKey(ctx, p, acct)
	if err != nil {
		return fmt.Errorf("%s login failed: %w", p.Name, err)
	}

	existing, _ := store.Load()
	own := make([]*auth.Auth, 0, len(existing)+1)
	for _, a := range existing {
		if a.Provider == p.Name {
			own = append(own, a)
		}
	}
	merged := false
	for i, a := range own {
		if a.AccessToken == key {
			own[i] = acct
			merged = true
			break
		}
	}
	if !merged {
		own = append(own, acct)
	}
	if err := store.Save(own); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s key accepted: %d model(s) available\n%d key(s) stored in %s\n", p.Name, len(models), len(own), store.Path)
	return nil
}

// probeKey confirms the key actually authenticates by listing the catalog.
func probeKey(ctx context.Context, p Provider, acct *auth.Auth) ([]string, error) {
	e, err := NewProviderEngine(p, auth.AuthStore{}, []*auth.Auth{acct})
	if err != nil {
		return nil, err
	}
	e.HTTP = backend.NewHTTPClient()
	models, err := e.fetchModelsWith(ctx, acct)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out, nil
}
