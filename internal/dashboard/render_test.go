package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/httpapi"
)

func narrowSnapshot() Snapshot {
	return Snapshot{
		Generated: time.Date(2026, 11, 13, 6, 30, 16, 0, time.UTC),
		DBPath:    "/home/user/.seavx/usage.db",
		GroupBy:   "model",
		Today:     httpapi.UsageTotals{Requests: 1800, PromptTokens: 312_800_000, CompletionTokens: 465_600, TotalTokens: 313_300_000, CostUSD: 0.382},
		Week:      httpapi.UsageTotals{Requests: 4200, PromptTokens: 806_500_000, CompletionTokens: 1_300_000, TotalTokens: 807_800_000, CostUSD: 0.382},
		Total:     httpapi.UsageTotals{Requests: 4200, PromptTokens: 806_500_000, CompletionTokens: 1_300_000, TotalTokens: 807_800_000, CostUSD: 0.382},
		Groups: []httpapi.UsageGroup{
			{Key: "codebuddy/deepseek-v4-flash", UsageTotals: httpapi.UsageTotals{Requests: 2300, PromptTokens: 387_100_000, CompletionTokens: 779_800, TotalTokens: 387_900_000, CostUSD: 0.382}},
			{Key: "codex/gpt-5.6-sol", UsageTotals: httpapi.UsageTotals{Requests: 1700, PromptTokens: 395_300_000, CompletionTokens: 304_700, TotalTokens: 395_600_000}},
		},
		Compression:      httpapi.CompressionTotals{Events: 46, ToolResults: 25_100, Compressed: 5_500, BytesBefore: 58_000_000, BytesAfter: 46_000_000, BytesSaved: 12_000_000, SavedPercent: 20.4},
		CompressionToday: httpapi.CompressionTotals{Events: 46, ToolResults: 25_100, Compressed: 5_500, BytesBefore: 58_000_000, BytesAfter: 46_000_000, BytesSaved: 12_000_000, SavedPercent: 20.4},
		Recent: []httpapi.LogSummary{{
			TS:         "2026-11-13T06:30:16Z",
			Endpoint:   "chat",
			Model:      "codebuddy/deepseek-v4-flash",
			Status:     200,
			DurationMS: 4200,
			Usage:      backend.Usage{TotalTokens: 488_600},
		}},
	}
}

// TestRenderFitsTerminalWidth guards the 70-column case: the frame must never
// exceed the terminal width, or every line wraps and the screen scrolls.
func TestRenderFitsTerminalWidth(t *testing.T) {
	snap := narrowSnapshot()
	for _, width := range []int{60, 70, 96, 120} {
		frame := render(View{Snapshot: snap, Width: width, Height: 32})
		lines := strings.Split(frame, "\n")
		if len(lines) != 32 {
			t.Errorf("width %d: got %d lines, want 32", width, len(lines))
		}
		for i, line := range lines {
			if got := utf8.RuneCountInString(line); got > width {
				t.Errorf("width %d: line %d is %d cells: %q", width, i, got, line)
			}
		}
		if !strings.Contains(frame, "q quit") {
			t.Errorf("width %d: footer missing", width)
		}
	}
}

// TestRenderFillsWidth guards that tables stretch to the terminal width
// instead of leaving empty space on wide screens.
func TestRenderFillsWidth(t *testing.T) {
	frame := render(View{Snapshot: narrowSnapshot(), Width: 140, Height: 40})
	lines := strings.Split(frame, "\n")
	for _, prefix := range []string{"TOTALS", "today", "COMPRESSION", "all time", "KEY", "codebuddy/", "TIME", shortTime("2026-11-13T06:30:16Z")} {
		found := false
		for _, line := range lines {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			found = true
			if got := utf8.RuneCountInString(line); got != 140 {
				t.Errorf("line %q width = %d, want 140", prefix, got)
			}
		}
		if !found {
			t.Errorf("no line starts with %q", prefix)
		}
	}
}

// TestRenderRecentFillsHeight guards that tall terminals show more than the
// first dozen recent requests instead of leaving the bottom blank.
func TestRenderRecentFillsHeight(t *testing.T) {
	snap := narrowSnapshot()
	snap.Recent = nil
	for i := 0; i < 40; i++ {
		snap.Recent = append(snap.Recent, httpapi.LogSummary{
			TS: fmt.Sprintf("2026-11-13T06:%02d:00Z", i), Endpoint: "chat", Model: "m", Status: 200,
		})
	}
	frame := render(View{Snapshot: snap, Width: 120, Height: 60})
	if !strings.Contains(frame, shortTime(fmt.Sprintf("2026-11-13T06:%02d:00Z", 39))) {
		t.Errorf("recent rows do not fill a tall terminal:\n%s", frame)
	}
}

func TestRenderNarrowKeepsAllSections(t *testing.T) {
	frame := render(View{Snapshot: narrowSnapshot(), Width: 70, Height: 32})
	for _, want := range []string{"seavxrouter tui", "TOTALS", "COMPRESSION", "USAGE BY MODEL", "RECENT REQUESTS"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q", want)
		}
	}
}

func TestRenderHourGroup(t *testing.T) {
	snap := narrowSnapshot()
	snap.GroupBy = "hour"
	snap.Groups = []httpapi.UsageGroup{
		{Key: "2026-11-13T05", UsageTotals: httpapi.UsageTotals{Requests: 10, TotalTokens: 100}},
		{Key: "2026-11-13T06", UsageTotals: httpapi.UsageTotals{Requests: 20, TotalTokens: 200}},
	}
	frame := render(View{Snapshot: snap, Width: 100, Height: 30})
	for _, want := range []string{"USAGE BY HOUR", localHour("2026-11-13T06")} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q", want)
		}
	}
	if strings.Contains(frame, "2026-11-13T06") && localHour("2026-11-13T06") != "2026-11-13T06" {
		t.Errorf("hour key not converted to local time")
	}
}

func TestLocalHour(t *testing.T) {
	ts, err := time.Parse("2006-01-02T15", "2026-11-13T07")
	if err != nil {
		t.Fatal(err)
	}
	want := ts.In(time.Local).Format("2006-01-02T15")
	if got := localHour("2026-11-13T07"); got != want {
		t.Fatalf("localHour = %q, want %q", got, want)
	}
	if got := localHour("not-a-key"); got != "not-a-key" {
		t.Fatalf("localHour invalid = %q", got)
	}
}

func detailEntry() *httpapi.RequestLog {
	return &httpapi.RequestLog{
		ID:         "abcd1234-5678-90ab-cdef-1234567890ab",
		TS:         "2026-11-13T06:30:16Z",
		Endpoint:   "chat",
		Provider:   "codebuddy",
		Model:      "deepseek-v4-flash",
		User:       "alice",
		Status:     200,
		DurationMS: 1234,
		Stream:     true,
		Usage:      backend.Usage{InputTokens: 1200, OutputTokens: 340, CacheReadTokens: 900, TotalTokens: 1540},
	}
}

func TestRenderDetail(t *testing.T) {
	entry := detailEntry()
	frame := render(View{Detail: entry, Width: 100, Height: 24})
	for _, want := range []string{
		"request abcd1234", "DATE", localStamp(entry.TS), "MODEL", "deepseek-v4-flash",
		"PROVIDER", "codebuddy", "STATUS", "200", "STREAM", "true",
		"INPUT", "OUTPUT", "CACHE READ", "75.0% of input", "TOTAL", "esc back",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("detail missing %q:\n%s", want, frame)
		}
	}
	lines := strings.Split(frame, "\n")
	if len(lines) != 24 {
		t.Fatalf("detail rendered %d lines, want 24", len(lines))
	}
	for _, line := range lines {
		if runeLen(line) > 100 {
			t.Errorf("detail line wider than width: %q", line)
		}
	}
}

func TestLocalStamp(t *testing.T) {
	got := localStamp("2026-11-13T06:30:16Z")
	if !strings.HasPrefix(got, "2026-11-13 ") || strings.Count(got, ":") != 2 {
		t.Fatalf("localStamp = %q", got)
	}
	if got := localStamp("not a time"); got != "not a time" {
		t.Fatalf("localStamp invalid = %q", got)
	}
}

func TestRenderRecentSelectionWindow(t *testing.T) {
	snap := narrowSnapshot()
	snap.Recent = nil
	for i := 0; i < 100; i++ {
		snap.Recent = append(snap.Recent, httpapi.LogSummary{
			ID: fmt.Sprintf("id-%d", i), TS: "2026-11-13T06:30:16Z", Endpoint: "chat",
			Model: fmt.Sprintf("model-%02d", i), Status: 200,
		})
	}
	frame := render(View{Snapshot: snap, Width: 120, Height: 40, Selected: 50})
	if !strings.Contains(frame, "model-50") {
		t.Fatalf("selected row missing from window:\n%s", frame)
	}
	if strings.Contains(frame, "model-00") {
		t.Fatalf("window did not follow selection:\n%s", frame)
	}
	highlighted := render(View{Snapshot: snap, Width: 120, Height: 40, Color: true, Selected: 50})
	var marked []string
	for _, line := range strings.Split(highlighted, "\n") {
		if strings.Contains(line, ansiReverse) {
			marked = append(marked, line)
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "model-50") {
		t.Fatalf("expected exactly the selected row highlighted, got %d rows", len(marked))
	}
}
