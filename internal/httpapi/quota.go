package httpapi

import (
	"net/http"
	"time"

	"seavxrouter/internal/auth"
)

type quotaReporter interface{ Quota() []auth.ProviderQuota }

func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	data := []auth.ProviderQuota{}
	if qr, ok := s.backend.(quotaReporter); ok {
		data = qr.Quota()
	}
	writeJSON(w, map[string]any{
		"object":       "quota",
		"generated_at": time.Now().UTC(),
		"data":         data,
	})
}
