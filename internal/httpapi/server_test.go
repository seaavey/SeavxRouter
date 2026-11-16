package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"seavxrouter/internal/backend"
)

func newTenantServer(t *testing.T, master string, stub *chatStub) (*Server, *KeyStore, *UsageStore) {
	t.Helper()
	keys := newTestKeyStore(t)
	usage := newTestUsageStore(t)
	s := NewServer(stub, master, usage,
		WithKeys(keys),
		WithHiddenModelsFile(filepath.Join(t.TempDir(), "hidden-models.txt")),
	)
	return s, keys, usage
}

func doRequest(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTenantAuthMatrix(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s, keys, _ := newTenantServer(t, "admin", stub)
	h := s.Handler()
	if _, tenantKey, err := keys.Create("acme", 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	} else {
		cases := []struct {
			name       string
			method     string
			path       string
			token      string
			body       string
			wantStatus int
			wantBody   string
		}{
			{"no auth", "GET", "/v1/models", "", "", http.StatusUnauthorized, "invalid_api_key"},
			{"bad key", "GET", "/v1/models", "nope", "", http.StatusUnauthorized, "invalid_api_key"},
			{"master models", "GET", "/v1/models", "admin", "", http.StatusOK, ""},
			{"tenant models", "GET", "/v1/models", tenantKey, "", http.StatusOK, ""},
			{"tenant model by id", "GET", "/v1/models/m", tenantKey, "", http.StatusOK, ""},
			{"tenant operator", "GET", "/v1/usage", tenantKey, "", http.StatusForbidden, "admin_key_required"},
			{"tenant logging", "GET", "/v1/logging", tenantKey, "", http.StatusForbidden, "admin_key_required"},
			{"master operator", "GET", "/v1/usage", "admin", "", http.StatusOK, ""},
			{"tenant key mgmt", "GET", "/v1/keys", tenantKey, "", http.StatusForbidden, "admin_key_required"},
			{"master key mgmt", "GET", "/v1/keys", "admin", "", http.StatusOK, ""},
		}
		for _, tc := range cases {
			rec := doRequest(h, tc.method, tc.path, tc.token, tc.body)
			if rec.Code != tc.wantStatus {
				t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("%s: body = %s, want substring %q", tc.name, rec.Body.String(), tc.wantBody)
			}
		}
	}

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty bearer status = %d", rec.Code)
	}
}

func TestKeyManagementHTTP(t *testing.T) {
	s, _, _ := newTenantServer(t, "admin", &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}})
	h := s.Handler()

	rec := doRequest(h, "POST", "/v1/keys", "admin", `{"name":"globex","budget_usd":10,"limit_token":5000}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("create Cache-Control = %q", got)
	}
	var created CreatedAPIKey
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created key: %v", err)
	}
	if created.ID == 0 || created.Name != "globex" || created.BudgetUSD != 10 || created.LimitToken != 5000 || !strings.HasPrefix(created.Key, keyTokenPrefix) {
		t.Fatalf("created = %+v", created)
	}
	if rec := doRequest(h, "GET", "/v1/models", created.Key, ""); rec.Code != http.StatusOK {
		t.Fatalf("tenant key after create status = %d body %s", rec.Code, rec.Body.String())
	}

	if rec := doRequest(h, "POST", "/v1/keys", "admin", `{"name":"globex"}`); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, "POST", "/v1/keys", "admin", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", rec.Code)
	}
	if rec := doRequest(h, "POST", "/v1/keys", "admin", `{"name":"negative","limit_token":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative limit status = %d body %s", rec.Code, rec.Body.String())
	}

	path := "/v1/keys/" + strconv.FormatInt(created.ID, 10)
	if rec := doRequest(h, "GET", path, "admin", ""); rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, "GET", "/v1/keys/999", "admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing get status = %d", rec.Code)
	}
	if rec := doRequest(h, "GET", "/v1/keys/bogus", "admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", rec.Code)
	}

	rec = doRequest(h, "PATCH", path, "admin", `{"budget_usd":25.5,"limit_token":9000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d body %s", rec.Code, rec.Body.String())
	}
	var patched APIKey
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patched key: %v", err)
	}
	if patched.BudgetUSD != 25.5 || patched.LimitToken != 9000 {
		t.Fatalf("patched budget = %v limit = %v", patched.BudgetUSD, patched.LimitToken)
	}
	if rec := doRequest(h, "PATCH", path, "admin", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("patch without limits status = %d", rec.Code)
	}
	if rec := doRequest(h, "PATCH", path, "admin", `{"limit_token":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("patch negative limit status = %d body %s", rec.Code, rec.Body.String())
	}

	if rec := doRequest(h, "DELETE", path, "admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, "GET", "/v1/models", created.Key, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key status = %d", rec.Code)
	}
	if rec := doRequest(h, "DELETE", path, "admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("idempotent delete status = %d", rec.Code)
	}
	if rec := doRequest(h, "DELETE", "/v1/keys/999", "admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d", rec.Code)
	}
}

func TestTenantUsageAttribution(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{
		Content:      "hi",
		FinishReason: "stop",
		Usage:        backend.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}}
	s, keys, usage := newTenantServer(t, "admin", stub)
	h := s.Handler()
	_, tenantKey, err := keys.Create("acme", 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tenantKey)
	req.Header.Set("X-Seavx-User", "someone-else")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d body %s", rec.Code, rec.Body.String())
	}

	totals, _, err := usage.Query(UsageFilter{User: "key:acme"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if totals.Requests != 1 {
		t.Fatalf("key usage requests = %d, want 1", totals.Requests)
	}
	totals, _, err = usage.Query(UsageFilter{User: "someone-else"})
	if err != nil {
		t.Fatalf("Query spoofed: %v", err)
	}
	if totals.Requests != 0 {
		t.Fatalf("spoofed user requests = %d, want 0", totals.Requests)
	}

	logs, err := usage.QueryLogSummaries(10, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %+v, %v", logs, err)
	}
	if logs[0].User != "key:acme" {
		t.Fatalf("log user = %q, want key:acme", logs[0].User)
	}
}

func TestTenantBudgetEnforcement(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s, keys, usage := newTenantServer(t, "admin", stub)
	h := s.Handler()
	key, tenantKey, err := keys.Create("acme", 1, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := usage.Record(UsageEntry{User: tenantUser("acme"), CostUSD: 2}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	rec := doRequest(h, "POST", "/v1/chat/completions", tenantKey, body)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "insufficient_quota") {
		t.Fatalf("over budget status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, "GET", "/v1/models", tenantKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("models must not be budget-gated: %d", rec.Code)
	}
	if rec := doRequest(h, "POST", "/v1/chat/completions", "admin", body); rec.Code != http.StatusOK {
		t.Fatalf("master chat status = %d body %s", rec.Code, rec.Body.String())
	}

	if _, tenantOther, err := keys.Create("other", 1, 0); err != nil {
		t.Fatalf("Create other: %v", err)
	} else {
		prev := time.Now().UTC().AddDate(0, -1, 0).Format(usageTimestampLayout)
		if err := usage.Record(UsageEntry{TS: prev, User: tenantUser("other"), CostUSD: 100}); err != nil {
			t.Fatalf("Record previous month: %v", err)
		}
		if rec := doRequest(h, "POST", "/v1/chat/completions", tenantOther, body); rec.Code != http.StatusOK {
			t.Fatalf("previous-month spend must not count: %d body %s", rec.Code, rec.Body.String())
		}
	}

	if _, err := keys.SetLimits(key.ID, 5, 0); err != nil {
		t.Fatalf("SetLimits: %v", err)
	}
	if rec := doRequest(h, "POST", "/v1/chat/completions", tenantKey, body); rec.Code != http.StatusOK {
		t.Fatalf("raised budget status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestTenantTokenLimit(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s, keys, usage := newTenantServer(t, "admin", stub)
	h := s.Handler()
	key, tenantKey, err := keys.Create("acme", 0, 10)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := usage.Record(UsageEntry{User: tenantUser("acme"), TotalTokens: 10}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	rec := doRequest(h, "POST", "/v1/chat/completions", tenantKey, body)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "insufficient_quota") {
		t.Fatalf("over token limit status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, "GET", "/v1/models", tenantKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("models must not be token-gated: %d", rec.Code)
	}
	if rec := doRequest(h, "POST", "/v1/chat/completions", "admin", body); rec.Code != http.StatusOK {
		t.Fatalf("master chat status = %d body %s", rec.Code, rec.Body.String())
	}

	if _, tenantOther, err := keys.Create("other", 0, 10); err != nil {
		t.Fatalf("Create other: %v", err)
	} else {
		prev := time.Now().UTC().AddDate(0, -1, 0).Format(usageTimestampLayout)
		if err := usage.Record(UsageEntry{TS: prev, User: tenantUser("other"), TotalTokens: 100}); err != nil {
			t.Fatalf("Record previous month: %v", err)
		}
		if rec := doRequest(h, "POST", "/v1/chat/completions", tenantOther, body); rec.Code != http.StatusOK {
			t.Fatalf("previous-month tokens must not count: %d body %s", rec.Code, rec.Body.String())
		}
	}

	if _, err := keys.SetLimits(key.ID, 0, 20); err != nil {
		t.Fatalf("SetLimits: %v", err)
	}
	if rec := doRequest(h, "POST", "/v1/chat/completions", tenantKey, body); rec.Code != http.StatusOK {
		t.Fatalf("raised token limit status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestKeyManagementDisabledWithoutMaster(t *testing.T) {
	s := NewServer(&chatStub{result: &backend.ChatResult{Content: "ok"}},
		"", newTestUsageStore(t),
		WithKeys(newTestKeyStore(t)),
		WithHiddenModelsFile(filepath.Join(t.TempDir(), "hidden-models.txt")),
	)
	h := s.Handler()

	rec := doRequest(h, "GET", "/v1/keys", "", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "key_management_disabled") {
		t.Fatalf("key management without master status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, "GET", "/v1/usage", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("open operator route status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, "GET", "/v1/models", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("open models status = %d body %s", rec.Code, rec.Body.String())
	}
}
