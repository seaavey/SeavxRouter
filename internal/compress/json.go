package compress

import (
	"bytes"
	"encoding/json"
	"strings"
)

// minifyJSON removes insignificant whitespace from a single JSON document or a
// JSONL stream, preserving key order and number literals exactly. It returns
// "" when the content is not valid JSON so callers leave it untouched.
func minifyJSON(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(t)) == nil {
		return buf.String()
	}
	lines := splitLines(t)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var lb bytes.Buffer
		if json.Compact(&lb, []byte(line)) != nil {
			return ""
		}
		out = append(out, lb.String())
	}
	if len(out) == 0 {
		return ""
	}
	return joinLines(out)
}
