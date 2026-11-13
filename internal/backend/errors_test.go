package backend

import (
	"errors"
	"testing"
)

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
	}
	for _, msg := range retryable {
		if !IsRetryableError(errors.New(msg)) {
			t.Errorf("IsRetryableError(%q) = false, want true", msg)
		}
	}
	permanent := []string{
		"400 Bad Request: invalid model",
		"404 not found",
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
