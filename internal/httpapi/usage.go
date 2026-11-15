package httpapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

// Keep the WAL near 1 MiB instead of the default 1000 pages (~4 MiB).
const (
	usageWalAutocheckpoint = 256
	usageJournalSizeLimit  = 1 << 20
)

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
	CostUSD          float64
}

// UsageTotals aggregates usage rows.
type UsageTotals struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ThinkingTokens   int     `json:"thinking_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// UsageGroup is one row of a grouped usage query.
type UsageGroup struct {
	Key string `json:"key"`
	UsageTotals
}

// CompressionEntry is one persisted compression event: a request whose tool
// results were rewritten before reaching the backend.
type CompressionEntry struct {
	TS          string
	Endpoint    string
	ToolResults int64
	Compressed  int64
	BytesBefore int64
	BytesAfter  int64
}

// CompressionTotals aggregates persisted compression events.
type CompressionTotals struct {
	Events         int64   `json:"events"`
	ToolResults    int64   `json:"tool_results"`
	Compressed     int64   `json:"compressed"`
	BytesBefore    int64   `json:"bytes_before"`
	BytesAfter     int64   `json:"bytes_after"`
	BytesSaved     int64   `json:"bytes_saved"`
	SavedPercent   float64 `json:"saved_percent"`
	EstTokensSaved int64   `json:"estimated_tokens_saved"`
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
	total_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd REAL NOT NULL DEFAULT 0
)`

const compressionSchema = `CREATE TABLE IF NOT EXISTS compression (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	endpoint TEXT NOT NULL DEFAULT '',
	tool_results INTEGER NOT NULL DEFAULT 0,
	compressed INTEGER NOT NULL DEFAULT 0,
	bytes_before INTEGER NOT NULL DEFAULT 0,
	bytes_after INTEGER NOT NULL DEFAULT 0
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
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(%d)&_pragma=journal_size_limit(%d)",
		path, usageWalAutocheckpoint, usageJournalSizeLimit)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening usage db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(usageSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating usage table: %w", err)
	}
	if err := ensureUsageCostColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating usage table: %w", err)
	}
	if _, err := db.Exec(requestLogSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating request log table: %w", err)
	}
	if _, err := db.Exec(compressionSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating compression table: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS usage_ts ON usage(ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating usage index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS request_logs_ts ON request_logs(ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating request log index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS compression_ts ON compression(ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating compression index: %w", err)
	}
	return &UsageStore{db: db}, nil
}

// Close truncates the WAL back into the main database before closing so the
// -wal file does not linger at its capped size after shutdown.
func (s *UsageStore) Close() error {
	var busy, log, checkpointed int
	_ = s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	return s.db.Close()
}

// ensureUsageCostColumn adds cost_usd to databases created before cost
// tracking existed.
func ensureUsageCostColumn(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(usage)")
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
		if name == "cost_usd" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE usage ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0")
	return err
}

// Record inserts one usage row, stamping TS with the current UTC time when unset.
func (s *UsageStore) Record(e UsageEntry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(usageTimestampLayout)
	}
	_, err := s.db.Exec(
		`INSERT INTO usage (ts, provider, model, endpoint, user,
			prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS, e.Provider, e.Model, e.Endpoint, e.User,
		e.PromptTokens, e.CompletionTokens, e.ThinkingTokens, e.CachedTokens, e.TotalTokens, e.CostUSD,
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
	case "hour":
		return "substr(ts, 1, 13)", true
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
	COALESCE(SUM(total_tokens), 0),
	COALESCE(SUM(cost_usd), 0)`

func scanUsageTotals(row interface{ Scan(...any) error }, t *UsageTotals) error {
	return row.Scan(&t.Requests, &t.PromptTokens, &t.CompletionTokens, &t.ThinkingTokens, &t.CachedTokens, &t.TotalTokens, &t.CostUSD)
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
			&g.ThinkingTokens, &g.CachedTokens, &g.TotalTokens, &g.CostUSD)
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

// RecordCompression inserts one compression event, stamping TS with the
// current UTC time when unset.
func (s *UsageStore) RecordCompression(e CompressionEntry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(usageTimestampLayout)
	}
	_, err := s.db.Exec(
		`INSERT INTO compression (ts, endpoint, tool_results, compressed, bytes_before, bytes_after)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.TS, e.Endpoint, e.ToolResults, e.Compressed, e.BytesBefore, e.BytesAfter,
	)
	return err
}

// QueryCompressionTotals returns persisted compression totals since start
// (nil = all time).
func (s *UsageStore) QueryCompressionTotals(start *time.Time) (CompressionTotals, error) {
	where := ""
	args := []any{}
	if start != nil {
		where = " WHERE ts >= ?"
		args = append(args, start.UTC().Format(usageTimestampLayout))
	}
	var t CompressionTotals
	err := s.db.QueryRow(
		`SELECT COUNT(*),
			COALESCE(SUM(tool_results), 0),
			COALESCE(SUM(compressed), 0),
			COALESCE(SUM(bytes_before), 0),
			COALESCE(SUM(bytes_after), 0)
		 FROM compression`+where,
		args...,
	).Scan(&t.Events, &t.ToolResults, &t.Compressed, &t.BytesBefore, &t.BytesAfter)
	if err != nil {
		return CompressionTotals{}, fmt.Errorf("summing compression: %w", err)
	}
	t.BytesSaved = t.BytesBefore - t.BytesAfter
	t.EstTokensSaved = t.BytesSaved / 4
	if t.BytesBefore > 0 {
		t.SavedPercent = float64(t.BytesSaved) * 100 / float64(t.BytesBefore)
	}
	return t, nil
}

// recordResult persists one successful request and completes the request log
// entry attached by the logging middleware. No-op when tracking is disabled.
func (s *Server) recordResult(r *http.Request, endpoint, model string, result *backend.ChatResult) {
	u := result.Usage
	if entry := logFromContext(r.Context()); entry != nil {
		entry.Model = model
		entry.Provider = s.ownerOf(r.Context(), model)
		entry.Usage = u
		resp := struct {
			Content      string             `json:"content,omitempty"`
			ToolCalls    []backend.ToolCall `json:"tool_calls,omitempty"`
			FinishReason string             `json:"finish_reason,omitempty"`
		}{Content: result.Content, ToolCalls: result.ToolCalls, FinishReason: result.FinishReason}
		if b, err := json.Marshal(resp); err == nil {
			entry.Response = string(b)
		}
	}
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
		CostUSD:          s.pricing.Cost(model, u),
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
		writeError(w, &apiError{http.StatusBadRequest, `invalid group_by (want day, hour, provider, model, user or endpoint)`, "invalid_request_error", "invalid_group_by"})
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
