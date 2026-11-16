package completions

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorMapsQuotaTo429(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("qoder HTTP 403: Billing daily count exceeded"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "insufficient_quota" || body.Error.Code != "insufficient_quota" {
		t.Fatalf("error = %+v", body.Error)
	}
}

func TestWriteErrorKeepsGatewayError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("connection reset by peer"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestFailureStatusMapsQuotaTo429(t *testing.T) {
	if got := failureStatus(errors.New("quota: 402 Payment Required")); got != http.StatusTooManyRequests {
		t.Fatalf("quota status = %d, want 429", got)
	}
	if got := failureStatus(errors.New("connection reset by peer")); got != http.StatusBadGateway {
		t.Fatalf("generic status = %d, want 502", got)
	}
}
