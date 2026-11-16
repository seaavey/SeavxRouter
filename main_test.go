package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestRunKeyCmd(t *testing.T) {
	db := filepath.Join(t.TempDir(), "keys.db")
	var out, errOut bytes.Buffer

	if code := runKeyCmd([]string{"create", "acme", "-budget", "5", "-limit-token", "250000", "-keys-db", db}, &out, &errOut); code != 0 {
		t.Fatalf("create exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `created API key "acme"`) || !strings.Contains(out.String(), "sk-svr-") {
		t.Fatalf("create output = %s", out.String())
	}

	out.Reset()
	if code := runKeyCmd([]string{"list", "-keys-db", db}, &out, &errOut); code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, errOut.String())
	}
	for _, want := range []string{"acme", "$5.00/mo", "250000/mo", "active"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list output missing %q: %s", want, out.String())
		}
	}

	out.Reset()
	if code := runKeyCmd([]string{"revoke", "1", "-keys-db", db}, &out, &errOut); code != 0 {
		t.Fatalf("revoke exit = %d, stderr = %s", code, errOut.String())
	}
	out.Reset()
	if code := runKeyCmd([]string{"list", "-keys-db", db}, &out, &errOut); code != 0 {
		t.Fatalf("list after revoke exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "revoked") {
		t.Fatalf("list after revoke = %s", out.String())
	}

	if code := runKeyCmd([]string{"create", "acme", "-keys-db", db}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("duplicate create exit = %d", code)
	}
	if code := runKeyCmd([]string{"create", "bad-limit", "-limit-token", "-1", "-keys-db", db}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("negative limit create exit = %d", code)
	}
	if code := runKeyCmd([]string{"bogus"}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("unknown action exit = %d", code)
	}
	if code := runKeyCmd([]string{"revoke", "99", "-keys-db", db}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("missing revoke exit = %d", code)
	}
}
