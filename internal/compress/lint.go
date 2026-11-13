package compress

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	maxLintPerFile = 20
	maxLintTotal   = 200
	maxLintLine    = 400
)

var (
	lintErrorLineRE = regexp.MustCompile(`^[^\s:][^:]*:\d+:(?:\d+:)?\s`)
	lintSummaryRE   = regexp.MustCompile(`(?i)(\d+\s+problems?|\bcould not compile\b|\bcompilation (failed|terminated)\b|found \d+ errors?)`)
)

// compressLint keeps diagnostic lines grouped per file and always retains the
// headline summary ("N problems", "could not compile", ...).
func compressLint(s string) string {
	lines := splitLines(s)
	order := make([]string, 0, 8)
	perFile := make(map[string][]string)
	summary := make([]string, 0, 4)
	total := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if lintErrorLineRE.MatchString(t) {
			file := t
			if i := strings.IndexByte(t, ':'); i > 0 {
				file = t[:i]
			}
			if _, seen := perFile[file]; !seen {
				order = append(order, file)
			}
			if len(perFile[file]) < maxLintPerFile && total < maxLintTotal {
				detail := strings.TrimPrefix(t[len(file):], ":")
				perFile[file] = append(perFile[file], truncateLine(detail, maxLintLine))
				total++
			}
			continue
		}
		if lintSummaryRE.MatchString(t) {
			summary = append(summary, truncateLine(t, maxLintLine))
		}
	}
	if len(order) == 0 {
		return compressGeneric(s)
	}
	out := make([]string, 0, len(order)*2+len(summary))
	for _, file := range order {
		out = append(out, file+" ("+strconv.Itoa(len(perFile[file]))+")")
		for _, line := range perFile[file] {
			out = append(out, "  "+line)
		}
	}
	out = append(out, summary...)
	return joinLines(out)
}
