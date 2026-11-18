package httpapi

import (
	"path/filepath"
	"testing"
)

// BenchmarkUsageStoreBatched measures the amortized request-path cost of
// recording usage: enqueue into the bounded queue, flushing every batch so the
// producer does not outrun the writer into the synchronous fallback.
func BenchmarkUsageStoreBatched(b *testing.B) {
	store, err := OpenUsageStore(filepath.Join(b.TempDir(), "usage.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	entry := UsageEntry{
		TS: "2026-11-13T10:00:00Z", Provider: "bench", Model: "m", User: "u",
		PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if err := store.Record(entry); err != nil {
			b.Fatal(err)
		}
		i++
		if i%ledgerBatchMax == 0 {
			if err := store.Flush(); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := store.Flush(); err != nil {
		b.Fatal(err)
	}
}
