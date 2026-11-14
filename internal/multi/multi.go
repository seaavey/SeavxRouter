package multi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/providers/codebuddy"
	"seavxrouter/internal/providers/kiro"
)

// MultiBackend routes requests across providers. backend.Model IDs may carry an
// explicit "codex/", "antigravity/", "codebuddy/", "qoder/", "kiro/" or
// "verboo/", "nvidia/", "groq/" or "cloudflare/" prefix; bare IDs go to codex only when codex lists that model,
// otherwise to antigravity.
type MultiBackend struct {
	Antigravity backend.BackendAPI
	Codex       backend.BackendAPI
	CodeBuddy   backend.BackendAPI
	Qoder       backend.BackendAPI
	Kiro        backend.BackendAPI
	Verboo      backend.BackendAPI
	NVIDIA      backend.BackendAPI
	Groq        backend.BackendAPI
	Cloudflare  backend.BackendAPI
}

func (m *MultiBackend) Default() string {
	if m.Antigravity != nil {
		return m.Antigravity.Default()
	}
	if m.Codex != nil {
		return m.Codex.Default()
	}
	if m.CodeBuddy != nil {
		return m.CodeBuddy.Default()
	}
	if m.Qoder != nil {
		return m.Qoder.Default()
	}
	if m.Kiro != nil {
		return m.Kiro.Default()
	}
	if m.Verboo != nil {
		return m.Verboo.Default()
	}
	if m.NVIDIA != nil {
		return m.NVIDIA.Default()
	}
	if m.Groq != nil {
		return m.Groq.Default()
	}
	if m.Cloudflare != nil {
		return m.Cloudflare.Default()
	}
	return ""
}

// normalizeModel maps common user-facing aliases to canonical backend model
// IDs. The "gemini." separator is treated like "gemini-" so dotted and dashed
// spellings resolve alike; version dots (e.g. 3.8) are preserved.
func normalizeModel(model string) string {
	m := model
	if rest, ok := strings.CutPrefix(m, "gemini."); ok {
		m = "gemini-" + rest
	}
	switch m {
	case "gemini-3.8-flash", "gemini-3.8-flash-preview", "gemini-flash":
		return "gemini-3.8-flash-high"
	case "gemini-2.5-flash":
		return "gemini-2.5-flash"
	case "gemini-2.5-pro":
		return "gemini-2.5-pro"
	}
	switch {
	case strings.HasPrefix(m, "claude-3-7-sonnet"), strings.HasPrefix(m, "claude-3-5-sonnet"):
		return "claude-sonnet-4-6"
	case strings.HasPrefix(m, "claude-3-opus"):
		return "claude-opus-4-6-thinking"
	}
	return m
}

func (m *MultiBackend) resolve(ctx context.Context, model string) (backend.BackendAPI, string, error) {
	if name, id, ok := strings.Cut(model, "/"); ok {
		switch name {
		case "codex":
			if m.Codex == nil {
				return nil, "", fmt.Errorf("codex provider not configured; run: seavxrouter login codex")
			}
			return m.Codex, normalizeModel(id), nil
		case "antigravity":
			if m.Antigravity == nil {
				return nil, "", fmt.Errorf("antigravity provider not configured; run: seavxrouter login")
			}
			return m.Antigravity, normalizeModel(id), nil
		case "codebuddy":
			if m.CodeBuddy == nil {
				return nil, "", fmt.Errorf("codebuddy provider not configured; run: seavxrouter login codebuddy")
			}
			return m.CodeBuddy, codebuddy.ModelID(id), nil
		case "qoder":
			if m.Qoder == nil {
				return nil, "", fmt.Errorf("qoder provider not configured; run: seavxrouter login qoder")
			}
			return m.Qoder, id, nil
		case "kiro":
			if m.Kiro == nil {
				return nil, "", fmt.Errorf("kiro provider not configured; run: seavxrouter login kiro")
			}
			return m.Kiro, kiro.ModelID(id), nil
		case "verboo":
			if m.Verboo == nil {
				return nil, "", fmt.Errorf("verboo provider not configured; run: seavxrouter login verboo")
			}
			return m.Verboo, id, nil
		case "nvidia":
			if m.NVIDIA == nil {
				return nil, "", fmt.Errorf("nvidia provider not configured; run: seavxrouter login nvidia")
			}
			return m.NVIDIA, id, nil
		case "groq":
			if m.Groq == nil {
				return nil, "", fmt.Errorf("groq provider not configured; run: seavxrouter login groq")
			}
			return m.Groq, id, nil
		case "cloudflare", "cf":
			if m.Cloudflare == nil {
				return nil, "", fmt.Errorf("cloudflare provider not configured; run: seavxrouter login cloudflare")
			}
			return m.Cloudflare, id, nil
		}
	}
	model = normalizeModel(model)
	if m.Codex != nil && model != "" && m.Codex.HasModel(ctx, model) {
		return m.Codex, model, nil
	}
	if m.Antigravity != nil {
		return m.Antigravity, model, nil
	}
	if m.Codex != nil {
		return m.Codex, model, nil
	}
	if m.CodeBuddy != nil {
		return m.CodeBuddy, model, nil
	}
	if m.Qoder != nil {
		return m.Qoder, model, nil
	}
	if m.Kiro != nil {
		return m.Kiro, model, nil
	}
	if m.Verboo != nil {
		return m.Verboo, model, nil
	}
	if m.NVIDIA != nil {
		return m.NVIDIA, model, nil
	}
	if m.Groq != nil {
		return m.Groq, model, nil
	}
	if m.Cloudflare != nil {
		return m.Cloudflare, model, nil
	}
	return nil, "", fmt.Errorf("no providers configured")
}

func (m *MultiBackend) Models(ctx context.Context) ([]backend.Model, error) {
	out := []backend.Model{}
	seen := map[string]bool{}
	var lastErr error
	if m.Antigravity != nil {
		models, err := m.Antigravity.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("antigravity models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, mo)
			seen[mo.ID] = true
		}
	}
	if m.Codex != nil {
		models, err := m.Codex.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("codex models unavailable", "err", err)
		}
		for _, mo := range models {
			id := mo.ID
			if seen[id] {
				id = "codex/" + id
			}
			out = append(out, backend.Model{ID: id, Name: mo.Name})
		}
	}
	if m.CodeBuddy != nil {
		models, err := m.CodeBuddy.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("codebuddy models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, backend.Model{ID: "codebuddy/" + mo.ID, Name: mo.Name})
		}
	}
	if m.Qoder != nil {
		models, err := m.Qoder.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("qoder models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, backend.Model{ID: "qoder/" + mo.ID, Name: mo.Name})
		}
	}
	if m.Kiro != nil {
		models, err := m.Kiro.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("kiro models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, backend.Model{ID: "kiro/" + mo.ID, Name: mo.Name})
		}
	}
	if m.Verboo != nil {
		models, err := m.Verboo.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("verboo models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, backend.Model{ID: "verboo/" + mo.ID, Name: mo.Name})
		}
	}
	for _, p := range []struct {
		name    string
		backend backend.BackendAPI
	}{
		{"nvidia", m.NVIDIA},
		{"groq", m.Groq},
		{"cloudflare", m.Cloudflare},
	} {
		if p.backend == nil {
			continue
		}
		models, err := p.backend.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn(p.name+" models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, backend.Model{ID: p.name + "/" + mo.ID, Name: mo.Name})
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func (m *MultiBackend) HasModel(ctx context.Context, id string) bool {
	b, model, err := m.resolve(ctx, id)
	if err != nil {
		return false
	}
	return b.HasModel(ctx, model)
}

func (m *MultiBackend) Complete(ctx context.Context, model, prompt string) (*backend.Result, error) {
	if model == "" {
		model = m.Default()
	}
	b, id, err := m.resolve(ctx, model)
	if err != nil {
		return nil, err
	}
	return b.Complete(ctx, id, prompt)
}

func (m *MultiBackend) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
	if model == "" {
		model = m.Default()
	}
	b, id, err := m.resolve(ctx, model)
	if err != nil {
		return nil, err
	}
	return b.Stream(ctx, id, prompt, onDelta)
}

func (m *MultiBackend) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	model := req.Model
	if model == "" {
		model = m.Default()
	}
	primary, id, err := m.resolve(ctx, model)
	if err != nil {
		return nil, err
	}
	var started bool
	tracked := func(d backend.ChatDelta) {
		if d.Content != "" || len(d.ToolCalls) > 0 {
			started = true
		}
		if onDelta != nil {
			onDelta(d)
		}
	}
	call := func(b backend.BackendAPI, modelID string) (*backend.ChatResult, error) {
		r := *req
		r.Model = modelID
		return b.Chat(ctx, &r, tracked)
	}

	result, err := call(primary, id)
	if err == nil {
		return result, nil
	}
	if started || !backend.IsRetryableError(err) || ctx.Err() != nil {
		return nil, err
	}
	lastErr := err
	slog.Warn("provider request failed, attempting fallback", "model", id, "err", err)

	// Quota failures already rotated through every account inside the backend;
	// an immediate retry would only burn latency.
	if !backend.IsQuotaError(err) {
		if !sleepCtx(ctx, retryBackoff) {
			return nil, lastErr
		}
		result, err = call(primary, id)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if started || !backend.IsRetryableError(err) || ctx.Err() != nil {
			return nil, lastErr
		}
	}

	primaryErr := lastErr
	tried := map[backend.BackendAPI]bool{primary: true}
	for _, p := range m.slots() {
		if p.backend == nil || tried[p.backend] {
			continue
		}
		altID := p.modelID(id)
		if altID == "" || !p.backend.HasModel(ctx, altID) {
			continue
		}
		tried[p.backend] = true
		result, err := call(p.backend, altID)
		if err == nil {
			slog.Info("failover succeeded", "provider", p.name, "model", altID)
			return result, nil
		}
		lastErr = err
		if started || ctx.Err() != nil {
			return nil, lastErr
		}
		if !backend.IsRetryableError(err) {
			// A provider can claim a model via a fail-open HasModel and then
			// reject it; that must not end the fallback chain.
			slog.Warn("fallback provider rejected the model", "provider", p.name, "model", altID, "err", err)
			continue
		}
		slog.Warn("fallback provider failed", "provider", p.name, "model", altID, "err", err)
	}

	// The primary is quota- or auth-dead and no provider served the requested
	// model. Give every remaining provider one try with its own default model
	// so the request is still answered elsewhere.
	if backend.IsQuotaError(primaryErr) || backend.IsAuthError(primaryErr) {
		for _, p := range m.slots() {
			if p.backend == nil || p.backend == primary {
				continue
			}
			def := p.backend.Default()
			if def == "" {
				continue
			}
			result, err := call(p.backend, def)
			if err == nil {
				slog.Info("last-resort fallback succeeded", "provider", p.name, "model", def)
				return result, nil
			}
			lastErr = err
			if started || ctx.Err() != nil {
				return nil, lastErr
			}
			slog.Warn("last-resort fallback failed", "provider", p.name, "model", def, "err", err)
		}
	}
	return nil, lastErr
}

// retryBackoff is the pause before retrying a transient, non-quota failure on
// the primary provider.
var retryBackoff = 300 * time.Millisecond

type providerSlot struct {
	name    string
	backend backend.BackendAPI
	modelID func(string) string
}

func identityModel(id string) string { return id }

func (m *MultiBackend) slots() []providerSlot {
	return []providerSlot{
		{name: "antigravity", backend: m.Antigravity, modelID: normalizeModel},
		{name: "codex", backend: m.Codex, modelID: normalizeModel},
		{name: "codebuddy", backend: m.CodeBuddy, modelID: codebuddy.ModelID},
		{name: "qoder", backend: m.Qoder, modelID: identityModel},
		{name: "kiro", backend: m.Kiro, modelID: kiro.ModelID},
		{name: "verboo", backend: m.Verboo, modelID: identityModel},
		{name: "nvidia", backend: m.NVIDIA, modelID: identityModel},
		{name: "groq", backend: m.Groq, modelID: identityModel},
		{name: "cloudflare", backend: m.Cloudflare, modelID: identityModel},
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Provider reports which provider would serve model, mirroring resolve.
func (m *MultiBackend) Provider(ctx context.Context, model string) string {
	if name, _, ok := strings.Cut(model, "/"); ok {
		switch name {
		case "cloudflare":
			return "cloudflare"
		case "codex", "antigravity", "codebuddy", "qoder", "kiro", "verboo", "nvidia", "groq":
			return name
		}
	}
	model = normalizeModel(model)
	if m.Codex != nil && model != "" && m.Codex.HasModel(ctx, model) {
		return "codex"
	}
	if m.Antigravity != nil {
		return "antigravity"
	}
	if m.Codex != nil {
		return "codex"
	}
	if m.CodeBuddy != nil {
		return "codebuddy"
	}
	if m.Qoder != nil {
		return "qoder"
	}
	if m.Kiro != nil {
		return "kiro"
	}
	if m.Verboo != nil {
		return "verboo"
	}
	if m.NVIDIA != nil {
		return "nvidia"
	}
	if m.Groq != nil {
		return "groq"
	}
	if m.Cloudflare != nil {
		return "cloudflare"
	}
	return ""
}

type quotaReporter interface {
	Quota() auth.ProviderQuota
}

// Quota aggregates per-account quota across providers that expose it.
func (m *MultiBackend) Quota() []auth.ProviderQuota {
	out := []auth.ProviderQuota{}
	for _, b := range []backend.BackendAPI{m.Antigravity, m.Codex, m.CodeBuddy, m.Qoder, m.Kiro, m.Verboo, m.NVIDIA, m.Groq, m.Cloudflare} {
		if b == nil {
			continue
		}
		if q, ok := b.(quotaReporter); ok {
			out = append(out, q.Quota())
		}
	}
	return out
}
