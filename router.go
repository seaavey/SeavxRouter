package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// MultiBackend routes requests across providers. Model IDs may carry an
// explicit "codex/" or "antigravity/" prefix; bare IDs go to codex only when
// codex lists that model, otherwise to antigravity.
type MultiBackend struct {
	antigravity BackendAPI
	codex       BackendAPI
}

func (m *MultiBackend) Default() string {
	if m.antigravity != nil {
		return m.antigravity.Default()
	}
	if m.codex != nil {
		return m.codex.Default()
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

func (m *MultiBackend) resolve(ctx context.Context, model string) (BackendAPI, string, error) {
	if name, id, ok := strings.Cut(model, "/"); ok {
		switch name {
		case "codex":
			if m.codex == nil {
				return nil, "", fmt.Errorf("codex provider not configured; run: seavxrouter login codex")
			}
			return m.codex, normalizeModel(id), nil
		case "antigravity":
			if m.antigravity == nil {
				return nil, "", fmt.Errorf("antigravity provider not configured; run: seavxrouter login")
			}
			return m.antigravity, normalizeModel(id), nil
		}
	}
	model = normalizeModel(model)
	if m.codex != nil && model != "" && m.codex.HasModel(ctx, model) {
		return m.codex, model, nil
	}
	if m.antigravity != nil {
		return m.antigravity, model, nil
	}
	if m.codex != nil {
		return m.codex, model, nil
	}
	return nil, "", fmt.Errorf("no providers configured")
}

func (m *MultiBackend) Models(ctx context.Context) ([]Model, error) {
	out := []Model{}
	seen := map[string]bool{}
	var lastErr error
	if m.antigravity != nil {
		models, err := m.antigravity.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("antigravity models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, mo)
			seen[mo.ID] = true
		}
	}
	if m.codex != nil {
		models, err := m.codex.Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn("codex models unavailable", "err", err)
		}
		for _, mo := range models {
			id := mo.ID
			if seen[id] {
				id = "codex/" + id
			}
			out = append(out, Model{ID: id, Name: mo.Name})
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

func (m *MultiBackend) Complete(ctx context.Context, model, prompt string) (*Result, error) {
	if model == "" {
		model = m.Default()
	}
	b, id, err := m.resolve(ctx, model)
	if err != nil {
		return nil, err
	}
	return b.Complete(ctx, id, prompt)
}

func (m *MultiBackend) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*Result, error) {
	if model == "" {
		model = m.Default()
	}
	b, id, err := m.resolve(ctx, model)
	if err != nil {
		return nil, err
	}
	return b.Stream(ctx, id, prompt, onDelta)
}

func (m *MultiBackend) Chat(ctx context.Context, req *ChatRequest, onDelta func(ChatDelta)) (*ChatResult, error) {
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
