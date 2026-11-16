package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"seavxrouter/internal/backend"
)

// apiError is the OpenAI-shaped error envelope every non-2xx response uses.
type apiError struct {
	status int
	msg    string
	typ    string
	code   string
}

func (e *apiError) Error() string { return e.msg }

func writeError(w http.ResponseWriter, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		if backend.IsQuotaError(err) {
			ae = &apiError{http.StatusTooManyRequests, err.Error(), "insufficient_quota", "insufficient_quota"}
		} else {
			ae = &apiError{http.StatusBadGateway, err.Error(), "backend_error", "backend_error"}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": ae.msg, "type": ae.typ, "code": ae.code},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
