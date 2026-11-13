package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// IsQuotaError reports whether err looks like a quota or rate-limit failure
// worth retrying on another account.
func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "quota") || strings.Contains(msg, "429") ||
		strings.Contains(msg, "resource_exhausted") || strings.Contains(msg, "usage_limit") ||
		strings.Contains(msg, "rate_limit") || strings.Contains(msg, "rate limit")
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
func APIHTTPError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s: %s", resp.Status, e.Error.Message)
	}
	return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
}
