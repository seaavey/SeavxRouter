package multi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/providers/upstream"
)

// ProbeCustomProvider validates an API key by fetching the upstream model
// catalog and returns the model IDs it publishes. Nothing is stored: this backs
// the dashboard's validate step, so an operator can see the catalog before
// committing.
func (m *MultiBackend) ProbeCustomProvider(ctx context.Context, name, baseURL, sdk, key string) ([]string, error) {
	name, err := auth.NormalizeCustomProviderName(name)
	if err != nil {
		return nil, err
	}
	base, err := auth.NormalizeCustomBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	sdk, err = auth.NormalizeSDK(sdk)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("an API key is required")
	}
	return upstream.ProbeCatalog(ctx, name, base, key, sdk)
}

// AddCustomProvider validates the key against the upstream model catalog,
// persists the credential and activates the provider in the live routing table.
// Validation runs before the write, so a key that cannot list models never
// reaches auth.jsonc.
func (m *MultiBackend) AddCustomProvider(ctx context.Context, name, baseURL, sdk, key string) ([]string, error) {
	name, err := auth.NormalizeCustomProviderName(name)
	if err != nil {
		return nil, err
	}
	base, err := auth.NormalizeCustomBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	sdk, err = auth.NormalizeSDK(sdk)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("an API key is required")
	}
	if m.Store.Path == "" {
		return nil, fmt.Errorf("provider storage is not configured")
	}
	if _, exists := m.custom(name); exists {
		return nil, fmt.Errorf("provider %q already exists; remove it first", name)
	}

	// Conflicting base URLs for an existing name would silently move traffic,
	// so an existing provider must be removed explicitly.
	if existing, err := m.Store.Load(); err == nil {
		for _, a := range existing {
			if a.Provider == name && a.BaseURL != base {
				return nil, fmt.Errorf("provider %q is already stored at %s; remove it first", name, a.BaseURL)
			}
		}
	}

	models, err := upstream.ProbeCatalog(ctx, name, base, key, sdk)
	if err != nil {
		return nil, err
	}

	acct := &auth.Auth{Provider: name, AccessToken: key, BaseURL: base, SDK: sdk}
	if err := m.Store.Save([]*auth.Auth{acct}); err != nil {
		return nil, fmt.Errorf("cannot save credentials: %w", err)
	}

	engine, err := upstream.NewEngineSDK(name, base, sdk, m.Store, []*auth.Auth{acct})
	if err != nil {
		return models, fmt.Errorf("cannot start provider: %w", err)
	}
	m.customMu.Lock()
	if m.Custom == nil {
		m.Custom = map[string]backend.BackendAPI{}
	}
	m.Custom[name] = engine
	m.customMu.Unlock()
	slog.Info("custom provider added", "provider", name, "base_url", base, "sdk", sdk, "models", len(models))
	return models, nil
}
