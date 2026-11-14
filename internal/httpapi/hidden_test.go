package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"seavxrouter/internal/backend"
)

func writeHidden(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "hidden-models.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHiddenModelsFiltersCatalog(t *testing.T) {
	dir := t.TempDir()
	path := writeHidden(t, dir, "# comment\nnvidia/broken-model\n\nnvidia/\n")
	h := NewHiddenModels(path)

	if !h.Hidden("nvidia/broken-model") {
		t.Fatal("literal ID not hidden")
	}
	if !h.Hidden("nvidia/anything-else") {
		t.Fatal("provider prefix not hidden")
	}
	if h.Hidden("groq/fake-1") {
		t.Fatal("unrelated model wrongly hidden")
	}

	wrapped := WithHiddenModels(&catalogStub{models: []backend.Model{
		{ID: "nvidia/broken-model"}, {ID: "nvidia/other"}, {ID: "groq/fake-1"},
	}}, h)
	models, err := wrapped.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "groq/fake-1" {
		t.Fatalf("models = %+v", models)
	}
	// A hidden model must not resolve, so /v1/models/{id} answers 404.
	if wrapped.HasModel(context.Background(), "nvidia/broken-model") {
		t.Fatal("hidden model still resolvable")
	}
}

func TestHiddenModelsReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := writeHidden(t, dir, "groq/one\n")
	h := NewHiddenModels(path)
	if !h.Hidden("groq/one") {
		t.Fatal("initial entry not loaded")
	}
	if h.Hidden("groq/two") {
		t.Fatal("unexpected hidden entry")
	}

	// Rewrite with a new mtime; the list must refresh without a restart.
	time.Sleep(10 * time.Millisecond)
	writeHidden(t, dir, "groq/two\n")
	if err := os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !h.Hidden("groq/two") {
		t.Fatal("file change not picked up")
	}
	if h.Hidden("groq/one") {
		t.Fatal("stale entry survived reload")
	}
}

func TestHiddenModelsMissingFileHidesNothing(t *testing.T) {
	h := NewHiddenModels(filepath.Join(t.TempDir(), "nope.txt"))
	if h.Hidden("anything") {
		t.Fatal("missing file should hide nothing")
	}
	if got := h.IDs(); len(got) != 0 {
		t.Fatalf("IDs = %v", got)
	}
}

// catalogStub is a minimal backend exposing a fixed model list.
type catalogStub struct {
	chatStub
	models []backend.Model
}

func (c *catalogStub) Models(context.Context) ([]backend.Model, error) { return c.models, nil }

// providerStub also names the provider serving each model.
type providerStub struct{ catalogStub }

func (p *providerStub) Provider(context.Context, string) string { return "codex" }

func TestHiddenWrapperPreservesProviderAttribution(t *testing.T) {
	h := NewHiddenModels(filepath.Join(t.TempDir(), "nope.txt"))
	wrapped := WithHiddenModels(&providerStub{}, h)
	pn, ok := wrapped.(interface {
		Provider(context.Context, string) string
	})
	if !ok {
		t.Fatal("wrapping dropped provider attribution")
	}
	if got := pn.Provider(context.Background(), "gpt-6-sol"); got != "codex" {
		t.Fatalf("provider = %q, want codex", got)
	}
}
