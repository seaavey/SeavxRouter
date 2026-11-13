package compress

import "strings"

const (
	maxTestHead = 300
	maxTestTail = 100
	maxTestLine = 400
)

var testSummaryPrefixes = []string{
	"ok ", "PASS", "FAIL", "test result:", "Tests:", "Test Suites:",
	"Ran ", "BUILD SUCCESS", "BUILD FAILED", "Finished ", "All tests passed",
	"==== ", "Summary", "Tests run:", "took ", "Duration", "passed", "failed",
}

func compressTest(s string) string {
	lines := splitLines(s)
	failed := false
	for _, line := range lines {
		if isTestFailureLine(line) {
			failed = true
			break
		}
	}
	if !failed {
		out := make([]string, 0, 8)
		for _, line := range lines {
			t := strings.TrimSpace(line)
			if t != "" && isTestSummaryLine(t) {
				out = append(out, truncateLine(line, maxTestLine))
			}
		}
		if len(out) == 0 {
			out = lastNonEmptyLines(lines, 5)
		}
		if len(out) > 20 {
			out = out[len(out)-20:]
		}
		return joinLines(out)
	}
	out := make([]string, 0, len(lines))
	inFailure := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if isTestFailureLine(line) {
			inFailure = true
			out = append(out, truncateLine(line, maxTestLine))
			continue
		}
		if inFailure {
			switch {
			case isTestPassBoundary(t):
				inFailure = false
			case isTestSummaryLine(t):
				out = append(out, truncateLine(line, maxTestLine))
				inFailure = false
			case t != "":
				out = append(out, truncateLine(line, maxTestLine))
			}
			continue
		}
		if isTestSummaryLine(t) {
			out = append(out, truncateLine(line, maxTestLine))
		}
	}
	return joinLines(capLines(out, maxTestHead, maxTestTail))
}

func isTestFailureLine(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "--- FAIL"),
		strings.HasPrefix(t, "FAILED "), t == "FAILED",
		strings.HasPrefix(t, "=== FAILURES ==="), strings.HasPrefix(t, "--- FAILURES ---"),
		strings.HasPrefix(t, "panic:"), strings.HasPrefix(t, "AssertionError"),
		strings.HasPrefix(t, "Error Trace:"), strings.HasPrefix(t, "●"),
		strings.Contains(t, "✕"), strings.Contains(t, "✗"):
		return true
	case strings.HasPrefix(t, "FAIL"):
		return true
	case strings.HasSuffix(t, " FAILED"):
		return true
	case strings.HasPrefix(t, "Tests:") && strings.Contains(t, "failed"):
		return true
	case strings.Contains(t, "test result: FAILED"):
		return true
	}
	return false
}

func isTestSummaryLine(t string) bool {
	for _, prefix := range testSummaryPrefixes {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func isTestPassBoundary(t string) bool {
	return strings.HasPrefix(t, "=== RUN") || strings.HasPrefix(t, "=== PAUSE") ||
		strings.HasPrefix(t, "=== CONT") || strings.HasPrefix(t, "--- PASS")
}

func lastNonEmptyLines(lines []string, n int) []string {
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, truncateLine(lines[i], maxTestLine))
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
