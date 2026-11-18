package auth

import (
	"os"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"line comment", "// header\n{\"a\":1}\n// tail", "{\"a\":1}\n"},
		{"block comment", "/* hi */{\"a\":1}", "{\"a\":1}"},
		{"slashes in string", `{"url":"https://x.dev/a//b"}`, `{"url":"https://x.dev/a//b"}`},
		{"escaped quote", `{"s":"a\"//b"}`, `{"s":"a\"//b"}`},
		{"trailing comment", "{\"a\":1} // note", "{\"a\":1} "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(StripJSONC([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthLabel(t *testing.T) {
	cases := []struct {
		name string
		a    Auth
		want string
	}{
		{"email wins", Auth{Email: "a@x.dev", AccountID: "acc", AccessToken: "tok"}, "a@x.dev"},
		{"account id", Auth{AccountID: "acc", AccessToken: "tok"}, "acc"},
		{"key fingerprint", Auth{AccessToken: "tok"}, "key:1a7674eb"},
		{"empty", Auth{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Label(); got != tc.want {
				t.Fatalf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	in := []*Auth{
		{AccessToken: "at1", RefreshToken: "1//rt1", ProjectID: "proj1", Tier: "pro", Email: "a@x.dev"},
		{AccessToken: "at2", RefreshToken: "1//rt2", ProjectID: "proj2", Email: "b@x.dev"},
	}
	if err := store.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].RefreshToken != "1//rt1" || out[1].ProjectID != "proj2" {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestLoadLegacySingleObject(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	legacy := "// legacy\n{\"access_token\":\"at\",\"refresh_token\":\"1//rt\",\"project_id\":\"p\"}\n"
	if err := os.WriteFile(store.Path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].RefreshToken != "1//rt" {
		t.Fatalf("legacy load mismatch: %+v", out)
	}
}

func TestSaveRotatedRefreshToken(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	agy := []*Auth{
		{Provider: "antigravity", RefreshToken: "1//a", Email: "a@x.dev"},
		{Provider: "antigravity", RefreshToken: "1//b", Email: "b@x.dev"},
	}
	codex := []*Auth{{Provider: "codex", RefreshToken: "rt-c", Email: "c@x.dev"}}
	if err := store.Save(agy); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(codex); err != nil {
		t.Fatal(err)
	}
	// codex refreshes its token and saves only its own subset
	codex[0].RefreshToken = "rt-c2"
	if err := store.Save(codex); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 accounts, got %d: %+v", len(out), out)
	}
	counts := map[string]int{}
	for _, a := range out {
		counts[a.Provider]++
	}
	if counts["antigravity"] != 2 || counts["codex"] != 1 {
		t.Fatalf("provider counts wrong: %v", counts)
	}
}

func TestCustomProviderAccountsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	in := []*Auth{
		{Provider: "runcrate", AccessToken: "rc_1", BaseURL: "https://api.runcrate.ai/v1"},
		{Provider: "runcrate", AccessToken: "rc_2", BaseURL: "https://api.runcrate.ai/v1"},
	}
	if err := store.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("accounts = %d, want 2: %+v", len(out), out)
	}
	for _, a := range out {
		if a.Provider != "runcrate" || a.BaseURL != "https://api.runcrate.ai/v1" || a.RefreshToken != "" {
			t.Fatalf("custom account mismatch: %+v", a)
		}
	}
}

func TestRemoveProvider(t *testing.T) {
	dir := t.TempDir()
	store := AuthStore{Path: dir + "/auth.jsonc"}
	if err := store.Save([]*Auth{
		{Provider: "runcrate", AccessToken: "rc_1", BaseURL: "https://api.runcrate.ai/v1"},
		{Provider: "groq", AccessToken: "gsk_1"},
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := store.RemoveProvider("runcrate")
	if err != nil || removed != 1 {
		t.Fatalf("remove = %d, %v", removed, err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Provider != "groq" {
		t.Fatalf("remaining accounts = %+v", out)
	}

	if removed, err := store.RemoveProvider("groq"); err != nil || removed != 1 {
		t.Fatalf("remove last = %d, %v", removed, err)
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatalf("credentials file remains after removing every account: %v", err)
	}
	if removed, err := store.RemoveProvider("runcrate"); err != nil || removed != 0 {
		t.Fatalf("remove missing = %d, %v", removed, err)
	}
}
