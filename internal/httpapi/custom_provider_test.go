package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeCustomProviders records what the handlers asked for and returns canned
// results, so the HTTP contract is tested without touching a real upstream.
type fakeCustomProviders struct {
	mu      sync.Mutex
	probed  []string
	added   []string
	models  []string
	err     error
	probeIn []string
}

func (f *fakeCustomProviders) ProbeCustomProvider(_ context.Context, name, baseURL, sdk, key string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probed = append(f.probed, name+"|"+baseURL+"|"+sdk+"|"+key)
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

func (f *fakeCustomProviders) AddCustomProvider(_ context.Context, name, baseURL, sdk, key string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, name+"|"+baseURL+"|"+sdk+"|"+key)
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

// newCustomProviderServer builds a signed-in dashboard with the add-provider
// routes enabled and returns the handler plus a login helper.
func newCustomProviderServer(t *testing.T, svc CustomProviderService) (http.Handler, *http.Cookie) {
	t.Helper()
	store, err := OpenDashboardStore(t.TempDir() + "/dashboard.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewServer(&chatStub{}, "", newTestUsageStore(t), WithDashboard(store), WithCustomProviders(svc)).Handler()

	token, err := readSetupToken(store.setupPath)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"password":%q,"setup_token":%q}`, dashboardDefaultPassword, token)
	req := httptest.NewRequest("POST", "http://127.0.0.1:5173"+dashboardPath+"/login", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login returned no session cookie")
	}
	// The default password still requires rotation.
	rotate := httptest.NewRequest("POST", "http://127.0.0.1:5173"+dashboardPath+"/password",
		strings.NewReader(fmt.Sprintf(`{"password":"a long enough new password","setup_token":%q}`, token)))
	rotate.RemoteAddr = "127.0.0.1:12345"
	rotate.Header.Set("Origin", "http://127.0.0.1:5173")
	rotate.AddCookie(cookies[0])
	rotateRec := httptest.NewRecorder()
	handler.ServeHTTP(rotateRec, rotate)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("password rotation: %d %s", rotateRec.Code, rotateRec.Body)
	}
	rotated := rotateRec.Result().Cookies()
	if len(rotated) == 0 {
		t.Fatal("rotation returned no session cookie")
	}
	return handler, rotated[0]
}

func readSetupToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func postCustomProvider(t *testing.T, handler http.Handler, cookie *http.Cookie, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "http://127.0.0.1:5173"+path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var decoded map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return rec, decoded
}

func TestAddCustomProviderValidatesThenStores(t *testing.T) {
	svc := &fakeCustomProviders{models: []string{"alpha", "beta"}}
	handler, cookie := newCustomProviderServer(t, svc)

	rec, body := postCustomProvider(t, handler, cookie, dashboardPath+"/connections",
		`{"name":"Mock","base_url":"https://api.example.com/v1/","sdk":"anthropic","api_key":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if body["sdk"] != "anthropic" || body["base_url"] != "https://api.example.com/v1" {
		t.Fatalf("body = %v", body)
	}
	models, _ := body["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models = %v", body["models"])
	}
	// The name is normalized to lowercase and the trailing slash is dropped.
	want := "mock|https://api.example.com/v1|anthropic|sk-test"
	if len(svc.added) != 1 || svc.added[0] != want {
		t.Fatalf("added = %v, want %q", svc.added, want)
	}
}

func TestAddCustomProviderRejectsBadInput(t *testing.T) {
	svc := &fakeCustomProviders{models: []string{"alpha"}}
	handler, cookie := newCustomProviderServer(t, svc)

	cases := []struct {
		name string
		body string
	}{
		{"builtin name", `{"name":"groq","base_url":"https://api.example.com/v1","sdk":"openai","api_key":"sk"}`},
		{"reserved name", `{"name":"combo","base_url":"https://api.example.com/v1","sdk":"openai","api_key":"sk"}`},
		{"bad scheme", `{"name":"mock","base_url":"ftp://api.example.com","sdk":"openai","api_key":"sk"}`},
		{"query string", `{"name":"mock","base_url":"https://api.example.com/v1?x=1","sdk":"openai","api_key":"sk"}`},
		{"unknown sdk", `{"name":"mock","base_url":"https://api.example.com/v1","sdk":"grpc","api_key":"sk"}`},
		{"missing key", `{"name":"mock","base_url":"https://api.example.com/v1","sdk":"openai","api_key":"  "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := postCustomProvider(t, handler, cookie, dashboardPath+"/connections", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
		})
	}
	if len(svc.added) != 0 {
		t.Fatalf("rejected requests must not reach the service: %v", svc.added)
	}
}

func TestValidateCustomProviderDoesNotStore(t *testing.T) {
	svc := &fakeCustomProviders{models: []string{"alpha"}}
	handler, cookie := newCustomProviderServer(t, svc)

	rec, body := postCustomProvider(t, handler, cookie, dashboardPath+"/connections/validate",
		`{"name":"mock","base_url":"https://api.example.com/v1","sdk":"openai","api_key":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	if len(svc.probed) != 1 {
		t.Fatalf("probed = %v", svc.probed)
	}
	if len(svc.added) != 0 {
		t.Fatalf("validate must not store: %v", svc.added)
	}
	if models, _ := body["models"].([]any); len(models) != 1 {
		t.Fatalf("models = %v", body["models"])
	}
}

func TestCustomProviderRequiresSession(t *testing.T) {
	svc := &fakeCustomProviders{models: []string{"alpha"}}
	handler, _ := newCustomProviderServer(t, svc)

	req := httptest.NewRequest("POST", "http://127.0.0.1:5173"+dashboardPath+"/connections",
		strings.NewReader(`{"name":"mock","base_url":"https://api.example.com/v1","sdk":"openai","api_key":"sk"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(svc.probed)+len(svc.added) != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
}
