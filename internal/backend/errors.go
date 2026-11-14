package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// HTTPError is a non-2xx upstream response that keeps the status code, so
// handlers can report the real failure after a 200 was already sent.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return e.Message }

// StatusCode returns the upstream HTTP status carried by err, or 502 when err
// is not an HTTPError.
func StatusCode(err error) int {
	var he *HTTPError
	if errors.As(err, &he) && he.Status >= 400 {
		return he.Status
	}
	return http.StatusBadGateway
}

// IsQuotaError reports whether err looks like a quota or rate-limit failure
// worth retrying on another account.
func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "quota") || strings.Contains(msg, "429") ||
		strings.Contains(msg, "resource_exhausted") || strings.Contains(msg, "usage_limit") ||
		strings.Contains(msg, "rate_limit") || strings.Contains(msg, "rate limit") ||
		// Qoder's plan cap: "Billing daily count exceeded" or a bare
		// {"pricingUrl":...} 403. Both mean the plan is out of allowance.
		strings.Contains(msg, "billing") || strings.Contains(msg, "pricingurl")
}

// IsRetryableError reports whether err is a transient upstream failure worth
// retrying, either on the same provider or on a fallback provider. Quota and
// auth failures qualify because another provider may serve the same model.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if IsQuotaError(err) || IsAuthError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"internal server error", "bad gateway", "service unavailable",
		"gateway timeout", "overloaded", "temporarily unavailable",
		"timeout", "timed out", "connection reset", "connection refused",
		"broken pipe", "unexpected eof", "stream error", "transport is closing",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return msg == "eof"
}

// IsAuthError reports whether err looks like an authentication failure.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "invalid_token") || strings.Contains(msg, "invalid token")
}

// APIHTTPError converts a non-200 upstream response into a descriptive error.
// Upstream bodies are reduced to a short, sanitized message: provider internals
// such as account IDs, function UUIDs and raw JSON envelopes never reach the
// client.
func APIHTTPError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return &HTTPError{
		Status:  resp.StatusCode,
		Message: fmt.Sprintf("%s: %s", resp.Status, sanitizeUpstreamMessage(data)),
	}
}

// sanitizeUpstreamMessage extracts the human-readable part of an upstream error
// body, accepting the OpenAI ({"error":{"message"}}), NVIDIA
// ({"title","detail"}) and plain-text shapes.
func sanitizeUpstreamMessage(data []byte) string {
	raw := strings.TrimSpace(string(data))
	var envelope struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		switch {
		case envelope.Error != nil && envelope.Error.Message != "":
			return redactInternals(envelope.Error.Message)
		case envelope.Detail != "":
			// The HTTP status line already carries the title, so only the detail
			// is kept, with provider-internal identifiers removed.
			if detail := redactInternals(envelope.Detail); detail != "" {
				return detail
			}
			return redactInternals(envelope.Title)
		case envelope.Title != "":
			return redactInternals(envelope.Title)
		}
	}
	// Not JSON: keep a bounded prefix so a stray HTML page cannot flood the
	// response.
	if len(raw) > 200 {
		raw = raw[:200] + "..."
	}
	return redactInternals(raw)
}

// redactInternals strips provider-internal identifiers from an error message.
// NVIDIA, for example, reports the upstream account and function UUID.
var internalDetail = regexp.MustCompile(`(?i)\s*for account '[^']*'|Function '[^']*':?`)

func redactInternals(msg string) string {
	msg = internalDetail.ReplaceAllString(msg, "")
	// Tidy the punctuation left behind by the removed clauses.
	msg = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(msg), ":"))
	msg = strings.Join(strings.Fields(msg), " ")
	return strings.TrimSuffix(msg, ":")
}
