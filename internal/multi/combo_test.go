package multi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"seavxrouter/internal/backend"
)

func TestLoadCombosFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "combos.jsonc")
	content := `// ordered fallback lists
{
  "fast": ["groq/llama-3.3-70b", " nvidia/deepseek-v3 ", ""],
  "smart": ["codex/gpt-5"]
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadCombosFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries["fast"]; len(got) != 2 || got[0] != "groq/llama-3.3-70b" || got[1] != "nvidia/deepseek-v3" {
		t.Fatalf("fast = %#v", got)
	}
	if got := entries["smart"]; len(got) != 1 || got[0] != "codex/gpt-5" {
		t.Fatalf("smart = %#v", got)
	}
}

func TestLoadCombosFileMissingIsEmpty(t *testing.T) {
	entries, err := LoadCombosFile(filepath.Join(t.TempDir(), "combos.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestLoadCombosFileRejectsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "combos.jsonc")
	if err := os.WriteFile(path, []byte(`{"fast": "groq/llama"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCombosFile(path); err == nil {
		t.Fatal("expected an error for a non-array combo")
	}
}

func TestSaveCombosFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "combos.jsonc")
	want := map[string][]string{"fast": {"groq/llama", "codex/gpt-5"}}
	if err := SaveCombosFile(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCombosFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["fast"]) != 2 || got["fast"][1] != "codex/gpt-5" {
		t.Fatalf("round trip = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCombosReloadOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "combos.jsonc")
	if err := os.WriteFile(path, []byte(`{"fast": ["groq/old"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCombos(path)
	if got, ok := c.Get("fast"); !ok || got[0] != "groq/old" {
		t.Fatalf("initial = %#v, %v", got, ok)
	}
	if err := os.WriteFile(path, []byte(`{"fast": ["groq/new"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	touch := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, touch, touch); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Get("fast"); !ok || got[0] != "groq/new" {
		t.Fatalf("after edit = %#v, %v", got, ok)
	}
}

func TestComboFailsOverAcrossProviders(t *testing.T) {
	agy := &fakeBackend{
		models: map[string]bool{"gemini-3.8-flash-high": true},
		errs:   []error{errors.New("429 Too Many Requests: quota exhausted")},
	}
	codex := &fakeBackend{models: map[string]bool{"gpt-5": true}}
	m := &MultiBackend{
		Antigravity: agy,
		Codex:       codex,
		Combos:      &Combos{entries: map[string][]string{"fast": {"antigravity/gemini-3.8-flash", "codex/gpt-5"}}},
	}
	res, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "combo/fast"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" || agy.calls != 1 || codex.calls != 1 {
		t.Fatalf("result = %+v, antigravity=%d codex=%d", res, agy.calls, codex.calls)
	}
	if res.Model != "gpt-5" || res.Provider != "codex" {
		t.Fatalf("served model attribution = %q/%q", res.Provider, res.Model)
	}
}

func TestComboSkipsUnconfiguredMember(t *testing.T) {
	codex := &fakeBackend{models: map[string]bool{"gpt-5": true}}
	m := &MultiBackend{
		Codex:  codex,
		Combos: &Combos{entries: map[string][]string{"fast": {"nvidia/llama-3", "codex/gpt-5"}}},
	}
	if _, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "combo/fast"}, nil); err != nil {
		t.Fatal(err)
	}
	if codex.calls != 1 {
		t.Fatalf("codex calls = %d", codex.calls)
	}
}

func TestComboStopsAfterOutput(t *testing.T) {
	agy := &fakeBackend{
		models: map[string]bool{"gemini-3.8-flash-high": true},
		errs:   []error{errors.New("503 Service Unavailable")},
		deltas: []backend.ChatDelta{{Content: "partial"}},
	}
	codex := &fakeBackend{models: map[string]bool{"gpt-5": true}}
	m := &MultiBackend{
		Antigravity: agy,
		Codex:       codex,
		Combos:      &Combos{entries: map[string][]string{"fast": {"antigravity/gemini-3.8-flash", "codex/gpt-5"}}},
	}
	var got strings.Builder
	_, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "combo/fast"}, func(d backend.ChatDelta) {
		got.WriteString(d.Content)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got.String() != "partial" {
		t.Fatalf("deltas = %q", got.String())
	}
	if codex.calls != 0 {
		t.Fatalf("switched providers after output: codex calls = %d", codex.calls)
	}
}

func TestComboStopsOnNonRetryableError(t *testing.T) {
	agy := &fakeBackend{
		models: map[string]bool{"gemini-3.8-flash-high": true},
		errs:   []error{errors.New("400 Bad Request: invalid request")},
	}
	codex := &fakeBackend{models: map[string]bool{"gpt-5": true}}
	m := &MultiBackend{
		Antigravity: agy,
		Codex:       codex,
		Combos:      &Combos{entries: map[string][]string{"fast": {"antigravity/gemini-3.8-flash", "codex/gpt-5"}}},
	}
	if _, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "combo/fast"}, nil); err == nil {
		t.Fatal("expected error")
	}
	if codex.calls != 0 {
		t.Fatalf("fell over on a non-retryable error: codex calls = %d", codex.calls)
	}
}

func TestComboExhaustedKeepsQuotaClassification(t *testing.T) {
	agy := &fakeBackend{
		models: map[string]bool{"gemini-3.8-flash-high": true},
		errs:   []error{errors.New("429 Too Many Requests: quota exhausted")},
	}
	codex := &fakeBackend{
		models: map[string]bool{"gpt-5": true},
		errs:   []error{errors.New("429 Too Many Requests: quota exhausted")},
	}
	m := &MultiBackend{
		Antigravity: agy,
		Codex:       codex,
		Combos:      &Combos{entries: map[string][]string{"fast": {"antigravity/gemini-3.8-flash", "codex/gpt-5"}}},
	}
	_, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "combo/fast"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !backend.IsQuotaError(err) {
		t.Fatalf("quota classification lost: %v", err)
	}
	if !strings.Contains(err.Error(), `combo "fast" exhausted`) {
		t.Fatalf("error = %v", err)
	}
}

func TestComboRejectsNestedCombo(t *testing.T) {
	codex := &fakeBackend{models: map[string]bool{"gpt-5": true}}
	m := &MultiBackend{
		Codex:  codex,
		Combos: &Combos{entries: map[string][]string{"loop": {"combo/loop"}}},
	}
	_, err := m.Chat(context.Background(), &backend.ChatRequest{Model: "combo/loop"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if codex.calls != 0 {
		t.Fatalf("nested combo reached a provider: %d", codex.calls)
	}
}

func TestComboModelDiscovery(t *testing.T) {
	codex := &fakeBackend{models: map[string]bool{"gpt-5": true}}
	m := &MultiBackend{
		Codex:  codex,
		Combos: &Combos{entries: map[string][]string{"fast": {"codex/gpt-5"}}},
	}
	ctx := context.Background()
	if !m.HasModel(ctx, "combo/fast") {
		t.Fatal("HasModel(combo/fast) = false")
	}
	if m.HasModel(ctx, "combo/missing") {
		t.Fatal("HasModel(combo/missing) = true")
	}
	models, err := m.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mo := range models {
		if mo.ID == "combo/fast" {
			found = true
		}
	}
	if !found {
		t.Fatalf("combo missing from Models: %+v", models)
	}
	if got := m.Provider(ctx, "combo/fast"); got != ComboOwner {
		t.Fatalf("provider = %q", got)
	}
}

func TestComboCompleteAndStreamFallback(t *testing.T) {
	agy := &fakeBackend{
		models:      map[string]bool{"gemini-3.8-flash-high": true},
		completeErr: errors.New("503 Service Unavailable"),
		streamErr:   errors.New("503 Service Unavailable"),
	}
	codex := &fakeBackend{models: map[string]bool{"gpt-5": true}}
	m := &MultiBackend{
		Antigravity: agy,
		Codex:       codex,
		Combos:      &Combos{entries: map[string][]string{"fast": {"antigravity/gemini-3.8-flash", "codex/gpt-5"}}},
	}
	if res, err := m.Complete(context.Background(), "combo/fast", "hi"); err != nil || res.Response != "hello" {
		t.Fatalf("Complete = %+v, %v", res, err)
	}
	if res, err := m.Stream(context.Background(), "combo/fast", "hi", nil); err != nil || res.Response != "hello" {
		t.Fatalf("Stream = %+v, %v", res, err)
	}
	if agy.completeCalls != 1 || codex.completeCalls != 1 || agy.streamCalls != 1 || codex.streamCalls != 1 {
		t.Fatalf("calls: agy complete=%d stream=%d, codex complete=%d stream=%d", agy.completeCalls, agy.streamCalls, codex.completeCalls, codex.streamCalls)
	}
}
