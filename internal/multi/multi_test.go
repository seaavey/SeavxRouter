package multi

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/httpapi"
)

func TestManagedModelsAndManualEntries(t *testing.T) {
	ctx := context.Background()
	old := &fakeBackend{models: map[string]bool{"old": true, "keep": true}}
	other := &fakeBackend{models: map[string]bool{"old": true}}
	m := &MultiBackend{Custom: map[string]backend.BackendAPI{"one": old, "two": other}}
	path := filepath.Join(t.TempDir(), "models.json")
	manager, err := httpapi.NewProviderModelManager(m, path, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.SetModelManager(manager)
	if err := manager.AddModel(ctx, "one", "new"); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteModel(ctx, "one", "old"); err != nil {
		t.Fatal(err)
	}
	assertHas := func(id string, want bool) {
		t.Helper()
		if got := m.HasModel(ctx, id); got != want {
			t.Errorf("HasModel(%q) = %v, want %v", id, got, want)
		}
	}
	assertListed := func(want ...string) {
		t.Helper()
		models, err := m.Models(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, model := range models {
			got[model.ID] = true
		}
		for _, id := range want {
			if !got[id] {
				t.Errorf("Models() missing %q: %v", id, got)
			}
		}
		for _, id := range []string{"one/old"} {
			if got[id] {
				t.Errorf("Models() unexpectedly includes %q", id)
			}
		}
	}
	assertListed("one/new", "one/keep", "two/old")
	assertHas("one/new", true)
	assertHas("one/old", false)
	assertHas("two/old", true)
	m.Combos = &Combos{entries: map[string][]string{"disabled": {"one/old"}}}
	if _, err := m.Chat(ctx, &backend.ChatRequest{Model: "combo/disabled"}, nil); err == nil {
		t.Fatal("combo must not route to deleted model")
	}
	manager, err = httpapi.NewProviderModelManager(m, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetModelManager(manager)
	assertListed("one/new", "one/keep", "two/old")
	assertHas("one/new", true)
	assertHas("one/old", false)
	assertHas("two/old", true)
	m.Combos = &Combos{entries: map[string][]string{"disabled": {"one/old"}}}
	if _, err := m.Chat(ctx, &backend.ChatRequest{Model: "combo/disabled"}, nil); err == nil {
		t.Fatal("combo must not route to deleted model")
	}
}

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
	if res.Model != "big-pickle" || res.Provider != "opencode" {
		t.Fatalf("served attribution = %q/%q", res.Provider, res.Model)
	}
	if got := m.Provider(ctx, "opencode/big-pickle"); got != "opencode" {
		t.Fatalf("provider = %q", got)
	}
}

func TestCustomProviderRouting(t *testing.T) {
	custom := &fakeBackend{models: map[string]bool{"deepseek-ai/deepseek-v3": true}, def: "deepseek-ai/deepseek-v3"}
	m := &MultiBackend{Custom: map[string]backend.BackendAPI{"runcrate": custom}}
	ctx := context.Background()

	models, err := m.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "runcrate/deepseek-ai/deepseek-v3" {
		t.Fatalf("models = %+v", models)
	}
	res, err := m.Chat(ctx, &backend.ChatRequest{Model: "runcrate/deepseek-ai/deepseek-v3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" || custom.calls != 1 || custom.lastModel != "deepseek-ai/deepseek-v3" {
		t.Fatalf("result = %+v, calls = %d, model = %q", res, custom.calls, custom.lastModel)
	}
	if got := m.Provider(ctx, "runcrate/deepseek-ai/deepseek-v3"); got != "runcrate" {
		t.Fatalf("provider = %q", got)
	}
	if got := m.Default(); got != "deepseek-ai/deepseek-v3" {
		t.Fatalf("default = %q", got)
	}
	// Bare IDs fall through to a custom provider when no built-in is set.
	if _, id, err := m.resolve(ctx, "deepseek-ai/deepseek-v3"); err != nil || id != "deepseek-ai/deepseek-v3" {
		t.Fatalf("bare resolve = %q, %v", id, err)
	}
}

func TestCustomProviderKeepsModelIDsUnchanged(t *testing.T) {
	custom := &fakeBackend{models: map[string]bool{"gemini-flash": true}}
	m := &MultiBackend{Custom: map[string]backend.BackendAPI{"runcrate": custom}}
	ctx := context.Background()
	if _, id, err := m.resolve(ctx, "runcrate/gemini-flash"); err != nil || id != "gemini-flash" {
		t.Fatalf("custom model alias was rewritten: id=%q err=%v", id, err)
	}
}

type fakeBackend struct {
	models    map[string]bool
	lastModel string
	def       string
	calls     int
	errs      []error
	deltas    []backend.ChatDelta

	completeErr   error
	streamErr     error
	completeCalls int
	streamCalls   int
}

func (f *fakeBackend) Models(context.Context) ([]backend.Model, error) {
	out := make([]backend.Model, 0, len(f.models))
	for id := range f.models {
		out = append(out, backend.Model{ID: id, Name: id})
	}
	// Real engines return a sorted catalog; match that so tests can assert order.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (f *fakeBackend) HasModel(_ context.Context, id string) bool {
	f.lastModel = id
	return f.models[id]
}
func (f *fakeBackend) Complete(_ context.Context, model, _ string) (*backend.Result, error) {
	f.completeCalls++
	f.lastModel = model
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	return &backend.Result{Status: "SUCCESS", Response: "hello"}, nil
}
func (f *fakeBackend) Stream(_ context.Context, model, _ string, onDelta func(string)) (*backend.Result, error) {
	f.streamCalls++
	f.lastModel = model
	if f.streamErr != nil {
		return nil, f.streamErr
	}
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

func TestChatStaysOnProviderOnQuotaError(t *testing.T) {
	qoder := &fakeBackend{
		models: map[string]bool{"DeepSeek-Flash": true},
		errs:   []error{errors.New("qoder HTTP 403: Billing daily count exceeded")},
	}
	codex := &fakeBackend{models: map[string]bool{"DeepSeek-Flash": true}}
	m := &MultiBackend{Qoder: qoder, Codex: codex}
	if _, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "qoder/DeepSeek-Flash"}, nil); err == nil {
		t.Fatal("expected error")
	}
	if qoder.calls != 1 || codex.calls != 0 {
		t.Fatalf("switched providers: qoder=%d codex=%d", qoder.calls, codex.calls)
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

func TestChatRetriesSameProviderOnTransientFailure(t *testing.T) {
	old := retryBackoff
	retryBackoff = 0
	defer func() { retryBackoff = old }()

	agy := &fakeBackend{
		models: map[string]bool{"claude-sonnet-4-6": true},
		errs:   []error{errors.New("503 Service Unavailable"), errors.New("503 Service Unavailable")},
	}
	codex := &fakeBackend{models: map[string]bool{"claude-sonnet-4-6": true}}
	m := &MultiBackend{Antigravity: agy, Codex: codex}
	if _, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "antigravity/claude-sonnet-4-6"}, nil); err == nil {
		t.Fatal("expected error")
	}
	if agy.calls != 2 || codex.calls != 0 {
		t.Fatalf("calls: antigravity=%d codex=%d", agy.calls, codex.calls)
	}
}

// slowBackend blocks until released, so a test can prove the aggregator queries
// providers concurrently instead of one after another.
type slowBackend struct {
	id      string
	started chan struct{}
	release chan struct{}
}

func (s *slowBackend) Models(context.Context) ([]backend.Model, error) {
	s.started <- struct{}{}
	<-s.release
	return []backend.Model{{ID: s.id, Name: s.id}}, nil
}
func (s *slowBackend) HasModel(context.Context, string) bool { return false }
func (s *slowBackend) Complete(context.Context, string, string) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (s *slowBackend) Stream(context.Context, string, string, func(string)) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (s *slowBackend) Chat(context.Context, *backend.ChatRequest, func(backend.ChatDelta)) (*backend.ChatResult, error) {
	return &backend.ChatResult{}, nil
}
func (s *slowBackend) Default() string { return "" }

// TestModelsFetchesProvidersConcurrently guards the latency fix: /v1/models used
// to await every provider in sequence, so the response cost the sum of every
// upstream round trip. Both fetches must be in flight at the same time.
func TestModelsFetchesProvidersConcurrently(t *testing.T) {
	one := &slowBackend{id: "a", started: make(chan struct{}, 1), release: make(chan struct{})}
	two := &slowBackend{id: "b", started: make(chan struct{}, 1), release: make(chan struct{})}
	m := &MultiBackend{Custom: map[string]backend.BackendAPI{"one": one, "two": two}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Models(context.Background())
	}()

	// Both providers must report a started fetch before either is released; a
	// sequential implementation would leave the second one still waiting.
	for _, b := range []*slowBackend{one, two} {
		select {
		case <-b.started:
		case <-time.After(5 * time.Second):
			t.Fatal("provider fetch did not start: aggregation is not concurrent")
		}
	}
	close(one.release)
	close(two.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Models did not return")
	}
}

// TestModelsKeepsProviderOrder pins the response ordering that the concurrent
// rewrite must preserve: antigravity IDs stay bare, codex is prefixed only on a
// collision, and every other provider is namespaced.
func TestModelsKeepsProviderOrder(t *testing.T) {
	ctx := context.Background()
	m := &MultiBackend{
		Antigravity: &fakeBackend{models: map[string]bool{"shared": true, "gem": true}},
		Codex:       &fakeBackend{models: map[string]bool{"shared": true, "codexonly": true}},
		Verboo:      &fakeBackend{models: map[string]bool{"vb": true}},
	}
	models, err := m.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(models))
	for _, mo := range models {
		got = append(got, mo.ID)
	}
	// antigravity and codex both advertise sorted catalogs; "shared" is claimed by
	// antigravity first, so codex's copy is the one that gets namespaced.
	want := []string{"gem", "shared", "codexonly", "codex/shared", "verboo/vb"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}
