package httpapi

import (
	"log/slog"
	"math"
	"net/http"

	"seavxrouter/internal/backend"
)

func (s *Server) applyCompression(cr *backend.ChatRequest, endpoint string) {
	if s.compressor == nil {
		return
	}
	st := s.compressor.CompressRequest(cr)
	if st.Compressed == 0 {
		return
	}
	saved := st.BytesBefore - st.BytesAfter
	pct := 0.0
	if st.BytesBefore > 0 {
		pct = float64(saved) * 100 / float64(st.BytesBefore)
	}
	slog.Info("compression",
		"endpoint", endpoint,
		"tool_results", st.ToolResults,
		"compressed", st.Compressed,
		"bytes_before", st.BytesBefore,
		"bytes_after", st.BytesAfter,
		"bytes_saved", saved,
		"est_tokens_saved", saved/4,
		"saved_pct", math.Round(pct*10)/10,
	)
	if s.usage != nil {
		err := s.usage.RecordCompression(CompressionEntry{
			Endpoint:    endpoint,
			ToolResults: st.ToolResults,
			Compressed:  st.Compressed,
			BytesBefore: st.BytesBefore,
			BytesAfter:  st.BytesAfter,
		})
		if err != nil {
			slog.Warn("compression record failed", "endpoint", endpoint, "err", err)
		}
	}
}

func (s *Server) handleCompression(w http.ResponseWriter, r *http.Request) {
	snap := s.compressor.Snapshot()
	writeJSON(w, map[string]any{
		"object":                 "compression",
		"enabled":                snap.Enabled,
		"requests":               snap.Requests,
		"tool_results":           snap.ToolResults,
		"compressed":             snap.Compressed,
		"bytes_before":           snap.BytesBefore,
		"bytes_after":            snap.BytesAfter,
		"bytes_saved":            snap.BytesSaved,
		"saved_percent":          math.Round(snap.SavedPercent*10) / 10,
		"estimated_tokens_saved": snap.EstTokensSaved,
		"min_bytes":              snap.MinBytes,
		"min_savings":            snap.MinSavings,
	})
}
