package compress

import (
	"fmt"
	"strings"
)

const (
	maxDiffLine    = 400
	maxDiffContext = 2
	maxDiffHead    = 400
	maxDiffTail    = 100
	maxStatusFiles = 100
	maxLogCommits  = 50
	maxShowHeader  = 30

	contextElideMarker = "… [seavx: diff context elided]"
)

func compressDiff(s string) string {
	lines := splitLines(s)
	out := make([]string, 0, len(lines))
	inHunk := false
	inBinary := false
	contextRun := 0
	for _, line := range lines {
		switch {
		case isDiffStart(line):
			inHunk = false
			inBinary = false
			contextRun = 0
			out = append(out, line)
		case inBinary:
			continue
		case strings.HasPrefix(line, "GIT binary patch"):
			inBinary = true
			out = append(out, line, "… [seavx: binary patch body elided]")
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			contextRun = 0
			out = append(out, truncateLine(line, maxDiffLine))
		case !inHunk && isDiffHeaderLine(line):
			out = append(out, truncateLine(line, maxDiffLine))
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			out = append(out, line)
		case inHunk && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")):
			contextRun = 0
			out = append(out, truncateLine(line, maxDiffLine))
		case inHunk && strings.HasPrefix(line, " "):
			contextRun++
			switch {
			case contextRun <= maxDiffContext:
				out = append(out, truncateLine(line, maxDiffLine))
			case contextRun == maxDiffContext+1:
				out = append(out, contextElideMarker)
			}
		case strings.HasPrefix(line, "\\ No newline at end of file"):
			out = append(out, line)
		default:
			out = append(out, truncateLine(line, maxDiffLine))
		}
	}
	return joinLines(capLines(out, maxDiffHead, maxDiffTail))
}

func isDiffStart(line string) bool {
	return strings.HasPrefix(line, "diff --git ") ||
		strings.HasPrefix(line, "diff --cc ") ||
		strings.HasPrefix(line, "diff --combined ")
}

func isDiffHeaderLine(line string) bool {
	prefixes := []string{
		"index ", "--- ", "+++ ", "new file mode ", "deleted file mode ",
		"old mode ", "new mode ", "similarity index ", "dissimilarity index ",
		"rename from ", "rename to ", "copy from ", "copy to ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

func compressStatus(s string) string {
	lines := splitLines(s)
	out := make([]string, 0, len(lines))
	section := "changes"
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, `(use "git `) || strings.HasPrefix(trimmed, `(use 'git `) {
			continue
		}
		if isStatusHeader(trimmed) {
			out = append(out, line)
			section = strings.TrimSuffix(trimmed, ":")
			count = 0
			continue
		}
		count++
		if count <= maxStatusFiles {
			out = append(out, line)
		} else if count == maxStatusFiles+1 {
			out = append(out, fmt.Sprintf("… [seavx: more files under %s elided]", section))
		}
	}
	return joinLines(out)
}

func isStatusHeader(trimmed string) bool {
	prefixes := []string{
		"On branch ", "HEAD detached ", "Your branch ", "No commits yet",
		"Changes to be committed:", "Changes not staged for commit:",
		"Untracked files:", "Unmerged paths:", "nothing to commit", "## ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

func compressLog(s string) string {
	lines := splitLines(s)
	out := make([]string, 0, len(lines))
	var hash, date, subject string
	flush := func() {
		if hash == "" {
			return
		}
		h := hash
		if len(h) > 12 {
			h = h[:12]
		}
		entry := h
		if date != "" {
			entry += " " + date
		}
		if subject != "" {
			entry += " " + subject
		}
		out = append(out, truncateLine(entry, 300))
		hash, date, subject = "", "", ""
	}
	inCommit := false
	for _, line := range lines {
		if strings.HasPrefix(line, "commit ") {
			flush()
			hash = strings.TrimSpace(strings.TrimPrefix(line, "commit "))
			inCommit = true
			continue
		}
		if !inCommit {
			if strings.TrimSpace(line) == "" {
				continue
			}
			out = append(out, truncateLine(line, 300))
			continue
		}
		switch {
		case strings.HasPrefix(line, "Author:"), strings.HasPrefix(line, "AuthorDate:"),
			strings.HasPrefix(line, "Commit:"), strings.HasPrefix(line, "CommitDate:"),
			strings.HasPrefix(line, "Merge:"):
			continue
		case strings.HasPrefix(line, "Date:"):
			date = strings.TrimSpace(strings.TrimPrefix(line, "Date:"))
		case strings.HasPrefix(line, "    "):
			if subject == "" {
				subject = strings.TrimSpace(line)
			}
		case strings.TrimSpace(line) == "":
			continue
		}
	}
	flush()
	if len(out) > maxLogCommits {
		elided := len(out) - maxLogCommits
		out = append(out[:maxLogCommits], fmt.Sprintf("… [seavx: %d more commits elided]", elided))
	}
	return joinLines(out)
}

func compressShow(s string) string {
	lines := splitLines(s)
	split := -1
	for i, line := range lines {
		if isDiffStart(line) {
			split = i
			break
		}
	}
	if split < 0 {
		return compressLog(s)
	}
	header := capLines(lines[:split], maxShowHeader, 0)
	diff := splitLines(compressDiff(joinLines(lines[split:])))
	out := append(append([]string{}, header...), diff...)
	return joinLines(capLines(out, maxDiffHead, maxDiffTail))
}
