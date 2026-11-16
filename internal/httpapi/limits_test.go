package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"seavxrouter/internal/backend"
)

// limitCheckServer builds a server whose usage store can be closed mid-test so
// the spend lookup fails the way a broken database does.
func limitCheckServer(t *testing.T) (*Server, *KeyStore, *UsageStore) {
	t.Helper()
	return newTenantServer(t, "admin", &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}})
}

func TestLimitCheckFailureRejectsLimitedTenant(t *testing.T) {
	s, keys, usage := limitCheckServer(t)
	h := s.Handler()
	if _, tenantKey, err := keys.Create("acme", 5, 0); err != nil {
		t.Fatalf("Create: %v", err)
	} else {
		if err := usage.Close(); err != nil {
			t.Fatalf("closing usage store: %v", err)
		}
		body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
		rec := doRequest(h, "POST", "/v1/chat/completions", tenantKey, body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("limited tenant with broken usage db status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "limit_check_failed") {
			t.Fatalf("body = %s, want limit_check_failed", rec.Body.String())
		}
	}
}

func TestLimitCheckFailureSkipsUnlimitedAndMaster(t *testing.T) {
	s, keys, usage := limitCheckServer(t)
	h := s.Handler()
	_, unlimitedKey, err := keys.Create("nolimit", 0, 0)
	if err != nil {
		t.Fatalf("Create unlimited: %v", err)
	}
	if _, limitedKey, err := keys.Create("acme", 5, 0); err != nil {
		t.Fatalf("Create limited: %v", err)
	} else {
		if err := usage.Close(); err != nil {
			t.Fatalf("closing usage store: %v", err)
		}
		body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`

		// A key without caps never queries usage, so a broken db cannot affect it.
		if rec := doRequest(h, "POST", "/v1/chat/completions", unlimitedKey, body); rec.Code != http.StatusOK {
			t.Fatalf("unlimited key status = %d body %s", rec.Code, rec.Body.String())
		}
		// Master keys bypass limits entirely.
		if rec := doRequest(h, "POST", "/v1/chat/completions", "admin", body); rec.Code != http.StatusOK {
			t.Fatalf("master key status = %d body %s", rec.Code, rec.Body.String())
		}
		// Non-inference routes are not budget gated.
		if rec := doRequest(h, "GET", "/v1/models", limitedKey, ""); rec.Code != http.StatusOK {
			t.Fatalf("models route status = %d body %s", rec.Code, rec.Body.String())
		}
	}
}

// limitCheckProbe records the context state the store sees during a spend
// lookup, so a cancelled client context can be reproduced without a real
// disconnect.
func TestLimitCheckIgnoresCancelledRequestContext(t *testing.T) {
	keys := newTestKeyStore(t)
	usage := newTestUsageStore(t)
	s := NewServer(&chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}},
		"admin", usage,
		WithKeys(keys),
		WithHiddenModelsFile(filepath.Join(t.TempDir(), "hidden-models.txt")),
	)
	_, tenantKey, err := keys.Create("acme", 1, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tenantKey)
	ctx, cancel := context.WithCancel(req.Context())
	cancel() // client is already gone before the limit check runs
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	// The detached context keeps the spend lookup alive even though the caller
	// cancelled, so the tenant is judged on real spend (0 of 1 USD) and passes.
	if rec.Code != http.StatusOK {
		t.Fatalf("cancelled request context status = %d body %s, want 200", rec.Code, rec.Body.String())
	}
}
