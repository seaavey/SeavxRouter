package dashboard

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/httpapi"
)

func newTestStore(t *testing.T) (*httpapi.UsageStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	store, err := httpapi.OpenUsageStore(path)
	if err != nil {
		t.Fatalf("OpenUsageStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func TestLoad(t *testing.T) {
	store, path := newTestStore(t)
	now := time.Now().UTC()
	today := now.Format("2006-01-02T15:04:05Z")
	older := now.AddDate(0, 0, -2).Format("2006-01-02T15:04:05Z")
	for _, e := range []httpapi.UsageEntry{
		{TS: today, Provider: "codex", Model: "gpt-x", Endpoint: "chat", User: "alice", PromptTokens: 60, CompletionTokens: 40, TotalTokens: 100, CostUSD: 0.5},
		{TS: older, Provider: "kiro", Model: "auto", Endpoint: "messages", User: "bob", TotalTokens: 50, CostUSD: 0.25},
	} {
		if err := store.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if err := store.RecordCompression(httpapi.CompressionEntry{
		TS: today, Endpoint: "chat", ToolResults: 1, Compressed: 1, BytesBefore: 100, BytesAfter: 20,
	}); err != nil {
		t.Fatalf("RecordCompression: %v", err)
	}
	if err := store.RecordLog(httpapi.RequestLog{
		ID: "log_1", TS: today, Endpoint: "chat", Provider: "codex", Model: "gpt-x",
		Status: 200, DurationMS: 1234, Usage: backend.Usage{TotalTokens: 100},
	}); err != nil {
		t.Fatalf("RecordLog: %v", err)
	}

	d := &Dashboard{opts: Options{UsageDB: path}, store: store, groupBy: "day"}
	snap, err := d.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.DBSize <= 0 {
		t.Fatalf("db size = %d, want > 0", snap.DBSize)
	}
	if snap.Today.Requests != 1 || snap.Today.TotalTokens != 100 {
		t.Fatalf("today = %+v", snap.Today)
	}
	if snap.Week.Requests != 2 {
		t.Fatalf("week = %+v", snap.Week)
	}
	if snap.Total.Requests != 2 || math.Abs(snap.Total.CostUSD-0.75) > 1e-9 {
		t.Fatalf("total = %+v", snap.Total)
	}
	if len(snap.Groups) != 2 {
		t.Fatalf("groups = %+v", snap.Groups)
	}
	if snap.Compression.Events != 1 || snap.Compression.BytesSaved != 80 || snap.CompressionToday.Events != 1 {
		t.Fatalf("compression = %+v today = %+v", snap.Compression, snap.CompressionToday)
	}
	if len(snap.Recent) != 1 || snap.Recent[0].ID != "log_1" || snap.Recent[0].TotalTokens != 100 {
		t.Fatalf("recent = %+v", snap.Recent)
	}
	if snap.LiveNote != "db poll" || snap.GroupBy != "day" {
		t.Fatalf("meta = %+v", snap)
	}
}

func TestLoadHourGroupsKeepLatest(t *testing.T) {
	store, path := newTestStore(t)
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for i := 0; i < hourLimit+6; i++ {
		ts := base.Add(time.Duration(i) * time.Hour).Format("2006-01-02T15:04:05Z")
		if err := store.Record(httpapi.UsageEntry{TS: ts, Provider: "codex", Model: "m", Endpoint: "chat", TotalTokens: 1}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	d := &Dashboard{opts: Options{UsageDB: path}, store: store, groupBy: "hour"}
	snap, err := d.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snap.Groups) != hourLimit {
		t.Fatalf("groups = %d, want %d", len(snap.Groups), hourLimit)
	}
	if first, last := snap.Groups[0].Key, snap.Groups[len(snap.Groups)-1].Key; first != "2026-11-10T06" || last != "2026-11-11T05" {
		t.Fatalf("hour range = %s..%s", first, last)
	}
}

func TestOpenValidatesGroupBy(t *testing.T) {
	_, err := Open(Options{UsageDB: filepath.Join(t.TempDir(), "missing.db"), GroupBy: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "invalid group-by") {
		t.Fatalf("err = %v", err)
	}
	_, err = Open(Options{UsageDB: filepath.Join(t.TempDir(), "missing.db")})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing db err = %v", err)
	}
}

func TestSetGroupBy(t *testing.T) {
	d := &Dashboard{groupBy: "day"}
	if d.SetGroupBy("bogus") {
		t.Fatal("accepted invalid group-by")
	}
	if !d.SetGroupBy("provider") || d.GroupBy() != "provider" {
		t.Fatalf("group-by = %q", d.GroupBy())
	}
	if !d.SetGroupBy("hour") || d.GroupBy() != "hour" {
		t.Fatalf("group-by = %q", d.GroupBy())
	}
}

func TestDBSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	if err := os.WriteFile(path, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", make([]byte, 50), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dbSize(path); got != 150 {
		t.Fatalf("dbSize = %d, want 150", got)
	}
	if got := dbSize(filepath.Join(t.TempDir(), "missing.db")); got != 0 {
		t.Fatalf("dbSize missing = %d, want 0", got)
	}
}

func testSnapshot() Snapshot {
	return Snapshot{
		Generated: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC),
		DBPath:    "/home/x/.seavx/usage.db",
		DBSize:    12_582_912,
		GroupBy:   "day",
		Today: httpapi.UsageTotals{
			Requests: 12, PromptTokens: 1234, CompletionTokens: 345,
			ThinkingTokens: 6, CachedTokens: 78, TotalTokens: 1579, CostUSD: 0.021,
		},
		Week: httpapi.UsageTotals{Requests: 40, TotalTokens: 6000, CostUSD: 0.4},
		Total: httpapi.UsageTotals{
			Requests: 120, PromptTokens: 12000, CompletionTokens: 3000,
			TotalTokens: 15000, CostUSD: 1.5,
		},
		Groups: []httpapi.UsageGroup{{
			Key: "2026-11-13",
			UsageTotals: httpapi.UsageTotals{
				Requests: 12, PromptTokens: 1234, CompletionTokens: 345,
				TotalTokens: 1579, CostUSD: 0.021,
			},
		}},
		Compression: httpapi.CompressionTotals{
			Events: 3, ToolResults: 12, Compressed: 9,
			BytesBefore: 200000, BytesAfter: 12000, BytesSaved: 188000,
			SavedPercent: 94, EstTokensSaved: 47000,
		},
		CompressionToday: httpapi.CompressionTotals{
			Events: 1, ToolResults: 4, Compressed: 3,
			BytesBefore: 60000, BytesAfter: 4000, BytesSaved: 56000,
			SavedPercent: 93.3, EstTokensSaved: 14000,
		},
		Recent: []httpapi.LogSummary{{
			RequestLog: httpapi.RequestLog{
				ID: "log_1", TS: "2026-11-13T10:00:00Z", Endpoint: "chat",
				Model: "claude-sonnet-4-6", User: "alice", Status: 200,
				DurationMS: 1234, Usage: backend.Usage{TotalTokens: 1579},
			},
			RequestBytes: 100,
		}},
		Live:     true,
		LiveNote: "sse live",
	}
}

func TestRender(t *testing.T) {
	out := render(View{Snapshot: testSnapshot(), Width: 110, Height: 40})
	for _, want := range []string{
		"seavxrouter tui", "sse live", "size 12 MB", "TOTALS", "COMPRESSION", "USAGE BY DAY",
		"RECENT REQUESTS", "claude-sonnet-4-6", "alice", "$0.021", "93.3%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain render contains ANSI escapes")
	}
	if lines := strings.Count(out, "\n") + 1; lines != 40 {
		t.Fatalf("rendered %d lines, want 40", lines)
	}
	for _, line := range strings.Split(out, "\n") {
		if runeLen(line) > 110 {
			t.Fatalf("line wider than terminal: %q", line)
		}
	}
}

func TestRenderCompact(t *testing.T) {
	out := render(View{Snapshot: testSnapshot(), Width: 80, Height: 24})
	for _, line := range strings.Split(out, "\n") {
		if runeLen(line) > 80 {
			t.Fatalf("line wider than 80 columns: %q", line)
		}
	}
	if lines := strings.Count(out, "\n") + 1; lines > 24 {
		t.Fatalf("rendered %d lines, want <= 24", lines)
	}
	if !strings.Contains(out, "q quit · ↑↓ select · enter · 1-6 group") {
		t.Errorf("compact footer missing:\n%s", out)
	}
}

func TestRenderEmpty(t *testing.T) {
	out := render(View{Snapshot: Snapshot{Generated: time.Now(), GroupBy: "provider", LiveNote: "db poll"}, Width: 100, Height: 30})
	if !strings.Contains(out, "no usage recorded yet") || !strings.Contains(out, "no requests logged yet") {
		t.Fatalf("empty state missing:\n%s", out)
	}
}

func TestPrependRecent(t *testing.T) {
	list := []httpapi.LogSummary{
		{RequestLog: httpapi.RequestLog{ID: "a"}},
		{RequestLog: httpapi.RequestLog{ID: "b"}},
	}
	out := prependRecent(list, httpapi.LogSummary{RequestLog: httpapi.RequestLog{ID: "b"}})
	if len(out) != 2 || out[0].ID != "b" || out[1].ID != "a" {
		t.Fatalf("prependRecent = %+v", out)
	}
}

func TestHumanHelpers(t *testing.T) {
	counts := []struct {
		in   int64
		want string
	}{
		{0, "0"}, {-5, "0"}, {999, "999"}, {1500, "1500"}, {10_000, "10000"},
		{10_001, "10,001K"}, {1_200_000, "1,200M"},
		{2_000_000_000, "2,000B"}, {1_004_673_294, "1,005B"}, {536_500_000, "536,500M"},
	}
	for _, tc := range counts {
		if got := humanCount(tc.in); got != tc.want {
			t.Errorf("humanCount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	bytes := []struct {
		in   int64
		want string
	}{{512, "512 B"}, {2048, "2 KB"}, {1_572_864, "1.5 MB"}}
	for _, tc := range bytes {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	costs := []struct {
		in   float64
		want string
	}{{0, "$0"}, {0.0004, "$0.0004"}, {0.021, "$0.021"}, {1.5, "$1.50"}}
	for _, tc := range costs {
		if got := humanCost(tc.in); got != tc.want {
			t.Errorf("humanCost(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	durations := []struct {
		in   int64
		want string
	}{{0, "-"}, {340, "340ms"}, {1234, "1.2s"}, {65_000, "1m05s"}}
	for _, tc := range durations {
		if got := humanDuration(tc.in); got != tc.want {
			t.Errorf("humanDuration(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	wantShort := time.Date(2026, 11, 13, 10, 9, 8, 0, time.UTC).Local().Format("15:04:05")
	if got := shortTime("2026-11-13T10:09:08Z"); got != wantShort {
		t.Errorf("shortTime = %q, want %q", got, wantShort)
	}
}

func TestStreamLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logging/stream" || r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"nope"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "retry: 3000\n\nevent: ready\ndata: {}\n\n")
		fmt.Fprint(w, "event: log\ndata: {\"id\":\"log_1\",\"timestamp\":\"2026-11-13T10:00:00Z\",\"endpoint\":\"chat\",\"provider\":\"codex\",\"model\":\"gpt-x\",\"user\":\"alice\",\"status\":200,\"duration_ms\":1200,\"stream\":false,\"request_bytes\":10,\"response_bytes\":20,\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"thinking_tokens\":1,\"cached_tokens\":3,\"total_tokens\":7}}\n\n")
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	got := make(chan httpapi.LogSummary, 1)
	_ = streamLogs(ctx, srv.URL, "key", func() { connected <- struct{}{} }, func(e httpapi.LogSummary) {
		select {
		case got <- e:
		default:
		}
	})

	select {
	case <-connected:
	default:
		t.Fatal("onConnect was not called")
	}
	var e httpapi.LogSummary
	select {
	case e = <-got:
	default:
		t.Fatal("no log event parsed")
	}
	if e.ID != "log_1" || e.Endpoint != "chat" || e.Status != 200 || e.DurationMS != 1200 {
		t.Fatalf("log = %+v", e)
	}
	if e.InputTokens != 5 || e.OutputTokens != 2 || e.TotalTokens != 7 || e.CacheReadTokens != 3 {
		t.Fatalf("usage = %+v", e.Usage)
	}
	if e.RequestBytes != 10 || e.ResponseBytes != 20 {
		t.Fatalf("bytes = %+v", e)
	}
}

func TestStreamLogsRejectsBadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid key"}`)
	}))
	defer srv.Close()

	err := streamLogs(context.Background(), srv.URL, "wrong", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}
