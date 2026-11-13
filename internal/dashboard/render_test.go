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
			RequestLog: httpapi.RequestLog{
				TS:         "2026-11-13T06:30:16Z",
				Endpoint:   "chat",
				Model:      "codebuddy/deepseek-v4-flash",
				Status:     200,
				DurationMS: 4200,
				Usage:      backend.Usage{TotalTokens: 488_600},
			},
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
	for _, prefix := range []string{"TOTALS", "today", "COMPRESSION", "all time", "KEY", "codebuddy/", "TIME", "06:30:16"} {
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
		snap.Recent = append(snap.Recent, httpapi.LogSummary{RequestLog: httpapi.RequestLog{
			TS: fmt.Sprintf("2026-11-13T06:%02d:00Z", i), Endpoint: "chat", Model: "m", Status: 200,
		}})
	}
	frame := render(View{Snapshot: snap, Width: 120, Height: 60})
	if !strings.Contains(frame, "06:39:00") {
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
