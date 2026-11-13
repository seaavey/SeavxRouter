package compress

import "strings"

const (
	maxListHead = 80
	maxListTail = 40
	maxListLine = 300
)

func compressListing(s string) string {
	lines := splitLines(s)
	summary := make([]string, 0, 2)
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isListingSummary(trimmed) {
			summary = append(summary, line)
			continue
		}
		body = append(body, truncateLine(line, maxListLine))
	}
	body = capLines(body, maxListHead, maxListTail)
	out := append(summary, body...)
	return joinLines(out)
}

func isListingSummary(trimmed string) bool {
	if strings.HasPrefix(trimmed, "total ") {
		return allDigits(strings.TrimSpace(strings.TrimPrefix(trimmed, "total ")))
	}
	return strings.Contains(trimmed, " directory, ") || strings.Contains(trimmed, " directories, ")
}
