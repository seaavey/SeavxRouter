package compress

import (
	"strings"
	"testing"

	"seavxrouter/internal/backend"
)

// BenchmarkCompressNoToolMessages covers the common chat shape: compression is
// enabled but the conversation carries no tool results, so the command index
// must not be built.
func BenchmarkCompressNoToolMessages(b *testing.B) {
	e := NewDefault()
	req := &backend.ChatRequest{Messages: []backend.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: strings.Repeat("explain this codebase ", 200)},
		{Role: "assistant", Content: strings.Repeat("sure. ", 200)},
		{Role: "user", Content: "and then?"},
	}}
	b.ReportAllocs()
	for b.Loop() {
		e.CompressRequest(req)
	}
}
