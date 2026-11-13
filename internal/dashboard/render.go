package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"seavxrouter/internal/httpapi"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

// View is one dashboard frame.
type View struct {
	Snapshot Snapshot
	Width    int
	Height   int
	Color    bool
	Paused   bool
}

type renderer struct {
	width int
	color bool
}

func render(v View) string {
	width := v.Width
	if width < 60 {
		width = 60
	}
	if width > 180 {
		width = 180
	}
	height := v.Height
	if height < 20 {
		height = 20
	}
	snap := v.Snapshot
	r := renderer{width: width, color: v.Color}

	avail := height - 18
	if avail < 0 {
		avail = 0
	}
	half := (avail + 1) / 2
	groupRows := min(len(snap.Groups), half)
	recentRows := min(len(snap.Recent), avail-groupRows)
	if left := avail - groupRows - recentRows; left > 0 {
		if add := min(left, len(snap.Groups)-groupRows); add > 0 {
			groupRows += add
			left -= add
		}
		if left > 0 {
			recentRows += min(left, len(snap.Recent)-recentRows)
		}
	}

	lines := make([]string, 0, height)
	lines = append(lines, r.title(snap))
	lines = append(lines, r.subtitle(snap, v.Paused))
	lines = append(lines, "")
	lines = append(lines, r.totals(snap)...)
	lines = append(lines, "")
	lines = append(lines, r.compression(snap)...)
	lines = append(lines, "")
	lines = append(lines, r.groups(snap, groupRows)...)
	lines = append(lines, "")
	lines = append(lines, r.recent(snap, recentRows)...)

	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, r.footer(width < 90))
	return strings.Join(lines, "\n")
}

func (r renderer) paint(code, s string) string {
	if !r.color || code == "" {
		return s
	}
	return code + s + ansiReset
}

func (r renderer) title(snap Snapshot) string {
	left := "seavxrouter tui"
	note := snap.LiveNote
	if note == "" {
		note = "db poll"
	}
	style := ansiDim
	switch {
	case snap.Live:
		style = ansiGreen
	case strings.Contains(note, "reconnect"), strings.Contains(note, "offline"):
		style = ansiYellow
	}
	gap := r.width - runeLen(left) - runeLen(note)
	if gap < 1 {
		note = clip(note, max(1, r.width-runeLen(left)-1))
		gap = max(1, r.width-runeLen(left)-runeLen(note))
	}
	return r.paint(ansiBold+ansiCyan, left) + strings.Repeat(" ", gap) + r.paint(style, note)
}

func (r renderer) subtitle(snap Snapshot, paused bool) string {
	parts := []string{"db " + displayPath(snap.DBPath), "group " + snap.GroupBy}
	if paused {
		parts = append(parts, "paused")
	}
	parts = append(parts, "updated "+snap.Generated.UTC().Format("15:04:05")+" UTC")
	line := strings.Join(parts, "  ·  ")
	if snap.Err != "" {
		line += "  ·  error: " + snap.Err
	}
	return r.paint(ansiDim, clip(line, r.width))
}

// fitWidths adjusts the flexible column so the row spans the full terminal
// width instead of leaving empty space on wide screens.
func (r renderer) fitWidths(widths []int, flex int) []int {
	total := 2 * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	delta := r.width - total
	if delta == 0 || flex < 0 || flex >= len(widths) {
		return widths
	}
	if widths[flex]+delta < 6 {
		delta = 6 - widths[flex]
	}
	out := make([]int, len(widths))
	copy(out, widths)
	out[flex] += delta
	return out
}

func (r renderer) totals(snap Snapshot) []string {
	full := r.width >= 96
	var widths []int
	var headerCells []string
	if full {
		widths = []int{10, 8, 9, 10, 8, 9, 10, 10}
		headerCells = []string{"TOTALS", "REQ", "PROMPT", "COMPL", "THINK", "CACHED", "TOTAL", "COST"}
	} else {
		widths = []int{8, 7, 8, 9, 9, 9}
		headerCells = []string{"TOTALS", "REQ", "PROMPT", "COMPL", "TOTAL", "COST"}
	}
	widths = r.fitWidths(widths, 0)
	aligns := rightAligned(len(widths))
	lines := []string{r.sectionHeader(headerCells, widths, aligns)}
	rows := []struct {
		label string
		t     httpapi.UsageTotals
	}{{"today", snap.Today}, {"7 days", snap.Week}, {"all time", snap.Total}}
	for _, row := range rows {
		cells := []string{row.label, humanCount(int64(row.t.Requests)), humanCount(int64(row.t.PromptTokens))}
		if full {
			cells = append(cells,
				humanCount(int64(row.t.CompletionTokens)),
				humanCount(int64(row.t.ThinkingTokens)),
				humanCount(int64(row.t.CachedTokens)),
			)
		} else {
			cells = append(cells, humanCount(int64(row.t.CompletionTokens)))
		}
		cells = append(cells, humanCount(int64(row.t.TotalTokens)), humanCost(row.t.CostUSD))
		lines = append(lines, r.row(cells, widths, aligns, nil))
	}
	return lines
}

func (r renderer) compression(snap Snapshot) []string {
	headers := []string{"COMPRESSION", "EVENTS", "TOOLS", "COMPRESSED", "SAVED", "PCT", "TOKENS"}
	widths := []int{11, 7, 9, 10, 10, 7, 10}
	keep := []int{0, 1, 2, 3, 4, 5, 6}
	if r.width < 76 {
		keep = keep[:6]
	}
	if r.width < 64 {
		keep = []int{0, 1, 2, 4, 5}
	}
	headerCells := make([]string, len(keep))
	colWidths := make([]int, len(keep))
	for i, k := range keep {
		headerCells[i] = headers[k]
		colWidths[i] = widths[k]
	}
	colWidths = r.fitWidths(colWidths, 0)
	aligns := rightAligned(len(colWidths))
	lines := []string{r.sectionHeader(headerCells, colWidths, aligns)}
	rows := []struct {
		label string
		t     httpapi.CompressionTotals
	}{{"today", snap.CompressionToday}, {"all time", snap.Compression}}
	for _, row := range rows {
		pct := "-"
		if row.t.BytesBefore > 0 {
			pct = fmt.Sprintf("%.1f%%", row.t.SavedPercent)
		}
		all := []string{
			row.label,
			humanCount(row.t.Events),
			humanCount(row.t.ToolResults),
			humanCount(row.t.Compressed),
			humanBytes(row.t.BytesSaved),
			pct,
			humanCount(row.t.EstTokensSaved),
		}
		cells := make([]string, len(keep))
		for i, k := range keep {
			cells[i] = all[k]
		}
		lines = append(lines, r.row(cells, colWidths, aligns, nil))
	}
	return lines
}

func (r renderer) groups(snap Snapshot, limit int) []string {
	keyWidth := map[string]int{"day": 10, "provider": 14, "model": 24, "user": 16, "endpoint": 12}[snap.GroupBy]
	if maxKey := r.width - 51; keyWidth > maxKey {
		keyWidth = maxKey
	}
	if keyWidth < 9 {
		keyWidth = 9
	}
	widths := []int{keyWidth, 7, 8, 8, 9, 9}
	widths = r.fitWidths(widths, 0)
	aligns := rightAligned(len(widths))
	lines := []string{r.paint(ansiBold+ansiCyan, "USAGE BY "+strings.ToUpper(snap.GroupBy))}
	lines = append(lines, r.paint(ansiDim, joinCells([]string{"KEY", "REQ", "PROMPT", "COMPL", "TOTAL", "COST"}, widths, aligns)))
	if len(snap.Groups) == 0 {
		return append(lines, r.paint(ansiDim, "  no usage recorded yet"))
	}
	rows := snap.Groups
	if snap.GroupBy == "day" && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	} else if len(rows) > limit {
		rows = rows[:limit]
	}
	for _, g := range rows {
		cells := []string{
			g.Key,
			humanCount(int64(g.Requests)),
			humanCount(int64(g.PromptTokens)),
			humanCount(int64(g.CompletionTokens)),
			humanCount(int64(g.TotalTokens)),
			humanCost(g.CostUSD),
		}
		lines = append(lines, r.row(cells, widths, aligns, nil))
	}
	return lines
}

func (r renderer) recent(snap Snapshot, limit int) []string {
	lines := []string{r.paint(ansiBold+ansiCyan, "RECENT REQUESTS")}
	full := r.width >= 96
	var widths []int
	var headerCells []string
	if full {
		widths = []int{8, 9, 24, 12, 6, 7, 8}
		headerCells = []string{"TIME", "ENDPOINT", "MODEL", "USER", "STATUS", "LAT", "TOKENS"}
	} else {
		widths = []int{8, 9, 22, 6, 7, 8}
		if model := max(10, r.width-48); model < widths[2] {
			widths[2] = model
		}
		headerCells = []string{"TIME", "ENDPOINT", "MODEL", "STATUS", "LAT", "TOKENS"}
	}
	widths = r.fitWidths(widths, 2)
	aligns := make([]bool, len(widths))
	for i := 4; i < len(aligns); i++ {
		aligns[i] = true
	}
	lines = append(lines, r.paint(ansiDim, joinCells(headerCells, widths, aligns)))
	if len(snap.Recent) == 0 {
		return append(lines, r.paint(ansiDim, "  no requests logged yet"))
	}
	for i, e := range snap.Recent {
		if i >= limit {
			break
		}
		statusStyle := ansiGreen
		switch {
		case e.Status >= 500 || e.Status == 0:
			statusStyle = ansiRed
		case e.Status >= 400:
			statusStyle = ansiYellow
		}
		cells := []string{shortTime(e.TS), e.Endpoint, e.Model}
		styles := make([]string, len(widths))
		if full {
			cells = append(cells, e.User)
		}
		cells = append(cells, strconv.Itoa(e.Status), humanDuration(e.DurationMS), humanCount(int64(e.TotalTokens)))
		statusIdx := len(cells) - 3
		styles[statusIdx] = statusStyle
		lines = append(lines, r.row(cells, widths, aligns, styles))
	}
	return lines
}

func (r renderer) footer(narrow bool) string {
	hint := "q quit  ·  1 day  2 provider  3 model  4 user  5 endpoint  ·  r refresh  ·  p pause"
	if narrow {
		hint = "q quit · 1-5 group · r refresh · p pause"
	}
	return r.paint(ansiDim, clip(hint, r.width))
}

func (r renderer) sectionHeader(cells []string, widths []int, aligns []bool) string {
	var b strings.Builder
	for i := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		cell := fitCell(cells[i], widths[i], aligns[i])
		if i == 0 {
			b.WriteString(r.paint(ansiBold+ansiCyan, cell))
		} else {
			b.WriteString(r.paint(ansiDim, cell))
		}
	}
	return b.String()
}

func (r renderer) row(cells []string, widths []int, aligns []bool, styles []string) string {
	var b strings.Builder
	for i := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		cell := fitCell(cells[i], widths[i], aligns[i])
		if i < len(styles) && styles[i] != "" {
			cell = r.paint(styles[i], cell)
		}
		b.WriteString(cell)
	}
	return b.String()
}

func joinCells(cells []string, widths []int, aligns []bool) string {
	var b strings.Builder
	for i := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(fitCell(cells[i], widths[i], aligns[i]))
	}
	return b.String()
}

func rightAligned(n int) []bool {
	out := make([]bool, n)
	for i := 1; i < n; i++ {
		out[i] = true
	}
	return out
}

func fitCell(s string, width int, right bool) string {
	s = clip(s, width)
	pad := width - runeLen(s)
	if pad <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", pad) + s
	}
	return s + strings.Repeat(" ", pad)
}

func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	runes := []rune(s)
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return path
}

func humanCount(n int64) string {
	switch {
	case n <= 0:
		return "0"
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return scaledCount(n, 1e3) + "k"
	case n < 1_000_000_000:
		return scaledCount(n, 1e6) + "m"
	default:
		return scaledCount(n, 1e9) + "b"
	}
}

// scaledCount renders n/div with three decimals and a comma separator,
// e.g. 2700 -> "2,700k".
func scaledCount(n int64, div float64) string {
	return strings.Replace(strconv.FormatFloat(float64(n)/div, 'f', 3, 64), ".", ",", 1)
}

func trimDecimal(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n)
	i := -1
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return trimDecimal(f) + " " + units[i]
}

func humanCost(f float64) string {
	switch {
	case f <= 0:
		return "$0"
	case f < 0.01:
		return fmt.Sprintf("$%.4f", f)
	case f < 1:
		return fmt.Sprintf("$%.3f", f)
	default:
		return fmt.Sprintf("$%.2f", f)
	}
}

func humanDuration(ms int64) string {
	switch {
	case ms <= 0:
		return "-"
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%dm%02ds", ms/60_000, (ms%60_000)/1000)
	}
}

func shortTime(ts string) string {
	if len(ts) >= 19 {
		return ts[11:19]
	}
	return ts
}
