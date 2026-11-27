package multi

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/httpapi"
	"seavxrouter/internal/providers/codebuddy"
	"seavxrouter/internal/providers/kiro"
	"seavxrouter/internal/providers/upstream"
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
	// Store is where custom providers are persisted. When empty, runtime
	// provider additions are refused.
	Store       auth.AuthStore
	customMu    sync.RWMutex
	modelStates *httpapi.ProviderModelManager
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
	m.customMu.RLock()
	defer m.customMu.RUnlock()
	b, ok := m.Custom[name]
	if !ok || b == nil {
		return nil, false
	}
	return b, true
}

func (m *MultiBackend) customNames() []string {
	m.customMu.RLock()
	defer m.customMu.RUnlock()
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
	b, id, provider, err := m.resolveNamedUnchecked(ctx, model)
	if err == nil && m.modelStates != nil && m.modelStates.Disabled(provider, id) {
		return nil, "", "", fmt.Errorf("model_not_found: model is disabled")
	}
	return b, id, provider, err
}

func (m *MultiBackend) resolveNamedUnchecked(ctx context.Context, model string) (backend.BackendAPI, string, string, error) {
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

func (m *MultiBackend) ProviderModels(ctx context.Context, name string) ([]backend.Model, error) {
	b, ok := m.providerBackend(name)
	if !ok {
		return nil, fmt.Errorf("provider not configured")
	}
	models, err := b.Models(ctx)
	if err != nil {
		return nil, err
	}
	if m.modelStates == nil {
		return models, nil
	}
	return m.modelStates.ProviderModels(ctx, name, models)
}
func (m *MultiBackend) ProbeModel(ctx context.Context, provider, id string) error {
	b, ok := m.providerBackend(provider)
	if !ok {
		return fmt.Errorf("provider not configured")
	}
	probe, ok := b.(interface {
		ProbeChat(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("verified add unsupported for this provider")
	}
	return probe.ProbeChat(ctx, id)
}

func (m *MultiBackend) SetModelManager(manager *httpapi.ProviderModelManager) {
	m.modelStates = manager
}
func (m *MultiBackend) AddModel(ctx context.Context, p, id string) error {
	if m.modelStates == nil {
		return fmt.Errorf("model management unavailable")
	}
	b, ok := m.providerBackend(p)
	if !ok {
		return fmt.Errorf("provider not configured")
	}
	if _, ok := b.(*upstream.Engine); !ok {
		return fmt.Errorf("verified add unsupported for this provider")
	}
	return m.modelStates.AddModel(ctx, p, id)
}
func (m *MultiBackend) DeleteModel(ctx context.Context, p, id string) error {
	if m.modelStates == nil {
		return fmt.Errorf("model management unavailable")
	}
	return m.modelStates.DeleteModel(ctx, p, id)
}
func (m *MultiBackend) providerBackend(name string) (backend.BackendAPI, bool) {
	switch name {
	case "antigravity":
		return m.Antigravity, m.Antigravity != nil
	case "codex":
		return m.Codex, m.Codex != nil
	case "codebuddy":
		return m.CodeBuddy, m.CodeBuddy != nil
	case "qoder":
		return m.Qoder, m.Qoder != nil
	case "kiro":
		return m.Kiro, m.Kiro != nil
	case "verboo":
		return m.Verboo, m.Verboo != nil
	case "nvidia":
		return m.NVIDIA, m.NVIDIA != nil
	case "groq":
		return m.Groq, m.Groq != nil
	case "cloudflare":
		return m.Cloudflare, m.Cloudflare != nil
	case "opencode":
		return m.OpenCode, m.OpenCode != nil
	default:
		b, ok := m.custom(name)
		return b, ok
	}
}

// providerList is one provider's slot in the parallel catalog aggregation.
// Results land in slot order, so the response keeps its historical ordering.
type providerList struct {
	name    string
	backend backend.BackendAPI
}

func (m *MultiBackend) Models(ctx context.Context) ([]backend.Model, error) {
	lists := m.providerLists()
	fetched := make([][]backend.Model, len(lists))
	errs := make([]error, len(lists))

	var wg sync.WaitGroup
	for i, p := range lists {
		wg.Add(1)
		go func(i int, p providerList) {
			defer wg.Done()
			// WithoutCancel keeps a client disconnect from aborting a fetch whose
			// result still serves the shared catalog cache.
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backend.DefaultFetchTimeout)
			defer cancel()
			fetched[i], errs[i] = m.ProviderModels(cctx, p.name)
		}(i, p)
	}
	wg.Wait()

	out := []backend.Model{}
	seen := map[string]bool{}
	var lastErr error
	for i, p := range lists {
		if errs[i] != nil {
			lastErr = errs[i]
			slog.Warn(p.name+" models unavailable", "err", errs[i])
		}
		for _, mo := range fetched[i] {
			// antigravity and codex advertise bare IDs; codex is only prefixed when
			// antigravity already claimed the same ID. Every other provider is
			// always namespaced so its IDs stay unambiguous.
			id := mo.ID
			switch {
			case p.name == "antigravity":
			case p.name == "codex":
				if seen[id] {
					id = "codex/" + id
				}
			default:
				id = p.name + "/" + id
			}
			out = append(out, backend.Model{ID: id, Name: mo.Name})
			seen[mo.ID] = true
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

// providerLists returns the configured providers in the order their models are
// concatenated. Providers that are not configured are omitted.
func (m *MultiBackend) providerLists() []providerList {
	lists := make([]providerList, 0, 16)
	add := func(name string, b backend.BackendAPI) {
		if b != nil {
			lists = append(lists, providerList{name: name, backend: b})
		}
	}
	add("antigravity", m.Antigravity)
	add("codex", m.Codex)
	add("codebuddy", m.CodeBuddy)
	add("qoder", m.Qoder)
	add("kiro", m.Kiro)
	add("verboo", m.Verboo)
	add("nvidia", m.NVIDIA)
	add("groq", m.Groq)
	add("cloudflare", m.Cloudflare)
	add("opencode", m.OpenCode)
	for _, name := range m.customNames() {
		add(name, m.Custom[name])
	}
	return lists
}

func (m *MultiBackend) HasModel(ctx context.Context, id string) bool {
	if _, ok := m.comboMembers(id); ok {
		return true
	}
	if strings.HasPrefix(id, "+") || strings.HasPrefix(id, "-") {
		return false
	}
	if name, raw, ok := strings.Cut(id, "/"); ok {
		if _, found := m.providerBackend(name); found && m.modelStates != nil && m.modelStates.Disabled(name, raw) {
			return false
		}
		if _, found := m.providerBackend(name); found && m.modelStates != nil && m.modelStates.Enabled(name, raw) {
			return true
		}
	}
	if m.modelStates != nil && m.modelStates.Disabled(m.Provider(ctx, id), id) {
		return false
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
		b, ok := m.custom(name)
		if !ok {
			continue
		}
		if q, ok := b.(quotaReporter); ok {
			quota := q.Quota()
			out = append(out, quota)
		}
	}
	return out
}
