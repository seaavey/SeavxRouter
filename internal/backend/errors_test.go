package backend

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAPIHTTPErrorKeepsStatus(t *testing.T) {
	resp := &http.Response{
		StatusCode: 492,
		Status:     "492 Too Many Requests",
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"quota exceeded"}}`)),
	}
	err := APIHTTPError(resp)
	if got := StatusCode(err); got != 492 {
		t.Fatalf("StatusCode = %d, want 492", got)
	}
	if want := "492 Too Many Requests: quota exceeded"; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !IsQuotaError(err) {
		t.Fatal("typed error must still match quota detection")
	}
	if got := StatusCode(errors.New("boom")); got != http.StatusBadGateway {
		t.Fatalf("StatusCode(plain) = %d, want 502", got)
	}
	if got := StatusCode(nil); got != http.StatusBadGateway {
		t.Fatalf("StatusCode(nil) = %d, want 502", got)
	}
}

func TestIsRetryableError(t *testing.T) {
	retryable := []string{
		"503 Service Unavailable",
		"502 Bad Gateway",
		"upstream overloaded",
		"429 rate limit exceeded",
		"quota exhausted",
		"401 Unauthorized",
		"unexpected EOF",
		"read: connection reset by peer",
		// Qoder plan caps surface as 403 with these bodies.
		"qoder HTTP 403: Billing daily count exceeded",
		`qoder HTTP 403: {"pricingUrl":"https://qoder.com/pricing?client=qoder"}`,
	}
	for _, msg := range retryable {
		if !IsRetryableError(errors.New(msg)) {
			t.Errorf("IsRetryableError(%q) = false, want true", msg)
		}
	}
	permanent := []string{
		"400 Bad Request: invalid model",
		"404 not found",
		"403 Forbidden: permission denied",
	}
	for _, msg := range permanent {
		if IsRetryableError(errors.New(msg)) {
			t.Errorf("IsRetryableError(%q) = true, want false", msg)
		}
	}
	if IsRetryableError(nil) {
		t.Error("nil error must not be retryable")
	}
}

func TestAPIHTTPErrorSanitizesNVIDIABody(t *testing.T) {
	// NVIDIA answers 404 with {"title","detail"} and leaks the upstream account
	// ID plus the function UUID in the detail string.
	resp := &http.Response{
		Status:     "404 Not Found",
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader(`{"status":404,"title":"Not Found","detail":"Function 'cd89bd68-13e3-47a9-861e-9a62e6e14b05': Not found for account 'gNziUwRZAk4JnkrOnsLGkZmEx0SG39bORP9QFc7b2_A'"}`)),
	}
	err := APIHTTPError(resp)
	msg := err.Error()
	if strings.Contains(msg, "gNziUwRZAk4") || strings.Contains(msg, "cd89bd68") {
		t.Fatalf("upstream identifiers leaked: %q", msg)
	}
	if !strings.Contains(msg, "Not Found") {
		t.Fatalf("message lost its meaning: %q", msg)
	}
	if StatusCode(err) != http.StatusNotFound {
		t.Fatalf("status = %d", StatusCode(err))
	}
}

func TestAPIHTTPErrorKeepsOpenAIShape(t *testing.T) {
	resp := &http.Response{
		Status:     "401 Unauthorized",
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid API Key","type":"invalid_request_error"}}`)),
	}
	if got := APIHTTPError(resp).Error(); got != "401 Unauthorized: Invalid API Key" {
		t.Fatalf("message = %q", got)
	}
}

func TestAPIHTTPErrorTruncatesPlainText(t *testing.T) {
	resp := &http.Response{
		Status:     "502 Bad Gateway",
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 500))),
	}
	msg := APIHTTPError(resp).Error()
	if len(msg) > 260 {
		t.Fatalf("message not bounded: %d chars", len(msg))
	}
}
