package compress

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	maxGrepPerFile = 10
	maxGrepTotal   = 200
	maxGrepLine    = 300
)

// compressGrep groups file:line:text matches by file so repeated searches over
// one file do not repeat its path on every line.
func compressGrep(s string) string {
	lines := splitLines(s)
	order := make([]string, 0, len(lines))
	perFile := make(map[string][]string)
	total := 0
	matches := 0
	for _, line := range lines {
		if line == "--" || strings.TrimSpace(line) == "" {
			continue
		}
		file, rest, ok := splitGrepLine(line)
		if !ok {
			continue
		}
		matches++
		if total >= maxGrepTotal || len(perFile[file]) >= maxGrepPerFile {
			continue
		}
		if _, seen := perFile[file]; !seen {
			order = append(order, file)
		}
		perFile[file] = append(perFile[file], truncateLine(rest, maxGrepLine))
		total++
	}
	if matches < 2 || len(order) == 0 {
		return compressGeneric(s)
	}
	out := make([]string, 0, len(order)*2)
	for _, file := range order {
		out = append(out, file+" ("+strconv.Itoa(len(perFile[file]))+")")
		for _, match := range perFile[file] {
			out = append(out, "  "+match)
		}
	}
	if matches > total {
		out = append(out, fmt.Sprintf("… [seavx: %d matches elided]", matches-total))
	}
	return joinLines(out)
}

// splitGrepLine parses file:line:text and file:line:column:text, returning the
// file and the line/column/text remainder.
func splitGrepLine(line string) (file, rest string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i <= 0 || i == len(line)-1 {
		return "", "", false
	}
	j := strings.IndexByte(line[i+1:], ':')
	if j <= 0 {
		return "", "", false
	}
	if !allDigits(line[i+1 : i+1+j]) {
		return "", "", false
	}
	return line[:i], line[i+1:], true
}
