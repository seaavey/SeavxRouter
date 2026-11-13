package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalServerURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8080": "http://127.0.0.1:8080",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		":7000":          "http://127.0.0.1:7000",
		"[::1]:7000":     "http://[::1]:7000",
		"bogus":          "",
	}
	for in, want := range cases {
		if got := localServerURL(in); got != want {
			t.Errorf("localServerURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveServer(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "server.url")

	if got := resolveServer("127.0.0.1:8080", "", statePath, ""); got != "http://127.0.0.1:8080" {
		t.Fatalf("without state file = %q", got)
	}

	if got := resolveServer("127.0.0.1:8080", "http://127.0.0.1:1234", statePath, ""); got != "http://127.0.0.1:1234" {
		t.Fatalf("explicit server = %q", got)
	}

	preferred := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer preferred.Close()
	if got := resolveServer("127.0.0.1:8080", "", statePath, preferred.URL); got != preferred.URL {
		t.Fatalf("preferred default = %q, want %q", got, preferred.URL)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := os.WriteFile(statePath, []byte(srv.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveServer("127.0.0.1:8080", "", statePath, preferred.URL); got != srv.URL {
		t.Fatalf("live state file = %q, want %q", got, srv.URL)
	}

	srv.Close()
	if got := resolveServer("127.0.0.1:8080", "", statePath, ""); got != "http://127.0.0.1:8080" {
		t.Fatalf("stale state file = %q", got)
	}
}
