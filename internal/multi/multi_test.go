package multi

import (
	"context"
	"errors"
	"testing"

	"seavxrouter/internal/backend"
)

func TestNormalizeModel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gemini.3.8-flash", "gemini-3.8-flash-high"},
		{"gemini-3.8-flash", "gemini-3.8-flash-high"},
		{"gemini-3.8-flash-preview", "gemini-3.8-flash-high"},
		{"gemini.3.8-flash-preview", "gemini-3.8-flash-high"},
		{"gemini-flash", "gemini-3.8-flash-high"},
		{"gemini.2.5-flash", "gemini-2.5-flash"},
		{"gemini-2.5-flash", "gemini-2.5-flash"},
		{"gemini.2.5-pro", "gemini-2.5-pro"},
		{"gemini-2.5-pro", "gemini-2.5-pro"},
		{"claude-3-7-sonnet", "claude-sonnet-4-6"},
		{"claude-3-7-sonnet-20250219", "claude-sonnet-4-6"},
		{"claude-3-5-sonnet", "claude-sonnet-4-6"},
		{"claude-3-5-sonnet-20241022", "claude-sonnet-4-6"},
		{"claude-3-opus", "claude-opus-4-6-thinking"},
		{"claude-3-opus-20240229", "claude-opus-4-6-thinking"},
		// Unrecognized models fall back cleanly.
		{"gpt-5.1-codex", "gpt-5.1-codex"},
		{"gemini-3.8-flash-high", "gemini-3.8-flash-high"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeModel(tc.in); got != tc.want {
				t.Fatalf("normalizeModel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOpenCodeRouting(t *testing.T) {
	oc := &fakeBackend{models: map[string]bool{"big-pickle": true}, def: "big-pickle"}
	m := &MultiBackend{OpenCode: oc}
	ctx := context.Background()

	models, err := m.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "opencode/big-pickle" {
		t.Fatalf("models = %+v", models)
	}
	res, err := m.Chat(ctx, &backend.ChatRequest{Model: "opencode/big-pickle"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" || oc.calls != 1 || oc.lastModel != "big-pickle" {
		t.Fatalf("result = %+v, calls = %d, model = %q", res, oc.calls, oc.lastModel)
	}
	if got := m.Provider(ctx, "opencode/big-pickle"); got != "opencode" {
		t.Fatalf("provider = %q", got)
	}
}

type fakeBackend struct {
	models    map[string]bool
	lastModel string
	def       string
	calls     int
	errs      []error
	deltas    []backend.ChatDelta
}

func (f *fakeBackend) Models(context.Context) ([]backend.Model, error) {
	out := make([]backend.Model, 0, len(f.models))
	for id := range f.models {
		out = append(out, backend.Model{ID: id, Name: id})
	}
	return out, nil
}
func (f *fakeBackend) HasModel(_ context.Context, id string) bool {
	f.lastModel = id
	return f.models[id]
}
func (f *fakeBackend) Complete(_ context.Context, model, _ string) (*backend.Result, error) {
	f.lastModel = model
	return &backend.Result{Status: "SUCCESS", Response: "hello"}, nil
}
func (f *fakeBackend) Stream(_ context.Context, model, _ string, onDelta func(string)) (*backend.Result, error) {
	f.lastModel = model
	if onDelta != nil {
		onDelta("hello")
	}
	return &backend.Result{Status: "SUCCESS", Response: "hello"}, nil
}
func (f *fakeBackend) Chat(_ context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	f.calls++
	f.lastModel = req.Model
	for _, d := range f.deltas {
		if onDelta != nil {
			onDelta(d)
		}
	}
	if i := f.calls - 1; i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	return &backend.ChatResult{Content: "ok", FinishReason: "stop"}, nil
}
func (f *fakeBackend) Default() string { return f.def }

func TestMultiBackendResolveNormalizes(t *testing.T) {
	ctx := context.Background()
	agy := &fakeBackend{models: map[string]bool{"gemini-3.8-flash-high": true, "claude-sonnet-4-6": true}}
	m := &MultiBackend{Antigravity: agy}

	if _, id, err := m.resolve(ctx, "gemini.3.8-flash"); err != nil || id != "gemini-3.8-flash-high" {
		t.Fatalf("bare alias: id=%q err=%v, want gemini-3.8-flash-high", id, err)
	}
	if _, id, err := m.resolve(ctx, "antigravity/gemini-3.8-flash"); err != nil || id != "gemini-3.8-flash-high" {
		t.Fatalf("prefixed alias: id=%q err=%v, want gemini-3.8-flash-high", id, err)
	}
	if _, id, err := m.resolve(ctx, "claude-3-5-sonnet-20241022"); err != nil || id != "claude-sonnet-4-6" {
		t.Fatalf("claude alias: id=%q err=%v, want claude-sonnet-4-6", id, err)
	}
	if !m.HasModel(ctx, "gemini.3.8-flash") {
		t.Fatalf("HasModel should resolve alias gemini.3.8-flash")
	}
	if agy.lastModel != "gemini-3.8-flash-high" {
		t.Fatalf("HasModel passed %q to backend, want normalized id", agy.lastModel)
	}
}

func TestChatRetriesTransientFailure(t *testing.T) {
	old := retryBackoff
	retryBackoff = 0
	defer func() { retryBackoff = old }()

	agy := &fakeBackend{models: map[string]bool{"gemini-3.8-flash-high": true}, errs: []error{errors.New("503 Service Unavailable")}}
	m := &MultiBackend{Antigravity: agy}
	res, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "gemini-3.8-flash"}, nil)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if res.Content != "ok" || agy.calls != 2 {
		t.Fatalf("calls = %d, result = %+v", agy.calls, res)
	}
	if agy.lastModel != "gemini-3.8-flash-high" {
		t.Fatalf("model = %q", agy.lastModel)
	}
}

func TestChatFailsOverOnQuotaError(t *testing.T) {
	agy := &fakeBackend{models: map[string]bool{"claude-sonnet-4-6": true}, errs: []error{errors.New("429 rate limit exceeded")}}
	codex := &fakeBackend{models: map[string]bool{"claude-sonnet-4-6": true}, deltas: []backend.ChatDelta{{Content: "hi"}}}
	m := &MultiBackend{Antigravity: agy, Codex: codex}
	var got []string
	res, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "antigravity/claude-sonnet-4-6"}, func(d backend.ChatDelta) {
		got = append(got, d.Content)
	})
	if err != nil {
		t.Fatalf("failover failed: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("result = %+v", res)
	}
	if agy.calls != 1 || codex.calls != 1 {
		t.Fatalf("calls: antigravity=%d codex=%d", agy.calls, codex.calls)
	}
	if codex.lastModel != "claude-sonnet-4-6" {
		t.Fatalf("fallback model = %q", codex.lastModel)
	}
	if len(got) != 1 || got[0] != "hi" {
		t.Fatalf("deltas = %v", got)
	}
}

func TestChatFailsOverOnBillingCap(t *testing.T) {
	qoder := &fakeBackend{
		models: map[string]bool{"DeepSeek-Flash": true},
		errs:   []error{errors.New("qoder HTTP 403: Billing daily count exceeded")},
	}
	verboo := &fakeBackend{models: map[string]bool{"DeepSeek-Flash": true}}
	m := &MultiBackend{Qoder: qoder, Verboo: verboo}
	res, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "qoder/DeepSeek-Flash"}, nil)
	if err != nil {
		t.Fatalf("failover failed: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("result = %+v", res)
	}
	if qoder.calls != 1 || verboo.calls != 1 {
		t.Fatalf("calls: qoder=%d verboo=%d", qoder.calls, verboo.calls)
	}
	if verboo.lastModel != "DeepSeek-Flash" {
		t.Fatalf("fallback model = %q", verboo.lastModel)
	}
}

func TestChatFallsBackToProviderDefaultOnQuota(t *testing.T) {
	qoder := &fakeBackend{
		models: map[string]bool{"DeepSeek-Flash": true},
		errs:   []error{errors.New("qoder HTTP 403: Billing daily count exceeded")},
	}
	// verboo names its DeepSeek model differently, so the same-ID pass finds
	// nothing; the last-resort pass must use verboo's default model instead.
	verboo := &fakeBackend{models: map[string]bool{"deepseek-v4.1-flash": true}, def: "deepseek-v4.1-flash"}
	m := &MultiBackend{Qoder: qoder, Verboo: verboo}
	res, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "qoder/DeepSeek-Flash"}, nil)
	if err != nil {
		t.Fatalf("last-resort fallback failed: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("result = %+v", res)
	}
	if qoder.calls != 1 || verboo.calls != 1 {
		t.Fatalf("calls: qoder=%d verboo=%d", qoder.calls, verboo.calls)
	}
	if verboo.lastModel != "deepseek-v4.1-flash" {
		t.Fatalf("fallback model = %q", verboo.lastModel)
	}
}

func TestChatLastResortRetriesProviderThatRejectedModel(t *testing.T) {
	// antigravity's HasModel is fail-open, so it claims DeepSeek-Flash and then
	// rejects it with a non-retryable 400; the last-resort pass must still try
	// its own default model.
	agy := &fakeBackend{
		models: map[string]bool{"DeepSeek-Flash": true},
		def:    "gemini-3.8-flash-high",
		errs:   []error{errors.New("400 Bad Request: Request contains an invalid argument.")},
	}
	qoder := &fakeBackend{
		models: map[string]bool{"DeepSeek-Flash": true},
		errs:   []error{errors.New("qoder HTTP 403: Billing daily count exceeded")},
	}
	m := &MultiBackend{Antigravity: agy, Qoder: qoder}
	res, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "qoder/DeepSeek-Flash"}, nil)
	if err != nil {
		t.Fatalf("last-resort fallback failed: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("result = %+v", res)
	}
	if agy.calls != 2 || qoder.calls != 1 {
		t.Fatalf("calls: antigravity=%d qoder=%d", agy.calls, qoder.calls)
	}
	if agy.lastModel != "gemini-3.8-flash-high" {
		t.Fatalf("default model = %q", agy.lastModel)
	}
}

func TestChatNoRetryAfterOutput(t *testing.T) {
	old := retryBackoff
	retryBackoff = 0
	defer func() { retryBackoff = old }()

	agy := &fakeBackend{
		models: map[string]bool{"gemini-3.8-flash-high": true},
		errs:   []error{errors.New("503 Service Unavailable")},
		deltas: []backend.ChatDelta{{Content: "partial"}},
	}
	codex := &fakeBackend{models: map[string]bool{"gemini-3.8-flash-high": true}}
	m := &MultiBackend{Antigravity: agy, Codex: codex}
	if _, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "antigravity/gemini-3.8-flash"}, nil); err == nil {
		t.Fatal("expected error")
	}
	if agy.calls != 1 || codex.calls != 0 {
		t.Fatalf("retried after output: antigravity=%d codex=%d", agy.calls, codex.calls)
	}
}

func TestChatNoFallbackOnNonRetryableError(t *testing.T) {
	agy := &fakeBackend{models: map[string]bool{"claude-sonnet-4-6": true}, errs: []error{errors.New("400 Bad Request: invalid model")}}
	codex := &fakeBackend{models: map[string]bool{"claude-sonnet-4-6": true}}
	m := &MultiBackend{Antigravity: agy, Codex: codex}
	if _, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "antigravity/claude-sonnet-4-6"}, nil); err == nil {
		t.Fatal("expected error")
	}
	if agy.calls != 1 || codex.calls != 0 {
		t.Fatalf("fallback on non-retryable: antigravity=%d codex=%d", agy.calls, codex.calls)
	}
}

func TestChatFallbackRequiresMatchingModel(t *testing.T) {
	old := retryBackoff
	retryBackoff = 0
	defer func() { retryBackoff = old }()

	// Transient failures only fail over to a provider serving the same model;
	// the last-resort default model is reserved for quota/auth-dead providers.
	agy := &fakeBackend{
		models: map[string]bool{"claude-sonnet-4-6": true},
		errs:   []error{errors.New("503 Service Unavailable"), errors.New("503 Service Unavailable")},
	}
	codex := &fakeBackend{models: map[string]bool{}, def: "gpt-5.1-codex"}
	m := &MultiBackend{Antigravity: agy, Codex: codex}
	if _, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "antigravity/claude-sonnet-4-6"}, nil); err == nil {
		t.Fatal("expected error")
	}
	if codex.calls != 0 || codex.lastModel != "claude-sonnet-4-6" {
		t.Fatalf("fallback called without model: calls=%d model=%q", codex.calls, codex.lastModel)
	}
}
