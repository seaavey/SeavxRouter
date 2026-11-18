package multi

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/providers/codebuddy"
	"seavxrouter/internal/providers/kiro"
)

// MultiBackend routes requests across providers. backend.Model IDs may carry an
// explicit provider prefix; custom OpenAI-compatible providers added with
// "seavxrouter provider add" are resolved from Custom. Bare IDs go to codex
// only when codex lists that model, otherwise to antigravity.
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
	OpenCode    backend.BackendAPI
	// Custom holds user-defined providers keyed by their model prefix.
	Custom map[string]backend.BackendAPI
	// Combos holds named fallback lists requested as "combo/<name>".
	Combos *Combos
}

// comboMembers returns the ordered models of a combo/<name> ID.
func (m *MultiBackend) comboMembers(model string) ([]string, bool) {
	name, ok := strings.CutPrefix(model, ComboPrefix)
	if !ok || name == "" {
		return nil, false
	}
	return m.Combos.Get(name)
}

func (m *MultiBackend) custom(name string) (backend.BackendAPI, bool) {
	b, ok := m.Custom[name]
	if !ok || b == nil {
		return nil, false
	}
	return b, true
}

func (m *MultiBackend) customNames() []string {
	if len(m.Custom) == 0 {
		return nil
	}
	names := make([]string, 0, len(m.Custom))
	for name, b := range m.Custom {
		if b != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
	if m.OpenCode != nil {
		return m.OpenCode.Default()
	}
	for _, name := range m.customNames() {
		return m.Custom[name].Default()
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
	b, id, _, err := m.resolveNamed(ctx, model)
	return b, id, err
}

// resolveNamed is resolve plus the provider name, which callers report in
// usage and logs for models served through a combo.
func (m *MultiBackend) resolveNamed(ctx context.Context, model string) (backend.BackendAPI, string, string, error) {
	if name, id, ok := strings.Cut(model, "/"); ok {
		switch name {
		case "codex":
			if m.Codex == nil {
				return nil, "", "", fmt.Errorf("codex provider not configured; run: seavxrouter login codex")
			}
			return m.Codex, normalizeModel(id), name, nil
		case "antigravity":
			if m.Antigravity == nil {
				return nil, "", "", fmt.Errorf("antigravity provider not configured; run: seavxrouter login")
			}
			return m.Antigravity, normalizeModel(id), name, nil
		case "codebuddy":
			if m.CodeBuddy == nil {
				return nil, "", "", fmt.Errorf("codebuddy provider not configured; run: seavxrouter login codebuddy")
			}
			return m.CodeBuddy, codebuddy.ModelID(id), name, nil
		case "qoder":
			if m.Qoder == nil {
				return nil, "", "", fmt.Errorf("qoder provider not configured; run: seavxrouter login qoder")
			}
			return m.Qoder, id, name, nil
		case "kiro":
			if m.Kiro == nil {
				return nil, "", "", fmt.Errorf("kiro provider not configured; run: seavxrouter login kiro")
			}
			return m.Kiro, kiro.ModelID(id), name, nil
		case "verboo":
			if m.Verboo == nil {
				return nil, "", "", fmt.Errorf("verboo provider not configured; run: seavxrouter login verboo")
			}
			return m.Verboo, id, name, nil
		case "nvidia":
			if m.NVIDIA == nil {
				return nil, "", "", fmt.Errorf("nvidia provider not configured; run: seavxrouter login nvidia")
			}
			return m.NVIDIA, id, name, nil
		case "groq":
			if m.Groq == nil {
				return nil, "", "", fmt.Errorf("groq provider not configured; run: seavxrouter login groq")
			}
			return m.Groq, id, name, nil
		case "cloudflare", "cf":
			if m.Cloudflare == nil {
				return nil, "", "", fmt.Errorf("cloudflare provider not configured; run: seavxrouter login cloudflare")
			}
			return m.Cloudflare, id, "cloudflare", nil
		case "opencode":
			if m.OpenCode == nil {
				return nil, "", "", fmt.Errorf("opencode provider not configured; run: seavxrouter login opencode")
			}
			return m.OpenCode, id, name, nil
		}
		if b, ok := m.custom(name); ok {
			return b, id, name, nil
		}
	}
	model = normalizeModel(model)
	if m.Codex != nil && model != "" && m.Codex.HasModel(ctx, model) {
		return m.Codex, model, "codex", nil
	}
	if m.Antigravity != nil {
		return m.Antigravity, model, "antigravity", nil
	}
	if m.Codex != nil {
		return m.Codex, model, "codex", nil
	}
	if m.CodeBuddy != nil {
		return m.CodeBuddy, model, "codebuddy", nil
	}
	if m.Qoder != nil {
		return m.Qoder, model, "qoder", nil
	}
	if m.Kiro != nil {
		return m.Kiro, model, "kiro", nil
	}
	if m.Verboo != nil {
		return m.Verboo, model, "verboo", nil
	}
	if m.NVIDIA != nil {
		return m.NVIDIA, model, "nvidia", nil
	}
	if m.Groq != nil {
		return m.Groq, model, "groq", nil
	}
	if m.Cloudflare != nil {
		return m.Cloudflare, model, "cloudflare", nil
	}
	if m.OpenCode != nil {
		return m.OpenCode, model, "opencode", nil
	}
	if names := m.customNames(); len(names) > 0 {
		return m.Custom[names[0]], model, names[0], nil
	}
	return nil, "", "", fmt.Errorf("no providers configured")
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
		{"opencode", m.OpenCode},
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
	for _, name := range m.customNames() {
		models, err := m.Custom[name].Models(ctx)
		if err != nil {
			lastErr = err
			slog.Warn(name+" models unavailable", "err", err)
		}
		for _, mo := range models {
			out = append(out, backend.Model{ID: name + "/" + mo.ID, Name: mo.Name})
		}
	}
	for _, name := range m.Combos.Names() {
		out = append(out, backend.Model{ID: ComboPrefix + name, Name: ComboPrefix + name})
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func (m *MultiBackend) HasModel(ctx context.Context, id string) bool {
	if _, ok := m.comboMembers(id); ok {
		return true
	}
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
	members, isCombo := m.comboMembers(model)
	if !isCombo {
		b, id, err := m.resolve(ctx, model)
		if err != nil {
			return nil, err
		}
		return b.Complete(ctx, id, prompt)
	}
	var lastErr error
	for _, member := range members {
		b, id, _, err := m.resolveNamed(ctx, member)
		if err != nil {
			slog.Warn("combo member unavailable", "combo", model, "model", member, "err", err)
			lastErr = err
			continue
		}
		result, err := b.Complete(ctx, id, prompt)
		if err == nil {
			return result, nil
		}
		slog.Warn("combo member failed", "combo", model, "model", member, "err", err)
		lastErr = err
		if ctx.Err() != nil || !backend.IsRetryableError(err) {
			return nil, fmt.Errorf("combo %q: %w", comboName(model), err)
		}
	}
	return nil, comboError(model, lastErr)
}

func (m *MultiBackend) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
	if model == "" {
		model = m.Default()
	}
	members, isCombo := m.comboMembers(model)
	if !isCombo {
		b, id, err := m.resolve(ctx, model)
		if err != nil {
			return nil, err
		}
		return b.Stream(ctx, id, prompt, onDelta)
	}
	var lastErr error
	for _, member := range members {
		b, id, _, err := m.resolveNamed(ctx, member)
		if err != nil {
			slog.Warn("combo member unavailable", "combo", model, "model", member, "err", err)
			lastErr = err
			continue
		}
		var started bool
		tracked := func(delta string) {
			started = true
			if onDelta != nil {
				onDelta(delta)
			}
		}
		result, err := b.Stream(ctx, id, prompt, tracked)
		if err == nil {
			return result, nil
		}
		slog.Warn("combo member failed", "combo", model, "model", member, "err", err)
		lastErr = err
		if started || ctx.Err() != nil || !backend.IsRetryableError(err) {
			return nil, fmt.Errorf("combo %q: %w", comboName(model), err)
		}
	}
	return nil, comboError(model, lastErr)
}

// Chat sends one request to the provider that owns the model. Failures never
// hop to another provider: account rotation happens inside the provider, and a
// dead plan returns its error so the client can pick a different model. The
// exception is a combo, whose members are an explicit cross-provider fallback.
func (m *MultiBackend) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	model := req.Model
	if model == "" {
		model = m.Default()
	}
	if members, ok := m.comboMembers(model); ok {
		return m.chatCombo(ctx, model, members, req, onDelta)
	}
	b, id, provider, err := m.resolveNamed(ctx, model)
	if err != nil {
		return nil, err
	}
	var started bool
	tracked := func(d backend.ChatDelta) {
		if d.Content != "" || d.Thinking != "" || len(d.ToolCalls) > 0 {
			started = true
		}
		if onDelta != nil {
			onDelta(d)
		}
	}
	call := func() (*backend.ChatResult, error) {
		r := *req
		r.Model = id
		return b.Chat(ctx, &r, tracked)
	}

	result, err := call()
	if err == nil {
		result.Model, result.Provider = id, provider
		return result, nil
	}
	if started || !backend.IsRetryableError(err) || ctx.Err() != nil {
		return nil, err
	}
	// Quota failures already rotated through every account inside the backend;
	// an immediate retry would only burn latency.
	if backend.IsQuotaError(err) {
		return nil, err
	}
	if !sleepCtx(ctx, retryBackoff) {
		return nil, err
	}
	result, err = call()
	if err != nil {
		return nil, err
	}
	result.Model, result.Provider = id, provider
	return result, nil
}

// chatCombo tries the combo's members in order. A failure before any output
// moves to the next member; after output the stream cannot switch, and a
// non-retryable failure would fail on every member, so both end the request.
func (m *MultiBackend) chatCombo(ctx context.Context, combo string, members []string, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	var lastErr error
	for _, member := range members {
		if strings.HasPrefix(member, ComboPrefix) {
			lastErr = fmt.Errorf("combo %q cannot contain combo %q", combo, member)
			slog.Warn("combo member is another combo", "combo", combo, "model", member)
			continue
		}
		b, id, provider, err := m.resolveNamed(ctx, member)
		if err != nil {
			slog.Warn("combo member unavailable", "combo", combo, "model", member, "err", err)
			lastErr = err
			continue
		}
		var started bool
		tracked := func(d backend.ChatDelta) {
			if d.Content != "" || d.Thinking != "" || len(d.ToolCalls) > 0 {
				started = true
			}
			if onDelta != nil {
				onDelta(d)
			}
		}
		r := *req
		r.Model = id
		result, err := b.Chat(ctx, &r, tracked)
		if err == nil {
			result.Model, result.Provider = id, provider
			return result, nil
		}
		slog.Warn("combo member failed", "combo", combo, "model", member, "err", err)
		lastErr = err
		if started || ctx.Err() != nil || !backend.IsRetryableError(err) {
			return nil, fmt.Errorf("combo %q: %w", comboName(combo), err)
		}
	}
	return nil, comboError(combo, lastErr)
}

func comboName(model string) string {
	return strings.TrimPrefix(model, ComboPrefix)
}

func comboError(combo string, err error) error {
	if err == nil {
		return fmt.Errorf("combo %q has no usable members", comboName(combo))
	}
	return fmt.Errorf("combo %q exhausted: %w", comboName(combo), err)
}

// retryBackoff is the pause before retrying a transient, non-quota failure on
// the same provider.
var retryBackoff = 300 * time.Millisecond

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

// Provider reports which provider would serve model, mirroring resolve. Combo
// IDs report ComboOwner because the router builds the fallback list itself; the
// concrete provider that answered a request travels on the ChatResult instead.
func (m *MultiBackend) Provider(ctx context.Context, model string) string {
	if _, ok := m.comboMembers(model); ok {
		return ComboOwner
	}
	if name, _, ok := strings.Cut(model, "/"); ok {
		switch name {
		case "cloudflare", "opencode":
			return name
		case "codex", "antigravity", "codebuddy", "qoder", "kiro", "verboo", "nvidia", "groq":
			return name
		}
		if _, ok := m.custom(name); ok {
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
	if m.OpenCode != nil {
		return "opencode"
	}
	if names := m.customNames(); len(names) > 0 {
		return names[0]
	}
	return ""
}

type quotaReporter interface {
	Quota() auth.ProviderQuota
}

// Quota aggregates per-account quota across providers that expose it.
func (m *MultiBackend) Quota() []auth.ProviderQuota {
	out := []auth.ProviderQuota{}
	for _, b := range []backend.BackendAPI{m.Antigravity, m.Codex, m.CodeBuddy, m.Qoder, m.Kiro, m.Verboo, m.NVIDIA, m.Groq, m.Cloudflare, m.OpenCode} {
		if b == nil {
			continue
		}
		if q, ok := b.(quotaReporter); ok {
			out = append(out, q.Quota())
		}
	}
	for _, name := range m.customNames() {
		if q, ok := m.Custom[name].(quotaReporter); ok {
			out = append(out, q.Quota())
		}
	}
	return out
}
