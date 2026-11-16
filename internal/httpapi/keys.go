package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tenant API keys let one router serve many customers: each key carries its own
// monthly budget and attributes usage to "key:<name>".
var (
	ErrKeyNameTaken     = errors.New("an API key with that name already exists")
	ErrKeyNotFound      = errors.New("API key not found")
	ErrKeyNameInvalid   = errors.New("name must be 1-128 characters")
	ErrKeyBudgetInvalid = errors.New("budget_usd must be a non-negative number")
	ErrKeyLimitInvalid  = errors.New("limit_token must be a non-negative integer")
)

// APIKey is one stored tenant key. The plaintext key is never persisted.
type APIKey struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"created_at"`
	RevokedAt  *string `json:"revoked_at,omitempty"`
	BudgetUSD  float64 `json:"budget_usd"`
	LimitToken int64   `json:"limit_token"`
}

// CreatedAPIKey is returned once by Create and carries the plaintext key.
type CreatedAPIKey struct {
	APIKey
	Key string `json:"key"`
}

// KeyStore persists tenant API keys in SQLite.
type KeyStore struct {
	db *sql.DB
}

const apiKeysSchema = `CREATE TABLE IF NOT EXISTS api_keys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	key_hash TEXT NOT NULL UNIQUE,
	prefix TEXT NOT NULL,
	created_at TEXT NOT NULL,
	revoked_at TEXT,
	budget_usd REAL NOT NULL DEFAULT 0,
	limit_token INTEGER NOT NULL DEFAULT 0
)`

const apiKeyColumns = `id, name, prefix, created_at, revoked_at, budget_usd, limit_token`

const (
	keyTokenPrefix = "sk-svr-"
	maxKeyNameLen  = 128
)

// DefaultKeysDBPath mirrors DefaultUsageDBPath so state stays under ~/.seavx.
func DefaultKeysDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".seavx", "keys.db")
	}
	return filepath.Join(home, ".seavx", "keys.db")
}

// OpenKeyStore opens (or creates) the tenant key database.
func OpenKeyStore(path string) (*KeyStore, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating api key db dir: %w", err)
		}
	}
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(%d)&_pragma=journal_size_limit(%d)",
		path, usageWalAutocheckpoint, usageJournalSizeLimit)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening api key db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(apiKeysSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating api key table: %w", err)
	}
	if err := ensureKeyLimitColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating api key table: %w", err)
	}
	return &KeyStore{db: db}, nil
}

// ensureKeyLimitColumn adds limit_token to databases created before token
// caps existed.
func ensureKeyLimitColumn(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(api_keys)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    any
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "limit_token" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE api_keys ADD COLUMN limit_token INTEGER NOT NULL DEFAULT 0")
	return err
}

// Close truncates the WAL before closing, mirroring UsageStore.Close.
func (s *KeyStore) Close() error {
	var busy, log, checkpointed int
	_ = s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	return s.db.Close()
}

// Create mints a key and returns its plaintext exactly once.
func (s *KeyStore) Create(name string, budgetUSD float64, limitToken int64) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxKeyNameLen {
		return APIKey{}, "", ErrKeyNameInvalid
	}
	if budgetUSD < 0 || math.IsNaN(budgetUSD) || math.IsInf(budgetUSD, 0) {
		return APIKey{}, "", ErrKeyBudgetInvalid
	}
	if limitToken < 0 {
		return APIKey{}, "", ErrKeyLimitInvalid
	}
	plaintext, keyHash, prefix, err := newKeyToken()
	if err != nil {
		return APIKey{}, "", err
	}
	created := time.Now().UTC().Format(usageTimestampLayout)
	res, err := s.db.Exec(
		"INSERT INTO api_keys (name, key_hash, prefix, created_at, budget_usd, limit_token) VALUES (?, ?, ?, ?, ?, ?)",
		name, keyHash, prefix, created, budgetUSD, limitToken,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return APIKey{}, "", ErrKeyNameTaken
		}
		return APIKey{}, "", fmt.Errorf("creating api key: %w", err)
	}
	id, _ := res.LastInsertId()
	return APIKey{ID: id, Name: name, Prefix: prefix, CreatedAt: created, BudgetUSD: budgetUSD, LimitToken: limitToken}, plaintext, nil
}

// List returns every key, newest last, without hashes.
func (s *KeyStore) List() ([]APIKey, error) {
	rows, err := s.db.Query("SELECT " + apiKeyColumns + " FROM api_keys ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("listing api keys: %w", err)
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning api key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Get returns one key by ID, including revoked keys.
func (s *KeyStore) Get(id int64) (*APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRow("SELECT "+apiKeyColumns+" FROM api_keys WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading api key: %w", err)
	}
	return &k, nil
}

// SetLimits updates a key's monthly USD budget and token cap (0 = unlimited).
func (s *KeyStore) SetLimits(id int64, budgetUSD float64, limitToken int64) (*APIKey, error) {
	if budgetUSD < 0 || math.IsNaN(budgetUSD) || math.IsInf(budgetUSD, 0) {
		return nil, ErrKeyBudgetInvalid
	}
	if limitToken < 0 {
		return nil, ErrKeyLimitInvalid
	}
	res, err := s.db.Exec(
		"UPDATE api_keys SET budget_usd = ?, limit_token = ? WHERE id = ?",
		budgetUSD, limitToken, id,
	)
	if err != nil {
		return nil, fmt.Errorf("updating api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrKeyNotFound
	}
	return s.Get(id)
}

// Revoke disables a key without deleting it, keeping usage history intact.
func (s *KeyStore) Revoke(id int64) (*APIKey, error) {
	revoked := time.Now().UTC().Format(usageTimestampLayout)
	res, err := s.db.Exec(
		"UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?",
		revoked, id,
	)
	if err != nil {
		return nil, fmt.Errorf("revoking api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrKeyNotFound
	}
	return s.Get(id)
}

// Lookup returns the active key matching a plaintext token, or nil.
func (s *KeyStore) Lookup(token string) (*APIKey, error) {
	if token == "" {
		return nil, nil
	}
	k, err := scanAPIKey(s.db.QueryRow(
		"SELECT "+apiKeyColumns+" FROM api_keys WHERE key_hash = ? AND revoked_at IS NULL",
		hashKeyToken(token),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up api key: %w", err)
	}
	return &k, nil
}

func scanAPIKey(row interface{ Scan(...any) error }) (APIKey, error) {
	var (
		k       APIKey
		revoked sql.NullString
	)
	err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &revoked, &k.BudgetUSD, &k.LimitToken)
	if revoked.Valid {
		k.RevokedAt = &revoked.String
	}
	return k, err
}

func newKeyToken() (plaintext, keyHash, prefix string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", "", fmt.Errorf("generating api key: %w", err)
	}
	plaintext = keyTokenPrefix + hex.EncodeToString(raw[:])
	keyHash = hashKeyToken(plaintext)
	prefix = plaintext[:len(keyTokenPrefix)+8]
	return plaintext, keyHash, prefix, nil
}

func hashKeyToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Tenant keys are attributed as "key:<name>" so a master client cannot spoof a
// tenant's billing identity through X-Seavx-User.
func tenantUser(name string) string { return "key:" + name }

type tenantKeyCtxKey struct{}

func withTenantKey(ctx context.Context, k *APIKey) context.Context {
	return context.WithValue(ctx, tenantKeyCtxKey{}, k)
}

func tenantKeyFromContext(ctx context.Context) *APIKey {
	k, _ := ctx.Value(tenantKeyCtxKey{}).(*APIKey)
	return k
}

func monthStart(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func writeKeyStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrKeyNameTaken):
		writeError(w, &apiError{http.StatusConflict, err.Error(), "invalid_request_error", "key_name_taken"})
	case errors.Is(err, ErrKeyNotFound):
		writeError(w, &apiError{http.StatusNotFound, err.Error(), "invalid_request_error", "key_not_found"})
	case errors.Is(err, ErrKeyNameInvalid), errors.Is(err, ErrKeyBudgetInvalid), errors.Is(err, ErrKeyLimitInvalid):
		writeError(w, &apiError{http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_key"})
	default:
		slog.Error("api key store error", "err", err)
		writeError(w, &apiError{http.StatusInternalServerError, "api key store error", "server_error", "key_store_error"})
	}
}

func keyPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, &apiError{http.StatusBadRequest, "invalid API key id", "invalid_request_error", "invalid_key_id"})
		return 0, false
	}
	return id, true
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string  `json:"name"`
		BudgetUSD  float64 `json:"budget_usd"`
		LimitToken int64   `json:"limit_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, &apiError{http.StatusBadRequest, "invalid JSON body", "invalid_request_error", "invalid_json"})
		return
	}
	key, plaintext, err := s.keys.Create(req.Name, req.BudgetUSD, req.LimitToken)
	if err != nil {
		writeKeyStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CreatedAPIKey{APIKey: key, Key: plaintext})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.keys.List()
	if err != nil {
		writeKeyStoreError(w, err)
		return
	}
	writeJSON(w, map[string]any{"keys": keys})
}

func (s *Server) handleGetKey(w http.ResponseWriter, r *http.Request) {
	id, ok := keyPathID(w, r)
	if !ok {
		return
	}
	key, err := s.keys.Get(id)
	if err != nil {
		writeKeyStoreError(w, err)
		return
	}
	writeJSON(w, key)
}

func (s *Server) handlePatchKey(w http.ResponseWriter, r *http.Request) {
	id, ok := keyPathID(w, r)
	if !ok {
		return
	}
	var req struct {
		BudgetUSD  *float64 `json:"budget_usd"`
		LimitToken *int64   `json:"limit_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, &apiError{http.StatusBadRequest, "invalid JSON body", "invalid_request_error", "invalid_json"})
		return
	}
	if req.BudgetUSD == nil && req.LimitToken == nil {
		writeError(w, &apiError{http.StatusBadRequest, "budget_usd or limit_token is required", "invalid_request_error", "invalid_key"})
		return
	}
	current, err := s.keys.Get(id)
	if err != nil {
		writeKeyStoreError(w, err)
		return
	}
	budget, limit := current.BudgetUSD, current.LimitToken
	if req.BudgetUSD != nil {
		budget = *req.BudgetUSD
	}
	if req.LimitToken != nil {
		limit = *req.LimitToken
	}
	key, err := s.keys.SetLimits(id, budget, limit)
	if err != nil {
		writeKeyStoreError(w, err)
		return
	}
	writeJSON(w, key)
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, ok := keyPathID(w, r)
	if !ok {
		return
	}
	if _, err := s.keys.Revoke(id); err != nil {
		writeKeyStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
