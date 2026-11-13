package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAuthFile(t *testing.T, path string, auths []*Auth) {
	t.Helper()
	data, err := json.Marshal(auths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	bump := time.Now().Add(time.Duration(len(auths)) * time.Second)
	if err := os.Chtimes(path, bump, bump); err != nil {
		t.Fatal(err)
	}
}

func TestAccountCacheFirstRefreshLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.jsonc")
	writeAuthFile(t, path, []*Auth{
		{Provider: "codebuddy", Email: "a@example.com", AccessToken: "tok-a"},
		{Provider: "qoder", Email: "b@example.com", AccessToken: "tok-b"},
	})
	c := NewAccountCache(AuthStore{Path: path}, "codebuddy")
	got := c.Refresh(nil)
	if len(got) != 1 || got[0].Email != "a@example.com" {
		t.Fatalf("refresh = %+v, want the single codebuddy account", got)
	}
}

func TestAccountCacheUnchangedKeepsPointers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.jsonc")
	writeAuthFile(t, path, []*Auth{{Provider: "codebuddy", Email: "a@example.com", AccessToken: "tok-a"}})
	c := NewAccountCache(AuthStore{Path: path}, "codebuddy")
	current := c.Refresh(nil)
	if len(current) != 1 {
		t.Fatalf("refresh = %d accounts, want 1", len(current))
	}
	again := c.Refresh(current)
	if len(again) != 1 || again[0] != current[0] {
		t.Fatalf("unchanged file replaced the pointer")
	}
}

func TestAccountCacheAddsAndRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.jsonc")
	writeAuthFile(t, path, []*Auth{{Provider: "codebuddy", Email: "a@example.com", AccessToken: "tok-a"}})
	c := NewAccountCache(AuthStore{Path: path}, "codebuddy")
	current := c.Refresh(nil)
	first := current[0]

	writeAuthFile(t, path, []*Auth{
		{Provider: "codebuddy", Email: "a@example.com", AccessToken: "tok-a"},
		{Provider: "codebuddy", Email: "b@example.com", AccessToken: "tok-b"},
	})
	current = c.Refresh(current)
	if len(current) != 2 {
		t.Fatalf("after add = %d accounts, want 2", len(current))
	}
	if current[0] != first {
		t.Fatalf("existing account lost its pointer after reload")
	}
	if current[1].Email != "b@example.com" || current[1].AccessToken != "tok-b" {
		t.Fatalf("added account = %+v", current[1])
	}

	writeAuthFile(t, path, []*Auth{{Provider: "codebuddy", Email: "b@example.com", AccessToken: "tok-b"}})
	current = c.Refresh(current)
	if len(current) != 1 || current[0].Email != "b@example.com" {
		t.Fatalf("after remove = %+v, want only b@example.com", current)
	}
}

func TestAccountCacheAdoptsNewCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.jsonc")
	writeAuthFile(t, path, []*Auth{{Provider: "codebuddy", Email: "a@example.com", AccessToken: "tok-old"}})
	c := NewAccountCache(AuthStore{Path: path}, "codebuddy")
	current := c.Refresh(nil)
	old := current[0]

	writeAuthFile(t, path, []*Auth{{Provider: "codebuddy", Email: "a@example.com", AccessToken: "tok-new"}})
	current = c.Refresh(current)
	if len(current) != 1 {
		t.Fatalf("refresh = %d accounts, want 1", len(current))
	}
	if current[0] == old || current[0].AccessToken != "tok-new" {
		t.Fatalf("rotated credentials not adopted: %+v", current[0])
	}
}

func TestAccountCacheUnreadableKeepsCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.jsonc")
	writeAuthFile(t, path, []*Auth{{Provider: "codebuddy", Email: "a@example.com", AccessToken: "tok-a"}})
	c := NewAccountCache(AuthStore{Path: path}, "codebuddy")
	current := c.Refresh(nil)

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	bump := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, bump, bump); err != nil {
		t.Fatal(err)
	}
	got := c.Refresh(current)
	if len(got) != 1 || got[0] != current[0] {
		t.Fatalf("unreadable file should keep current accounts")
	}
}
