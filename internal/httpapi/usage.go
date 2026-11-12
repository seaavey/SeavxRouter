package httpapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"seavxrouter/internal/backend"
)

// usageTimestampLayout is fixed-width UTC so TEXT comparison stays a valid
// time ordering for range filters.
const usageTimestampLayout = "2006-01-02T15:04:05Z"

// UsageEntry is one recorded billable request.
type UsageEntry struct {
	TS               string
	Provider         string
	Model            string
	Endpoint         string
	User             string
	PromptTokens     int
	CompletionTokens int
	ThinkingTokens   int
	CachedTokens     int
	TotalTokens      int
}

// UsageTotals aggregates usage rows.
type UsageTotals struct {
	Requests         int `json:"requests"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ThinkingTokens   int `json:"thinking_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// UsageGroup is one row of a grouped usage query.
type UsageGroup struct {
	Key string `json:"key"`
	UsageTotals
}

// UsageFilter selects and groups usage rows.
type UsageFilter struct {
	Start    *time.Time
	End      *time.Time
	Provider string
	Model    string
	User     string
	GroupBy  string
}

// UsageStore persists request usage in SQLite.
type UsageStore struct {
	db *sql.DB
}

const usageSchema = `CREATE TABLE IF NOT EXISTS usage (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	provider TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	endpoint TEXT NOT NULL DEFAULT '',
	user TEXT NOT NULL DEFAULT '',
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	thinking_tokens INTEGER NOT NULL DEFAULT 0,
	cached_tokens INTEGER NOT NULL DEFAULT 0,
	total_tokens INTEGER NOT NULL DEFAULT 0
)`

// defaultUsageDBPath mirrors defaultAuthPath: keep state under ~/.seavx.
func DefaultUsageDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".seavx", "usage.db")
	}
	return filepath.Join(home, ".seavx", "usage.db")
}

func OpenUsageStore(path string) (*UsageStore, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating usage db dir: %w", err)
		}
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening usage db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(usageSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating usage table: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS usage_ts ON usage(ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating usage index: %w", err)
	}
	return &UsageStore{db: db}, nil
}

func (s *UsageStore) Close() error { return s.db.Close() }

// Record inserts one usage row, stamping TS with the current UTC time when unset.
func (s *UsageStore) Record(e UsageEntry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(usageTimestampLayout)
	}
	_, err := s.db.Exec(
		`INSERT INTO usage (ts, provider, model, endpoint, user,
			prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS, e.Provider, e.Model, e.Endpoint, e.User,
		e.PromptTokens, e.CompletionTokens, e.ThinkingTokens, e.CachedTokens, e.TotalTokens,
	)
	return err
}

func usageWhere(f UsageFilter) (string, []any) {
	conds := []string{}
	args := []any{}
	if f.Start != nil {
		conds = append(conds, "ts >= ?")
		args = append(args, f.Start.UTC().Format(usageTimestampLayout))
	}
	if f.End != nil {
		conds = append(conds, "ts <= ?")
		args = append(args, f.End.UTC().Format(usageTimestampLayout))
	}
	if f.Provider != "" {
		conds = append(conds, "provider = ?")
		args = append(args, f.Provider)
	}
	if f.Model != "" {
		conds = append(conds, "model = ?")
		args = append(args, f.Model)
	}
	if f.User != "" {
		conds = append(conds, "user = ?")
		args = append(args, f.User)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// usageGroupExpr maps group_by values to an allowlisted SQL expression.
func usageGroupExpr(groupBy string) (string, bool) {
	switch groupBy {
	case "", "day":
		return "substr(ts, 1, 10)", true
	case "provider":
		return "provider", true
	case "model":
		return "model", true
	case "user":
		return "user", true
	case "endpoint":
		return "endpoint", true
	}
	return "", false
}

const usageSumColumns = `COUNT(*),
	COALESCE(SUM(prompt_tokens), 0),
	COALESCE(SUM(completion_tokens), 0),
	COALESCE(SUM(thinking_tokens), 0),
	COALESCE(SUM(cached_tokens), 0),
	COALESCE(SUM(total_tokens), 0)`

func scanUsageTotals(row interface{ Scan(...any) error }, t *UsageTotals) error {
	return row.Scan(&t.Requests, &t.PromptTokens, &t.CompletionTokens, &t.ThinkingTokens, &t.CachedTokens, &t.TotalTokens)
}

// Query returns filtered totals plus one row per group.
func (s *UsageStore) Query(f UsageFilter) (UsageTotals, []UsageGroup, error) {
	where, args := usageWhere(f)

	var totals UsageTotals
	row := s.db.QueryRow("SELECT "+usageSumColumns+" FROM usage"+where, args...)
	if err := scanUsageTotals(row, &totals); err != nil {
		return UsageTotals{}, nil, fmt.Errorf("summing usage: %w", err)
	}

	expr, ok := usageGroupExpr(f.GroupBy)
	if !ok {
		return UsageTotals{}, nil, fmt.Errorf("invalid group_by %q", f.GroupBy)
	}
	rows, err := s.db.Query(
		"SELECT "+expr+" AS key, "+usageSumColumns+" FROM usage"+where+" GROUP BY key ORDER BY key",
		args...,
	)
	if err != nil {
		return UsageTotals{}, nil, fmt.Errorf("grouping usage: %w", err)
	}
	defer rows.Close()

	groups := []UsageGroup{}
	for rows.Next() {
		var g UsageGroup
		err := rows.Scan(&g.Key, &g.Requests, &g.PromptTokens, &g.CompletionTokens,
			&g.ThinkingTokens, &g.CachedTokens, &g.TotalTokens)
		if err != nil {
			return UsageTotals{}, nil, fmt.Errorf("scanning usage group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return UsageTotals{}, nil, fmt.Errorf("iterating usage groups: %w", err)
	}
	return totals, groups, nil
}

// usageUser attributes a request to a user: explicit X-Seavx-User header,
// else a short hash of the bearer token, else "anonymous".
func usageUser(r *http.Request) string {
	if u := strings.TrimSpace(r.Header.Get("X-Seavx-User")); u != "" {
		if len(u) > 128 {
			u = u[:128]
		}
		return u
	}
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
		sum := sha256.Sum256([]byte(token))
		return hex.EncodeToString(sum[:])[:12]
	}
	return "anonymous"
}

// recordUsage persists one successful request. No-op when tracking is disabled.
func (s *Server) recordUsage(r *http.Request, endpoint, model string, u backend.Usage) {
	if s.usage == nil {
		return
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	e := UsageEntry{
		Provider:         s.ownerOf(r.Context(), model),
		Model:            model,
		Endpoint:         endpoint,
		User:             usageUser(r),
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		ThinkingTokens:   u.ThinkingTokens,
		CachedTokens:     u.CacheReadTokens,
		TotalTokens:      total,
	}
	if err := s.usage.Record(e); err != nil {
		slog.Warn("usage record failed", "endpoint", endpoint, "model", model, "err", err)
	}
}

func parseUsageTime(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return &t, nil
	}
	return nil, fmt.Errorf("invalid time %q (want RFC3339 or YYYY-MM-DD)", v)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		writeError(w, &apiError{http.StatusNotFound, "usage tracking is disabled", "invalid_request_error", "usage_disabled"})
		return
	}
	q := r.URL.Query()
	f := UsageFilter{
		Provider: q.Get("provider"),
		Model:    q.Get("model"),
		User:     q.Get("user"),
		GroupBy:  q.Get("group_by"),
	}
	var err error
	if f.Start, err = parseUsageTime(q.Get("start")); err != nil {
		writeError(w, &apiError{http.StatusBadRequest, "invalid start: " + err.Error(), "invalid_request_error", "invalid_time"})
		return
	}
	if f.End, err = parseUsageTime(q.Get("end")); err != nil {
		writeError(w, &apiError{http.StatusBadRequest, "invalid end: " + err.Error(), "invalid_request_error", "invalid_time"})
		return
	}
	if _, ok := usageGroupExpr(f.GroupBy); !ok {
		writeError(w, &apiError{http.StatusBadRequest, `invalid group_by (want day, provider, model, user or endpoint)`, "invalid_request_error", "invalid_group_by"})
		return
	}
	totals, groups, err := s.usage.Query(f)
	if err != nil {
		slog.Warn("usage query failed", "err", err)
		writeError(w, &apiError{http.StatusInternalServerError, "usage query failed", "server_error", "usage_query_failed"})
		return
	}
	writeJSON(w, map[string]any{
		"object":       "usage",
		"generated_at": time.Now().UTC(),
		"totals":       totals,
		"data":         groups,
	})
}
