package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"seavxrouter/internal/auth"
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

func newCustomProviderServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			// Public like Runcrate: the key must still be proven by chat.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v3","name":"DeepSeek V3"}]}`))
		case "/chat/completions":
			switch r.Header.Get("Authorization") {
			case "Bearer rc_test":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
			case "Bearer rc_free":
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode body: %v", err)
				}
				if body.Model != "free-model" {
					w.WriteHeader(http.StatusPaymentRequired)
					_, _ = w.Write([]byte(`{"error":{"message":"model is not part of the free tier"}}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
			case "Bearer rc_broke":
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"error":{"message":"Insufficient balance"}}`))
			default:
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunProviderCmd(t *testing.T) {
	srv := newCustomProviderServer(t)
	authPath := filepath.Join(t.TempDir(), "auth.jsonc")
	store := auth.AuthStore{Path: authPath}
	var out, errOut bytes.Buffer

	// An endpoint with an empty catalog is not a usable provider.
	emptySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer emptySrv.Close()
	var emptyErr bytes.Buffer
	if code := runProviderCmd([]string{"add", "empty", "-base-url", emptySrv.URL, "-api-key", "x", "-auth-file", authPath}, strings.NewReader(""), io.Discard, &emptyErr); code != 1 {
		t.Fatalf("empty catalog exit = %d, want 1", code)
	}
	if !strings.Contains(emptyErr.String(), "cannot verify empty") {
		t.Fatalf("empty catalog stderr = %s", emptyErr.String())
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("empty catalog provider was stored: %v", err)
	}

	code := runProviderCmd([]string{"add", "runcrate", "-base-url", srv.URL + "/", "-api-key", "rc_test", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("add exit = %d, stderr = %s", code, errOut.String())
	}
	for _, want := range []string{"verified runcrate: 1 model(s)", `added custom provider "runcrate"`, "runcrate/<model>"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("add output missing %q: %s", want, out.String())
		}
	}
	stored, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Provider != "runcrate" || stored[0].AccessToken != "rc_test" || stored[0].BaseURL != srv.URL {
		t.Fatalf("stored accounts = %+v", stored)
	}

	out.Reset()
	if code := runProviderCmd([]string{"list", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, errOut.String())
	}
	for _, want := range []string{"NAME", "runcrate", srv.URL, "1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list output missing %q: %s", want, out.String())
		}
	}

	errOut.Reset()
	if code := runProviderCmd([]string{"add", "runcrate", "-base-url", srv.URL, "-api-key", "bad", "-auth-file", authPath}, strings.NewReader(""), io.Discard, &errOut); code != 1 {
		t.Fatalf("bad key exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "cannot verify runcrate") {
		t.Fatalf("bad key stderr = %s", errOut.String())
	}
	if stored, _ := store.Load(); len(stored) != 1 || stored[0].AccessToken != "rc_test" {
		t.Fatalf("failed probe changed credentials: %+v", stored)
	}

	// A quota rejection proves the key authenticated; store it with a warning
	// so the account can be topped up later.
	out.Reset()
	errOut.Reset()
	if code := runProviderCmd([]string{"add", "broke", "-base-url", srv.URL, "-api-key", "rc_broke", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("quota-warning add exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "warning:") || !strings.Contains(out.String(), "no allowance") {
		t.Fatalf("quota-warning output = %s", out.String())
	}
	if code := runProviderCmd([]string{"remove", "broke", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("remove quota-warning provider exit = %d", code)
	}

	// The catalog's first model can be outside a free plan; -model pins the
	// smoke test to a model the operator knows is usable.
	out.Reset()
	errOut.Reset()
	if code := runProviderCmd([]string{"add", "pinned", "-base-url", srv.URL, "-api-key", "rc_free", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("unpinned add exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "warning:") {
		t.Fatalf("unpinned probe should warn about the paid catalog model: %s", out.String())
	}
	if code := runProviderCmd([]string{"remove", "pinned", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("remove unpinned provider exit = %d", code)
	}
	out.Reset()
	errOut.Reset()
	if code := runProviderCmd([]string{"add", "pinned", "-base-url", srv.URL, "-api-key", "rc_free", "-model", "free-model", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("model-pinned add exit = %d, stderr = %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "warning:") {
		t.Fatalf("pinned model should validate without warning: %s", out.String())
	}
	if code := runProviderCmd([]string{"remove", "pinned", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("remove pinned provider exit = %d", code)
	}

	errOut.Reset()
	if code := runProviderCmd([]string{"add", "runcrate", "-base-url", "https://other.example/v1", "-api-key", "rc_test", "-auth-file", authPath}, strings.NewReader(""), io.Discard, &errOut); code != 1 {
		t.Fatalf("different base URL exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "already points at") {
		t.Fatalf("different base URL stderr = %s", errOut.String())
	}

	if code := runProviderCmd([]string{"add", "groq", "-base-url", srv.URL, "-api-key", "x", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("built-in name exit = %d, want 2", code)
	}
	if code := runProviderCmd([]string{"add", "Bad/Name", "-base-url", srv.URL, "-api-key", "x", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("invalid name exit = %d, want 2", code)
	}
	if code := runProviderCmd([]string{"add", "badurl", "-base-url", "not a url", "-api-key", "x", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("invalid URL exit = %d, want 2", code)
	}

	// Missing flags are prompted on stdin.
	out.Reset()
	errOut.Reset()
	if code := runProviderCmd([]string{"add", "alt", "-auth-file", authPath}, strings.NewReader(srv.URL+"\nrc_test\n"), &out, &errOut); code != 0 {
		t.Fatalf("prompted add exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `added custom provider "alt"`) {
		t.Fatalf("prompted add output = %s", out.String())
	}

	out.Reset()
	if code := runProviderCmd([]string{"list", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "alt") {
		t.Fatalf("list output = %s", out.String())
	}

	out.Reset()
	if code := runProviderCmd([]string{"remove", "runcrate", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("remove exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `removed custom provider "runcrate" (1 key(s))`) {
		t.Fatalf("remove output = %s", out.String())
	}
	if code := runProviderCmd([]string{"remove", "groq", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("remove built-in exit = %d, want 2", code)
	}
	if code := runProviderCmd([]string{"remove", "missing", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 1 {
		t.Fatalf("remove missing exit = %d, want 1", code)
	}
	if code := runProviderCmd([]string{"remove", "alt", "-auth-file", authPath}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("remove last exit = %d", code)
	}

	out.Reset()
	if code := runProviderCmd([]string{"list", "-auth-file", authPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "no custom providers" {
		t.Fatalf("empty list = %q", out.String())
	}
}

func TestGroupAuthsCustom(t *testing.T) {
	accounts := groupAuths([]*auth.Auth{
		{Provider: "runcrate", AccessToken: "rc", BaseURL: "https://api.runcrate.ai/v1"},
		{Provider: "legacy-unknown", AccessToken: "x", RefreshToken: "rt"},
	})
	if got := accounts.count("runcrate"); got != 1 {
		t.Fatalf("custom accounts = %d", got)
	}
	if !isProvider("runcrate", accounts) {
		t.Fatal("custom provider not recognized")
	}
	if len(accounts.custom) != 1 {
		t.Fatalf("custom groups = %v", accounts.custom)
	}
	if len(accounts.antigravity) != 1 {
		t.Fatalf("legacy unknown provider should still group with antigravity, got %d", len(accounts.antigravity))
	}
}

func TestRunComboCmd(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.jsonc")
	combosPath := filepath.Join(dir, "combos.jsonc")
	var out, errOut bytes.Buffer

	// Without -combos the file sits next to -auth-file.
	if code := runComboCmd([]string{"add", "Fast", "-models", "groq/llama, codex/gpt-5", "-auth-file", authPath}, &out, &errOut); code != 0 {
		t.Fatalf("add exit = %d, stderr = %s", code, errOut.String())
	}
	if _, err := os.Stat(combosPath); err != nil {
		t.Fatalf("combos file not created next to auth file: %v", err)
	}
	for _, want := range []string{`combo "fast"`, "combo/fast", "groq/llama -> codex/gpt-5"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("add output missing %q: %s", want, out.String())
		}
	}

	out.Reset()
	if code := runComboCmd([]string{"list", "-combos", combosPath}, &out, &errOut); code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, errOut.String())
	}
	for _, want := range []string{"fast", "groq/llama -> codex/gpt-5"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list output missing %q: %s", want, out.String())
		}
	}

	if code := runComboCmd([]string{"add", "empty", "-combos", combosPath}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("add without models exit = %d", code)
	}
	if code := runComboCmd([]string{"add", "Bad Name", "-models", "codex/gpt-5", "-combos", combosPath}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("bad name exit = %d", code)
	}
	if code := runComboCmd([]string{"add", "nested", "-models", "combo/fast", "-combos", combosPath}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("nested combo exit = %d", code)
	}
	if code := runComboCmd([]string{"bogus", "-combos", combosPath}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("unknown action exit = %d", code)
	}

	if code := runComboCmd([]string{"remove", "fast", "-combos", combosPath}, &out, &errOut); code != 0 {
		t.Fatalf("remove exit = %d, stderr = %s", code, errOut.String())
	}
	out.Reset()
	if code := runComboCmd([]string{"list", "-combos", combosPath}, &out, &errOut); code != 0 {
		t.Fatalf("empty list exit = %d", code)
	}
	if strings.TrimSpace(out.String()) != "no combos" {
		t.Fatalf("empty list = %q", out.String())
	}
	if code := runComboCmd([]string{"remove", "fast", "-combos", combosPath}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("missing remove exit = %d", code)
	}
}
