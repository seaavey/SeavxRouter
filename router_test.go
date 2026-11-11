package main

import (
	"context"
	"testing"
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

type fakeBackend struct {
	models    map[string]bool
	lastModel string
	def       string
}

func (f *fakeBackend) Models(context.Context) ([]Model, error) {
	out := make([]Model, 0, len(f.models))
	for id := range f.models {
		out = append(out, Model{ID: id, Name: id})
	}
	return out, nil
}
func (f *fakeBackend) HasModel(_ context.Context, id string) bool {
	f.lastModel = id
	return f.models[id]
}
func (f *fakeBackend) Complete(_ context.Context, model, _ string) (*Result, error) {
	f.lastModel = model
	return &Result{Status: "SUCCESS", Response: "hello"}, nil
}
func (f *fakeBackend) Stream(_ context.Context, model, _ string, onDelta func(string)) (*Result, error) {
	f.lastModel = model
	if onDelta != nil {
		onDelta("hello")
	}
	return &Result{Status: "SUCCESS", Response: "hello"}, nil
}
func (f *fakeBackend) Chat(_ context.Context, req *ChatRequest, onDelta func(ChatDelta)) (*ChatResult, error) {
	f.lastModel = req.Model
	if onDelta != nil {
		onDelta(ChatDelta{Content: "x"})
	}
	return &ChatResult{Content: "ok", FinishReason: "stop"}, nil
}
func (f *fakeBackend) Default() string { return f.def }

func TestMultiBackendResolveNormalizes(t *testing.T) {
	ctx := context.Background()
	agy := &fakeBackend{models: map[string]bool{"gemini-3.8-flash-high": true, "claude-sonnet-4-6": true}}
	m := &MultiBackend{antigravity: agy}

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