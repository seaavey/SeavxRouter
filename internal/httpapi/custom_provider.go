package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// CustomProviderService is the operator-facing surface for adding a custom
// provider from the dashboard. It is implemented by the multi backend so the
// credentials file and the live routing table are updated once, in one place.
//
// Validation always means one thing: fetch the upstream model catalog with the
// supplied key. A key that cannot list models gives the router nothing to route
// to, so it is rejected before anything is stored.
type CustomProviderService interface {
	ProbeCustomProvider(ctx context.Context, name, baseURL, sdk, key string) ([]string, error)
	AddCustomProvider(ctx context.Context, name, baseURL, sdk, key string) ([]string, error)
}

type customProviderForm struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	SDK     string `json:"sdk"`
	APIKey  string `json:"api_key"`
}

// readCustomProviderForm decodes and normalizes the shared add/validate body.
func readCustomProviderForm(w http.ResponseWriter, r *http.Request) (name, base, sdk, key string, ok bool) {
	var form customProviderForm
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&form); err != nil {
		dashboardError(w, http.StatusBadRequest, "Send a JSON body with name, base_url, sdk and api_key.")
		return "", "", "", "", false
	}
	name, err := auth.NormalizeCustomProviderName(form.Name)
	if err != nil {
		dashboardError(w, http.StatusBadRequest, err.Error())
		return "", "", "", "", false
	}
	base, err = auth.NormalizeCustomBaseURL(form.BaseURL)
	if err != nil {
		dashboardError(w, http.StatusBadRequest, err.Error())
		return "", "", "", "", false
	}
	sdk, err = auth.NormalizeSDK(form.SDK)
	if err != nil {
		dashboardError(w, http.StatusBadRequest, err.Error())
		return "", "", "", "", false
	}
	key = strings.TrimSpace(form.APIKey)
	if key == "" {
		dashboardError(w, http.StatusBadRequest, "An API key is required.")
		return "", "", "", "", false
	}
	if len(key) > 4096 {
		dashboardError(w, http.StatusBadRequest, "That API key is too long.")
		return "", "", "", "", false
	}
	return name, base, sdk, key, true
}

// handleProbeCustomProvider checks a key against the upstream model catalog
// without storing it, so the dialog can show what the provider serves before
// the operator commits.
func (s *Server) handleProbeCustomProvider(w http.ResponseWriter, r *http.Request) {
	if s.customProviders == nil {
		dashboardError(w, http.StatusNotImplemented, "Provider validation is not available.")
		return
	}
	name, base, sdk, key, ok := readCustomProviderForm(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	models, err := s.customProviders.ProbeCustomProvider(ctx, name, base, sdk, key)
	if err != nil {
		writeCustomProviderError(w, err)
		return
	}
	writeJSON(w, map[string]any{"name": name, "base_url": base, "sdk": sdk, "models": models})
}

// handleAddCustomProvider validates the key against the upstream's model
// catalog before storing anything, then activates the provider live.
func (s *Server) handleAddCustomProvider(w http.ResponseWriter, r *http.Request) {
	if s.customProviders == nil {
		dashboardError(w, http.StatusNotImplemented, "Adding providers from the dashboard is not available.")
		return
	}
	name, base, sdk, key, ok := readCustomProviderForm(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	models, err := s.customProviders.AddCustomProvider(ctx, name, base, sdk, key)
	if err != nil {
		writeCustomProviderError(w, err)
		return
	}
	writeJSON(w, map[string]any{"name": name, "base_url": base, "sdk": sdk, "models": models})
}

// writeCustomProviderError keeps the upstream HTTP status so the dialog can say
// whether the key was rejected or the endpoint did not answer.
func writeCustomProviderError(w http.ResponseWriter, err error) {
	body := map[string]any{"message": err.Error()}
	var upstreamErr *backend.HTTPError
	if errors.As(err, &upstreamErr) && upstreamErr.Status >= 400 && upstreamErr.Status <= 599 {
		body["upstream_status"] = upstreamErr.Status
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}
