package compress

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"seavxrouter/internal/backend"
)

func toolRequest(content, args string) *backend.ChatRequest {
	return &backend.ChatRequest{Messages: []backend.ChatMessage{
		{Role: "user", Content: "please fix"},
		{Role: "assistant", ToolCalls: []backend.ToolCall{{ID: "call_1", Name: "Bash", Arguments: args}}},
		{Role: "tool", ToolCallID: "call_1", Name: "Bash", Content: content},
	}}
}

func TestCompressRequestDiff(t *testing.T) {
	original := bigDiffFixture()
	req := toolRequest(original, `{"command":"git diff"}`)
	e := NewDefault()
	st := e.CompressRequest(req)
	if st.ToolResults != 1 || st.Compressed != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.BytesBefore != int64(len(original)) || st.BytesAfter >= st.BytesBefore {
		t.Fatalf("byte stats = %+v", st)
	}
	tool := req.Messages[2]
	if len(tool.Content) >= len(original) {
		t.Fatal("tool content was not compressed")
	}
	if !strings.Contains(tool.Content, contextElideMarker) || !strings.Contains(tool.Content, "+new line") {
		t.Errorf("compressed diff lost structure:\n%s", tool.Content)
	}
	if req.Messages[0].Content != "please fix" {
		t.Error("user message was modified")
	}
	snap := e.Snapshot()
	if snap.Requests != 1 || snap.Compressed != 1 || snap.BytesSaved <= 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.EstTokensSaved != snap.BytesSaved/4 {
		t.Errorf("token estimate = %d, want %d", snap.EstTokensSaved, snap.BytesSaved/4)
	}
	if snap.SavedPercent <= 0 {
		t.Errorf("saved percent = %f", snap.SavedPercent)
	}
}

func TestCompressRequestDeterministic(t *testing.T) {
	e := NewDefault()
	r1 := toolRequest(bigDiffFixture(), `{"command":"git diff"}`)
	r2 := toolRequest(bigDiffFixture(), `{"command":"git diff"}`)
	e.CompressRequest(r1)
	e.CompressRequest(r2)
	if r1.Messages[2].Content != r2.Messages[2].Content {
		t.Fatal("compression is not deterministic")
	}
}

func TestCompressRequestBelowThreshold(t *testing.T) {
	req := toolRequest("tiny output", `{"command":"git diff"}`)
	st := NewDefault().CompressRequest(req)
	if st.ToolResults != 1 || st.Compressed != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if req.Messages[2].Content != "tiny output" {
		t.Fatal("small content was modified")
	}
}

func TestCompressRequestSavingBelowMinimum(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 170; i++ {
		fmt.Fprintf(&b, "row %03d: some generic output padding\n", i)
	}
	content := b.String()
	req := toolRequest(content, "")
	st := NewDefault().CompressRequest(req)
	if st.Compressed != 0 {
		t.Fatalf("compressed despite saving below minimum: %+v", st)
	}
	if req.Messages[2].Content != content {
		t.Fatal("content changed despite saving below minimum")
	}
}

func TestCompressRequestKeepsCompactJSON(t *testing.T) {
	jsonContent := `{"items":[` + strings.Repeat(`{"name":"x","value":1},`, 200) + `{"name":"end","value":0}]}`
	req := toolRequest(jsonContent, "")
	st := NewDefault().CompressRequest(req)
	if st.Compressed != 0 {
		t.Fatalf("compact JSON was rewritten: %+v", st)
	}
	if req.Messages[2].Content != jsonContent {
		t.Fatal("compact JSON content changed")
	}
}

func TestCompressRequestMinifiesPrettyJSON(t *testing.T) {
	var b strings.Builder
	b.WriteString("{\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "  \"key_%03d\": \"value %03d\",\n", i, i)
	}
	b.WriteString("  \"done\": true\n}\n")
	req := toolRequest(b.String(), "")
	st := NewDefault().CompressRequest(req)
	if st.Compressed != 1 {
		t.Fatalf("pretty JSON was not minified: %+v", st)
	}
	out := req.Messages[2].Content
	if strings.Contains(out, "\n") {
		t.Errorf("output still contains newlines: %q", out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("minified JSON is invalid: %v", err)
	}
	if v["done"] != true {
		t.Errorf("value changed: %v", v["done"])
	}
}

func TestCompressRequestLeavesInvalidJSON(t *testing.T) {
	content := `{"broken": ` + strings.Repeat("x", 3000) + "}"
	req := toolRequest(content, "")
	st := NewDefault().CompressRequest(req)
	if st.Compressed != 0 {
		t.Fatalf("invalid JSON was compressed: %+v", st)
	}
	if req.Messages[2].Content != content {
		t.Fatal("invalid JSON content changed")
	}
}

func TestCompressRequestDiffWithJSONLines(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/data.json b/data.json\nindex 1..2 100644\n--- a/data.json\n+++ b/data.json\n@@ -1,200 +1,200 @@\n")
	for i := 0; i < 200; i++ {
		if i%50 == 25 {
			b.WriteString("-{\"old\": true}\n")
			b.WriteString("+{\"new\": true}\n")
			continue
		}
		fmt.Fprintf(&b, " context %03d padding padding padding padding\n", i)
	}
	req := toolRequest(b.String(), `{"command":"git diff"}`)
	st := NewDefault().CompressRequest(req)
	if st.Compressed != 1 {
		t.Fatalf("JSON diff was not compressed: %+v", st)
	}
	if !strings.Contains(req.Messages[2].Content, `+{"new": true}`) {
		t.Error("JSON diff lost changed lines")
	}
}

func TestCompressRequestArrayCommand(t *testing.T) {
	req := toolRequest(bigDiffFixture(), `{"command":["bash","-lc","git diff"]}`)
	st := NewDefault().CompressRequest(req)
	if st.Compressed != 1 {
		t.Fatalf("array command not detected: %+v", st)
	}
}

func TestCompressRequestSkipsImages(t *testing.T) {
	req := toolRequest(bigDiffFixture(), `{"command":"git diff"}`)
	req.Messages[2].Images = []backend.ImagePart{{URL: "https://example.com/x.png"}}
	st := NewDefault().CompressRequest(req)
	if st.Compressed != 0 {
		t.Fatalf("message with images was compressed: %+v", st)
	}
}

func TestCompressRequestDisabled(t *testing.T) {
	original := bigDiffFixture()
	req := toolRequest(original, `{"command":"git diff"}`)
	e := New(Options{Enabled: false})
	st := e.CompressRequest(req)
	if st != (Stats{}) {
		t.Fatalf("disabled engine returned stats: %+v", st)
	}
	if req.Messages[2].Content != original {
		t.Fatal("disabled engine modified content")
	}
	snap := e.Snapshot()
	if snap.Enabled || snap.Requests != 0 {
		t.Fatalf("disabled snapshot = %+v", snap)
	}
}

func TestCompressRequestCountsSkippedToolResults(t *testing.T) {
	req := toolRequest(bigDiffFixture(), `{"command":"git diff"}`)
	req.Messages = append(req.Messages, backend.ChatMessage{Role: "tool", ToolCallID: "call_2", Content: "small"})
	st := NewDefault().CompressRequest(req)
	if st.ToolResults != 2 || st.Compressed != 1 {
		t.Fatalf("stats = %+v", st)
	}
}
