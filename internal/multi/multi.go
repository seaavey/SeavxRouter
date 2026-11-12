package multi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/codebuddy"
)

// MultiBackend routes requests across providers. backend.Model IDs may carry an
// explicit "codex/", "antigravity/", "codebuddy/" or "qoder/" prefix; bare IDs
// go to codex only when codex lists that model, otherwise to antigravity.
type MultiBackend struct {
	Antigravity backend.BackendAPI
	Codex       backend.BackendAPI
	CodeBuddy   backend.BackendAPI
	Qoder       backend.BackendAPI
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
	b, id, err := m.resolve(ctx, model)
	if err != nil {
		return nil, err
	}
	r := *req
	r.Model = id
	return b.Chat(ctx, &r, onDelta)
}

// Provider reports which provider would serve model, mirroring resolve.
func (m *MultiBackend) Provider(ctx context.Context, model string) string {
	if name, _, ok := strings.Cut(model, "/"); ok {
		switch name {
		case "codex", "antigravity", "codebuddy", "qoder":
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
	return ""
}

type quotaReporter interface {
	Quota() auth.ProviderQuota
}

// Quota aggregates per-account quota across providers that expose it.
func (m *MultiBackend) Quota() []auth.ProviderQuota {
	out := []auth.ProviderQuota{}
	for _, b := range []backend.BackendAPI{m.Antigravity, m.Codex, m.CodeBuddy, m.Qoder} {
		if b == nil {
			continue
		}
		if q, ok := b.(quotaReporter); ok {
			out = append(out, q.Quota())
		}
	}
	return out
}
