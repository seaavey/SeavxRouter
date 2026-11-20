package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestDashboardFirstLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.db")
	store, err := OpenDashboardStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	newHandler := func(d *DashboardStore) http.Handler {
		return NewServer(&chatStub{}, "", newTestUsageStore(t), WithDashboard(d)).Handler()
	}
	handler := newHandler(store)
	request := func(method, path, body string, cookie *http.Cookie, host, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, recorder.Code, status, recorder.Body)
		}
		return recorder
	}
	const host = "127.0.0.1:5173"
	const origin = "http://" + host
	const defaultBody = `{"password":"` + dashboardDefaultPassword + `"}`
	const newPassword = "a different passphrase"
	const newBody = `{"password":"` + newPassword + `"}`
	request("GET", dashboardPath+"/usage", "", nil, host, "", 401)
	request("GET", "/v1/usage", "", nil, host, "", 403)
	request("POST", dashboardPath+"/login", defaultBody, nil, "router.example", "", 403)
	request("POST", dashboardPath+"/login", defaultBody, nil, host, "https://other.example", 403)
	store.nextLogin = time.Time{}
	setup := request("POST", dashboardPath+"/login", defaultBody, nil, host, origin, 200).Result().Cookies()[0]
	if !setup.HttpOnly || setup.SameSite != http.SameSiteStrictMode || setup.Path != dashboardPath {
		t.Fatalf("unexpected cookie flags: %+v", setup)
	}
	request("GET", dashboardPath+"/usage", "", setup, host, "", 403)
	request("POST", dashboardPath+"/password", newBody, setup, host, "https://other.example", 403)
	request("POST", dashboardPath+"/password", `{"password":"short"}`, setup, host, origin, 400)
	session := request("POST", dashboardPath+"/password", newBody, setup, host, origin, 200).Result().Cookies()[0]
	request("GET", dashboardPath+"/usage", "", setup, host, "", 401)
	request("GET", dashboardPath+"/usage", "", session, host, "", 200)
	request("GET", "/v1/usage", "", session, host, "", 403)
	request("POST", dashboardPath+"/logout", "", session, host, origin, 204)
	request("GET", dashboardPath+"/usage", "", session, host, "", 401)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDashboardStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	handler = newHandler(reopened)
	var hash []byte
	if err := reopened.db.QueryRow("SELECT password_hash FROM dashboard_admin").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(newPassword)) != nil {
		t.Fatal("new password was not persisted as a bcrypt hash")
	}
	request("POST", dashboardPath+"/login", defaultBody, nil, host, origin, 401)
	reopened.nextLogin = time.Time{}
	request("POST", dashboardPath+"/login", newBody, nil, host, origin, 200)
}
