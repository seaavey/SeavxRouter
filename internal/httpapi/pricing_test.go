package httpapi

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"seavxrouter/internal/backend"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPricingCost(t *testing.T) {
	p := DefaultPricing()
	u := backend.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if got := p.Cost("claude-sonnet-4-6", u); !approx(got, 18) {
		t.Errorf("cost = %v, want 18", got)
	}
	// Cached input is billed at the cached rate, not the full input rate.
	u = backend.Usage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	if got := p.Cost("claude-sonnet-4-6", u); !approx(got, 0.3) {
		t.Errorf("cached cost = %v, want 0.3", got)
	}
	// Cached tokens above the prompt size are clamped.
	u = backend.Usage{InputTokens: 100, CacheReadTokens: 500}
	if got := p.Cost("claude-sonnet-4-6", u); !approx(got, 0.00003) {
		t.Errorf("clamped cost = %v", got)
	}
	if got := p.Cost("unknown-model", u); got != 0 {
		t.Errorf("unknown cost = %v", got)
	}
	var nilP *Pricing
	if got := nilP.Cost("claude-sonnet-4-6", u); got != 0 {
		t.Errorf("nil pricing cost = %v", got)
	}
}

func TestPricingStripsProviderPrefixAndDots(t *testing.T) {
	p := DefaultPricing()
	u := backend.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if got := p.Cost("codex/gpt-5", u); !approx(got, 11.25) {
		t.Errorf("prefixed cost = %v, want 11.25", got)
	}
	if got := p.Cost("kiro/claude-sonnet-4.5", u); !approx(got, 18) {
		t.Errorf("dotted cost = %v, want 18", got)
	}
}

const sampleModelsDevCatalog = `{
  "anthropic": {"models": {
    "claude-sonnet-4-5": {"cost": {"input": 4, "output": 20, "cache_read": 0.4}}
  }},
  "openrouter": {"models": {
    "claude-sonnet-4-5": {"cost": {"input": 9, "output": 45, "cache_read": 0.9}},
    "glm-5": {"cost": {"input": 0.6, "output": 2.7, "cache_read": 0.15}},
    "free-model": {"cost": {"input": 0, "output": 0}}
  }}
}`

// fakeModelsDev points the catalog URL at an httptest server for one test.
func fakeModelsDev(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldURL, oldClient := modelsDevURL, modelsDevHTTP
	modelsDevURL = srv.URL
	modelsDevHTTP = srv.Client()
	t.Cleanup(func() {
		modelsDevURL, modelsDevHTTP = oldURL, oldClient
	})
}

func TestModelsDevPricingPrefersFirstPartyAndCaches(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), ".seavx", "cache", "models.json")
	fetches := 0
	fakeModelsDev(t, func(w http.ResponseWriter, r *http.Request) {
		fetches++
		w.Write([]byte(sampleModelsDevCatalog))
	})

	p, err := ModelsDevPricing(cachePath)
	if err != nil {
		t.Fatalf("ModelsDevPricing: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1", fetches)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	u := backend.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if got := p.Cost("claude-sonnet-4.5", u); !approx(got, 24) {
		t.Errorf("first-party cost = %v, want 24", got)
	}
	if got := p.Cost("glm-5", u); !approx(got, 3.3) {
		t.Errorf("catalog-only cost = %v, want 3.3", got)
	}
	if _, ok := p.lookup("free-model"); ok {
		t.Error("unpriced catalog entries must be skipped")
	}

	// A fresh cache is reused without another download.
	if _, err := ModelsDevPricing(cachePath); err != nil {
		t.Fatalf("ModelsDevPricing from cache: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fresh cache refetched: %d fetches", fetches)
	}
}

func TestModelsDevPricingRefreshesStaleCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte(sampleModelsDevCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * modelsDevCacheTTL)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	fakeModelsDev(t, func(w http.ResponseWriter, r *http.Request) {
		fetches++
		w.Write([]byte(`{"anthropic":{"models":{"claude-sonnet-4-5":{"cost":{"input":10,"output":50}}}}}`))
	})
	p, err := ModelsDevPricing(cachePath)
	if err != nil {
		t.Fatalf("ModelsDevPricing: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1", fetches)
	}
	u := backend.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if got := p.Cost("claude-sonnet-4-5", u); !approx(got, 60) {
		t.Errorf("refreshed cost = %v, want 60", got)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache not rewritten: %v", err)
	}
}

func TestModelsDevPricingFallsBackToStaleCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte(sampleModelsDevCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * modelsDevCacheTTL)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatal(err)
	}
	fakeModelsDev(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	p, err := ModelsDevPricing(cachePath)
	if err != nil {
		t.Fatalf("expected stale cache fallback, got %v", err)
	}
	u := backend.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if got := p.Cost("claude-sonnet-4.5", u); !approx(got, 24) {
		t.Errorf("cached cost = %v, want 24", got)
	}
}

func TestModelsDevPricingFailsWithoutCache(t *testing.T) {
	fakeModelsDev(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	if _, err := ModelsDevPricing(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected an error without a cache")
	}
}

func TestLoadPricingOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	content := `{"GPT-5.1-codex": {"input": 1, "output": 2, "cached": 0.5}, "custom": {"input": 4, "output": 8}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPricing(path)
	if err != nil {
		t.Fatal(err)
	}
	u := backend.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	// Longest prefix wins: the override beats the "gpt-5" default.
	if got := p.Cost("gpt-5.1-codex", u); !approx(got, 3) {
		t.Errorf("override cost = %v, want 3", got)
	}
	if got := p.Cost("custom-model", u); !approx(got, 12) {
		t.Errorf("prefix cost = %v, want 12", got)
	}
	// Defaults still apply to untouched keys.
	if got := p.Cost("claude-sonnet-4-6", u); !approx(got, 18) {
		t.Errorf("default cost = %v, want 18", got)
	}
	if _, err := LoadPricing(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing pricing file must error")
	}
}
