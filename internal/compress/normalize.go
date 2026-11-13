package compress

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const elideMarker = "… [seavx: %d lines elided; re-run with a narrower command for full output]"

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

// normalize applies the always-on cleanup shared by every filter: strip ANSI
// escapes, collapse carriage-return progress output, drop repeated lines and
// blank-line runs. It is a pure function of its input.
func normalize(s string) string {
	return collapseBlanks(dedupConsecutive(collapseCR(stripANSI(s))))
}

func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiRE.ReplaceAllString(s, "")
}

// collapseCR keeps only the final segment of each line overwritten by '\r',
// which is how progress bars render in captured output.
func collapseCR(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if j := strings.LastIndexByte(line, '\r'); j >= 0 {
			lines[i] = line[j+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// dedupConsecutive folds runs of three or more identical lines into one line
// tagged with the repeat count.
func dedupConsecutive(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i
		for j+1 < len(lines) && lines[j+1] == lines[i] {
			j++
		}
		n := j - i + 1
		if n >= 3 && strings.TrimSpace(lines[i]) != "" {
			out = append(out, fmt.Sprintf("%s (×%d)", lines[i], n))
		} else {
			out = append(out, lines[i:j+1]...)
		}
		i = j + 1
	}
	return strings.Join(out, "\n")
}

// collapseBlanks reduces runs of blank lines to a single blank line.
func collapseBlanks(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func truncateLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " … [truncated]"
}

// capLines keeps the head and tail of a long listing and names the number of
// lines it elided, so the model knows output was trimmed.
func capLines(lines []string, head, tail int) []string {
	n := len(lines)
	if n <= head+tail+1 {
		return lines
	}
	elided := n - head - tail
	out := make([]string, 0, head+tail+1)
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf(elideMarker, elided))
	out = append(out, lines[n-tail:]...)
	return out
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// looksLikeJSON reports whether the whole content is bracketed like a JSON
// document. Exact validity is not required: truncated JSON must be left alone
// rather than compressed into something even less parseable.
func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	first, last := t[0], t[len(t)-1]
	return (first == '{' && last == '}') || (first == '[' && last == ']')
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
