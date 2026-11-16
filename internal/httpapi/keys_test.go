package httpapi

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newTestKeyStore(t *testing.T) *KeyStore {
	t.Helper()
	store, err := OpenKeyStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatalf("OpenKeyStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestKeyStoreCRUD(t *testing.T) {
	store := newTestKeyStore(t)

	key, plaintext, err := store.Create("acme", 25, 1000)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(plaintext, keyTokenPrefix) || len(plaintext) != len(keyTokenPrefix)+64 {
		t.Fatalf("plaintext = %q", plaintext)
	}
	if key.ID == 0 || key.Name != "acme" || key.BudgetUSD != 25 || key.LimitToken != 1000 || key.RevokedAt != nil {
		t.Fatalf("key = %+v", key)
	}
	if key.Prefix != plaintext[:len(keyTokenPrefix)+8] {
		t.Fatalf("prefix = %q, plaintext = %q", key.Prefix, plaintext)
	}

	if _, _, err := store.Create("acme", 0, 0); !errors.Is(err, ErrKeyNameTaken) {
		t.Fatalf("duplicate name err = %v", err)
	}
	if _, _, err := store.Create("  ", 0, 0); !errors.Is(err, ErrKeyNameInvalid) {
		t.Fatalf("empty name err = %v", err)
	}
	if _, _, err := store.Create("bad-budget", -1, 0); !errors.Is(err, ErrKeyBudgetInvalid) {
		t.Fatalf("negative budget err = %v", err)
	}
	if _, _, err := store.Create("bad-limit", 0, -1); !errors.Is(err, ErrKeyLimitInvalid) {
		t.Fatalf("negative limit err = %v", err)
	}

	found, err := store.Lookup(plaintext)
	if err != nil || found == nil || found.ID != key.ID {
		t.Fatalf("Lookup = %+v, %v", found, err)
	}
	if found, err := store.Lookup("sk-svr-unknown"); err != nil || found != nil {
		t.Fatalf("unknown Lookup = %+v, %v", found, err)
	}
	if found, err := store.Lookup(""); err != nil || found != nil {
		t.Fatalf("empty Lookup = %+v, %v", found, err)
	}

	list, err := store.List()
	if err != nil || len(list) != 1 || list[0].Name != "acme" {
		t.Fatalf("List = %+v, %v", list, err)
	}

	updated, err := store.SetLimits(key.ID, 50, 2000)
	if err != nil || updated.BudgetUSD != 50 || updated.LimitToken != 2000 {
		t.Fatalf("SetLimits = %+v, %v", updated, err)
	}
	if _, err := store.SetLimits(key.ID, -5, 0); !errors.Is(err, ErrKeyBudgetInvalid) {
		t.Fatalf("negative budget SetLimits err = %v", err)
	}
	if _, err := store.SetLimits(key.ID, 0, -5); !errors.Is(err, ErrKeyLimitInvalid) {
		t.Fatalf("negative limit SetLimits err = %v", err)
	}
	if _, err := store.SetLimits(999, 5, 0); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("missing SetLimits err = %v", err)
	}

	revoked, err := store.Revoke(key.ID)
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("Revoke = %+v, %v", revoked, err)
	}
	if found, err := store.Lookup(plaintext); err != nil || found != nil {
		t.Fatalf("revoked Lookup = %+v, %v", found, err)
	}
	if again, err := store.Revoke(key.ID); err != nil || again.RevokedAt == nil {
		t.Fatalf("idempotent Revoke = %+v, %v", again, err)
	}

	got, err := store.Get(key.ID)
	if err != nil || got.Name != "acme" || got.RevokedAt == nil {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := store.Get(999); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("missing Get err = %v", err)
	}
	if _, err := store.Revoke(999); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("missing Revoke err = %v", err)
	}
}

func TestKeyStoreMigratesLimitTokenColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		key_hash TEXT NOT NULL UNIQUE,
		prefix TEXT NOT NULL,
		created_at TEXT NOT NULL,
		revoked_at TEXT,
		budget_usd REAL NOT NULL DEFAULT 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (name, key_hash, prefix, created_at, budget_usd)
		VALUES ('legacy', 'hash', 'sk-svr-abcd1234', '2026-11-01T00:00:00Z', 5)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenKeyStore(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	legacy, err := store.Get(1)
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	if legacy.BudgetUSD != 5 || legacy.LimitToken != 0 {
		t.Fatalf("legacy key = %+v", legacy)
	}

	key, _, err := store.Create("acme", 0, 500)
	if err != nil {
		t.Fatalf("Create after migration: %v", err)
	}
	if key.LimitToken != 500 {
		t.Fatalf("limit token = %d, want 500", key.LimitToken)
	}
}
