package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"seavxrouter/internal/backend"
)

func newTestUsageStore(t *testing.T) *UsageStore {
	t.Helper()
	store, err := OpenUsageStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("OpenUsageStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUsageStoreRecordAndQuery(t *testing.T) {
	store := newTestUsageStore(t)
	entries := []UsageEntry{
		{TS: "2026-11-11T10:00:00Z", Provider: "codex", Model: "gpt-x", Endpoint: "chat", User: "alice", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		{TS: "2026-11-12T09:00:00Z", Provider: "codebuddy", Model: "glm-5.3", Endpoint: "messages", User: "bob", PromptTokens: 20, CompletionTokens: 8, ThinkingTokens: 2, CachedTokens: 4, TotalTokens: 28},
		{TS: "2026-11-12T10:00:00Z", Provider: "codex", Model: "gpt-x", Endpoint: "chat", User: "alice", PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}
	for _, e := range entries {
		if err := store.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	totals, groups, err := store.Query(UsageFilter{GroupBy: "day"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	wantTotals := UsageTotals{Requests: 3, PromptTokens: 31, CompletionTokens: 15, ThinkingTokens: 2, CachedTokens: 4, TotalTokens: 46}
	if totals != wantTotals {
		t.Fatalf("totals = %+v, want %+v", totals, wantTotals)
	}
	if len(groups) != 2 || groups[0].Key != "2026-11-11" || groups[1].Key != "2026-11-12" {
		t.Fatalf("day groups = %+v", groups)
	}
	if groups[1].Requests != 2 || groups[1].TotalTokens != 31 {
		t.Fatalf("day group[1] = %+v", groups[1])
	}

	_, groups, err = store.Query(UsageFilter{GroupBy: "provider"})
	if err != nil {
		t.Fatalf("Query provider: %v", err)
	}
	if len(groups) != 2 || groups[0].Key != "codebuddy" || groups[1].Key != "codex" {
		t.Fatalf("provider groups = %+v", groups)
	}

	totals, _, err = store.Query(UsageFilter{Provider: "codex", User: "alice", GroupBy: "model"})
	if err != nil {
		t.Fatalf("Query filtered: %v", err)
	}
	if totals.Requests != 2 || totals.TotalTokens != 18 {
		t.Fatalf("filtered totals = %+v", totals)
	}
}

func TestUsageHandler(t *testing.T) {
	store := newTestUsageStore(t)
	if err := store.Record(UsageEntry{TS: "2026-11-12T10:00:00Z", Provider: "codex", Model: "m", Endpoint: "chat", User: "alice", PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	s := &Server{usage: store}

	w := httptest.NewRecorder()
	s.handleUsage(w, httptest.NewRequest("GET", "/v1/usage?group_by=user", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Object string       `json:"object"`
		Totals UsageTotals  `json:"totals"`
		Data   []UsageGroup `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Object != "usage" || resp.Totals.Requests != 1 || resp.Totals.TotalTokens != 10 {
		t.Fatalf("resp = %+v", resp)
	}
	if len(resp.Data) != 1 || resp.Data[0].Key != "alice" {
		t.Fatalf("data = %+v", resp.Data)
	}

	w = httptest.NewRecorder()
	s.handleUsage(w, httptest.NewRequest("GET", "/v1/usage?group_by=bogus", nil))
	if w.Code != 400 {
		t.Fatalf("invalid group_by status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	s.handleUsage(w, httptest.NewRequest("GET", "/v1/usage?start=not-a-date", nil))
	if w.Code != 400 {
		t.Fatalf("invalid start status = %d", w.Code)
	}

	s.usage = nil
	w = httptest.NewRecorder()
	s.handleUsage(w, httptest.NewRequest("GET", "/v1/usage", nil))
	if w.Code != 404 {
		t.Fatalf("disabled status = %d", w.Code)
	}
}

func TestUsageRecordedViaHandleChat(t *testing.T) {
	store := newTestUsageStore(t)
	stub := &chatStub{result: &backend.ChatResult{
		Content:      "hi",
		FinishReason: "stop",
		Usage:        backend.Usage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15},
	}}
	s := &Server{backend: stub, usage: store}

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Seavx-User", "alice")
	w := httptest.NewRecorder()
	s.handleChat(w, req)
	if w.Code != 200 {
		t.Fatalf("handleChat status = %d body=%s", w.Code, w.Body.String())
	}

	totals, groups, err := store.Query(UsageFilter{GroupBy: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if totals.Requests != 1 || totals.PromptTokens != 12 || totals.CompletionTokens != 3 || totals.TotalTokens != 15 {
		t.Fatalf("totals = %+v", totals)
	}
	if len(groups) != 1 || groups[0].Key != "alice" {
		t.Fatalf("groups = %+v", groups)
	}
	// chatStub is not a MultiBackend: provider defaults to antigravity.
	_, groups, _ = store.Query(UsageFilter{Provider: "antigravity", Model: "m", GroupBy: "endpoint"})
	if len(groups) != 1 || groups[0].Key != "chat" {
		t.Fatalf("endpoint groups = %+v", groups)
	}
}

func TestUsageUserAttribution(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Seavx-User", "  alice  ")
	if got := usageUser(r); got != "alice" {
		t.Fatalf("header user = %q", got)
	}

	r = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	got := usageUser(r)
	if len(got) != 12 || strings.Contains(got, "secret") {
		t.Fatalf("hashed user = %q", got)
	}

	r = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if got := usageUser(r); got != "anonymous" {
		t.Fatalf("anonymous = %q", got)
	}
}

func TestUsageRecordedViaStreamingChat(t *testing.T) {
	store := newTestUsageStore(t)
	stub := &chatStub{
		result: &backend.ChatResult{
			Content:      "hello",
			FinishReason: "stop",
			Usage:        backend.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		},
		deltas: []backend.ChatDelta{{Content: "hel"}, {Content: "lo"}},
	}
	s := &Server{backend: stub, usage: store}
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer key-abc")
	w := httptest.NewRecorder()
	s.handleChat(w, req)
	if w.Code != 200 {
		t.Fatalf("handleChat status = %d", w.Code)
	}
	totals, groups, err := store.Query(UsageFilter{GroupBy: "endpoint"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if totals.Requests != 1 || totals.TotalTokens != 7 {
		t.Fatalf("totals = %+v", totals)
	}
	if len(groups) != 1 || groups[0].Key != "chat" {
		t.Fatalf("groups = %+v", groups)
	}
}
