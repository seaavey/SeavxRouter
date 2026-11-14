package httpapi

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/compress"
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

	_, groups, err = store.Query(UsageFilter{GroupBy: "hour"})
	if err != nil {
		t.Fatalf("Query hour: %v", err)
	}
	if len(groups) != 3 || groups[0].Key != "2026-11-11T10" || groups[2].Key != "2026-11-12T10" {
		t.Fatalf("hour groups = %+v", groups)
	}
	if groups[2].Requests != 1 || groups[2].TotalTokens != 3 {
		t.Fatalf("hour group[2] = %+v", groups[2])
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
	s := NewServer(nil, "", store)

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
	s := NewServer(stub, "", store)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Seavx-User", "alice")
	w := httptest.NewRecorder()
	// Route through Handler so the logging middleware attributes the user;
	// calling the wire handler directly would skip middleware.
	s.Handler().ServeHTTP(w, req)
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

func TestUsageStoreCostTotals(t *testing.T) {
	store := newTestUsageStore(t)
	entries := []UsageEntry{
		{TS: "2026-11-12T09:00:00Z", Provider: "codex", Model: "gpt-x", Endpoint: "chat", User: "alice", PromptTokens: 1, TotalTokens: 1, CostUSD: 0.5},
		{TS: "2026-11-12T10:00:00Z", Provider: "codex", Model: "gpt-x", Endpoint: "chat", User: "alice", PromptTokens: 1, TotalTokens: 1, CostUSD: 0.25},
	}
	for _, e := range entries {
		if err := store.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	totals, groups, err := store.Query(UsageFilter{GroupBy: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !approx(totals.CostUSD, 0.75) {
		t.Fatalf("totals cost = %v", totals.CostUSD)
	}
	if len(groups) != 1 || !approx(groups[0].CostUSD, 0.75) {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestUsageStoreMigratesCostColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE usage (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL,
		provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		endpoint TEXT NOT NULL DEFAULT '',
		user TEXT NOT NULL DEFAULT '',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		thinking_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenUsageStore(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Record(UsageEntry{TS: "2026-11-12T10:00:00Z", CostUSD: 0.42}); err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	totals, _, err := store.Query(UsageFilter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !approx(totals.CostUSD, 0.42) {
		t.Fatalf("cost = %v", totals.CostUSD)
	}
}

func TestUsageCostRecordedViaHandleChat(t *testing.T) {
	store := newTestUsageStore(t)
	stub := &chatStub{result: &backend.ChatResult{
		Content:      "hi",
		FinishReason: "stop",
		Usage:        backend.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000},
	}}
	s := NewServer(stub, "", store,
		WithPricing(DefaultPricing()),
		WithHiddenModelsFile(filepath.Join(t.TempDir(), "hidden-models.txt")))
	body := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	s.wire.HandleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	totals, _, err := store.Query(UsageFilter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if math.Abs(totals.CostUSD-18) > 1e-9 {
		t.Fatalf("cost = %v, want 18", totals.CostUSD)
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
	s := NewServer(stub, "", store)
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer key-abc")
	w := httptest.NewRecorder()
	s.wire.HandleChat(w, req)
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

func TestUsageStoreCompressionTotals(t *testing.T) {
	store := newTestUsageStore(t)
	entries := []CompressionEntry{
		{TS: "2026-11-12T09:00:00Z", Endpoint: "chat", ToolResults: 3, Compressed: 2, BytesBefore: 1000, BytesAfter: 200},
		{TS: "2026-11-13T09:00:00Z", Endpoint: "messages", ToolResults: 1, Compressed: 1, BytesBefore: 500, BytesAfter: 100},
	}
	for _, e := range entries {
		if err := store.RecordCompression(e); err != nil {
			t.Fatalf("RecordCompression: %v", err)
		}
	}

	all, err := store.QueryCompressionTotals(nil)
	if err != nil {
		t.Fatalf("QueryCompressionTotals: %v", err)
	}
	if all.Events != 2 || all.ToolResults != 4 || all.Compressed != 3 {
		t.Fatalf("counts = %+v", all)
	}
	if all.BytesBefore != 1500 || all.BytesAfter != 300 || all.BytesSaved != 1200 {
		t.Fatalf("bytes = %+v", all)
	}
	if all.EstTokensSaved != 300 || !approx(all.SavedPercent, 80) {
		t.Fatalf("derived = %+v", all)
	}

	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	today, err := store.QueryCompressionTotals(&start)
	if err != nil {
		t.Fatalf("QueryCompressionTotals today: %v", err)
	}
	if today.Events != 1 || today.ToolResults != 1 || today.BytesBefore != 500 {
		t.Fatalf("today = %+v", today)
	}
}

func TestCompressionRecordedViaHandleChat(t *testing.T) {
	store := newTestUsageStore(t)
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s := NewServer(stub, "", store, WithCompression(compress.NewDefault()))

	w := httptest.NewRecorder()
	s.wire.HandleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(compressionChatBody()))))
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	totals, err := store.QueryCompressionTotals(nil)
	if err != nil {
		t.Fatalf("QueryCompressionTotals: %v", err)
	}
	if totals.Events != 1 || totals.Compressed != 1 || totals.BytesBefore == 0 || totals.BytesSaved <= 0 {
		t.Fatalf("compression totals = %+v", totals)
	}

	// Requests without compressed tool results are not recorded.
	w = httptest.NewRecorder()
	s.wire.HandleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != 200 {
		t.Fatalf("plain status = %d", w.Code)
	}
	after, err := store.QueryCompressionTotals(nil)
	if err != nil {
		t.Fatalf("QueryCompressionTotals: %v", err)
	}
	if after.Events != 1 {
		t.Fatalf("unexpected compression event for plain request: %+v", after)
	}
}
