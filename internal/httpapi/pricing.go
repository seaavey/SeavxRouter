package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"seavxrouter/internal/backend"
)

// Price is the USD cost per one million tokens for one model family.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cached float64 `json:"cached"`
}

// Pricing maps model IDs and prefixes to prices. Defaults cover the known
// model families; the models.dev catalog and an optional JSON file merge over
// them.
type Pricing struct {
	prices map[string]Price
	source string
}

// defaultPrices are USD per million tokens; keys match by prefix, longest wins.
var defaultPrices = map[string]Price{
	"claude-opus-4":     {Input: 15, Output: 75, Cached: 1.5},
	"claude-sonnet-4":   {Input: 3, Output: 15, Cached: 0.3},
	"claude-3-7-sonnet": {Input: 3, Output: 15, Cached: 0.3},
	"claude-3-5-sonnet": {Input: 3, Output: 15, Cached: 0.3},
	"gemini-3.8-flash":  {Input: 0.3, Output: 2.5, Cached: 0.075},
	"gemini-2.5-pro":    {Input: 1.25, Output: 10, Cached: 0.31},
	"gemini-2.5-flash":  {Input: 0.3, Output: 2.5, Cached: 0.075},
	"gpt-5":             {Input: 1.25, Output: 10, Cached: 0.125},
}

const (
	modelsDevCacheTTL = 24 * time.Hour
	modelsDevMaxBytes = 32 << 20
	modelsDevTimeout  = 30 * time.Second
)

// modelsDevURL and modelsDevHTTP are variables so tests can fake the catalog.
var (
	modelsDevURL  = "https://models.dev/api.json"
	modelsDevHTTP = &http.Client{Timeout: modelsDevTimeout}
)

// DefaultPricing returns the built-in fallback table.
func DefaultPricing() *Pricing {
	return &Pricing{prices: clonePrices(defaultPrices), source: "default"}
}

// DefaultPricingCachePath returns ~/.seavx/cache/models.json, falling back to
// .seavx/cache/models.json when no home directory is available.
func DefaultPricingCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".seavx", "cache", "models.json")
	}
	return filepath.Join(home, ".seavx", "cache", "models.json")
}

// ModelsDevPricing loads model prices from the models.dev catalog. The raw
// catalog is cached at cachePath (empty = DefaultPricingCachePath) and reused
// for a day before refetching; an unreachable catalog falls back to the stale
// cache and then to the built-in defaults at the caller. Built-in defaults
// backfill families the catalog does not price.
func ModelsDevPricing(cachePath string) (*Pricing, error) {
	if cachePath == "" {
		cachePath = DefaultPricingCachePath()
	}
	data, err := loadModelsDevCatalog(cachePath)
	if err != nil {
		return nil, err
	}
	catalog, err := parseModelsDev(data)
	if err != nil {
		return nil, err
	}
	merged := clonePrices(defaultPrices)
	for k, v := range catalog {
		merged[k] = v
	}
	return &Pricing{prices: merged, source: "models.dev"}, nil
}

func loadModelsDevCatalog(cachePath string) ([]byte, error) {
	cached, cacheErr := os.ReadFile(cachePath)
	if cacheErr == nil {
		if st, err := os.Stat(cachePath); err == nil && time.Since(st.ModTime()) < modelsDevCacheTTL {
			return cached, nil
		}
	}
	data, err := fetchModelsDevCatalog()
	if err != nil {
		if cacheErr == nil {
			slog.Warn("models.dev fetch failed; using cached prices", "cache", cachePath, "err", err)
			return cached, nil
		}
		return nil, err
	}
	if err := writePricingCache(cachePath, data); err != nil {
		slog.Warn("cannot write pricing cache", "cache", cachePath, "err", err)
	}
	return data, nil
}

func fetchModelsDevCatalog() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsDevTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "seavxrouter")
	resp, err := modelsDevHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models.dev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, modelsDevMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading models.dev: %w", err)
	}
	if len(data) > modelsDevMaxBytes {
		return nil, fmt.Errorf("models.dev catalog exceeds %d bytes", modelsDevMaxBytes)
	}
	return data, nil
}

func writePricingCache(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".models-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// modelsDevProvider is the subset of the models.dev catalog used for pricing.
type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	Cost *modelsDevCost `json:"cost"`
}

type modelsDevCost struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	CacheRead float64 `json:"cache_read"`
}

// modelsDevProviderPriority keeps first-party list prices ahead of resellers
// when several providers list the same model ID.
var modelsDevProviderPriority = []string{
	"anthropic", "openai", "google", "google-vertex",
	"deepseek", "alibaba", "moonshotai", "zhipuai", "minimax",
	"xai", "meta", "mistral",
}

func parseModelsDev(data []byte) (map[string]Price, error) {
	var providers map[string]modelsDevProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("parsing models.dev catalog: %w", err)
	}
	out := make(map[string]Price, len(providers)*4)
	for _, name := range orderedProviders(providers) {
		for id, model := range providers[name].Models {
			cost := model.Cost
			if cost == nil || (cost.Input <= 0 && cost.Output <= 0) {
				continue
			}
			key := normalizePriceKey(id)
			if _, exists := out[key]; exists {
				continue
			}
			out[key] = Price{Input: cost.Input, Output: cost.Output, Cached: cost.CacheRead}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models.dev catalog has no priced models")
	}
	return out, nil
}

func orderedProviders(all map[string]modelsDevProvider) []string {
	seen := make(map[string]bool, len(all))
	out := make([]string, 0, len(all))
	for _, name := range modelsDevProviderPriority {
		if _, ok := all[name]; ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	rest := make([]string, 0, len(all))
	for name := range all {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// LoadPricing reads a JSON object of model -> {input, output, cached} prices
// (USD per 1M tokens) and merges it over the built-in defaults.
func LoadPricing(path string) (*Pricing, error) {
	return DefaultPricing().MergeFile(path)
}

// MergeFile reads path and returns a new Pricing with its entries overriding p.
func (p *Pricing) MergeFile(path string) (*Pricing, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pricing: %w", err)
	}
	var file map[string]Price
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing pricing JSON: %w", err)
	}
	base := p
	if base == nil || len(base.prices) == 0 {
		base = DefaultPricing()
	}
	merged := make(map[string]Price, len(base.prices)+len(file))
	for k, v := range base.prices {
		merged[normalizePriceKey(k)] = v
	}
	for k, v := range file {
		merged[normalizePriceKey(k)] = v
	}
	return &Pricing{prices: merged, source: path}, nil
}

// Cost estimates the USD cost of one request's usage. Cached input tokens are
// billed at the cached rate and excluded from the full input rate.
func (p *Pricing) Cost(model string, u backend.Usage) float64 {
	price, ok := p.lookup(model)
	if !ok {
		return 0
	}
	cached := u.CacheReadTokens
	if cached > u.InputTokens {
		cached = u.InputTokens
	}
	uncached := u.InputTokens - cached
	return float64(uncached)*price.Input/1e6 +
		float64(cached)*price.Cached/1e6 +
		float64(u.OutputTokens)*price.Output/1e6
}

var priceProviderPrefixes = []string{
	"codex/", "antigravity/", "codebuddy/", "qoder/", "kiro/",
}

func (p *Pricing) lookup(model string) (Price, bool) {
	if p == nil || len(p.prices) == 0 {
		return Price{}, false
	}
	m := normalizePriceKey(stripProviderPrefix(model))
	bestKey := ""
	var best Price
	for k, v := range p.prices {
		if len(k) > len(bestKey) && strings.HasPrefix(m, k) {
			bestKey, best = k, v
		}
	}
	return best, bestKey != ""
}

func stripProviderPrefix(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range priceProviderPrefixes {
		if strings.HasPrefix(m, prefix) {
			return m[len(prefix):]
		}
	}
	return m
}

// normalizePriceKey folds dotted aliases into the dashed IDs used by the
// catalog, so claude-sonnet-4.5 and claude-sonnet-4-5 match one entry.
func normalizePriceKey(id string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), ".", "-")
}

func clonePrices(in map[string]Price) map[string]Price {
	out := make(map[string]Price, len(in))
	for k, v := range in {
		out[normalizePriceKey(k)] = v
	}
	return out
}
