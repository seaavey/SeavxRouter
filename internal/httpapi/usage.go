package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	ledgerQueueDepth = 128
	ledgerBatchMax   = 128
	ledgerFlushEvery = 75 * time.Millisecond
	ledgerPruneEvery = 10 * time.Second
)

var errUsageStoreClosed = errors.New("usage store closed")

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
	// OffsetMinutes is the reader's UTC offset, used to cut day and hour
	// buckets on their clock instead of UTC.
	OffsetMinutes int
}

// UsageStore persists request usage in SQLite. Writes are queued and committed
// in batches off the request path; reads flush the queue first so callers keep
// read-your-writes semantics.
type UsageStore struct {
	db *sql.DB

	ops       chan ledgerOp
	done      chan struct{}
	lifeMu    sync.RWMutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
	pending   atomic.Int64
	overflow  atomic.Int64
}

// ledgerOp is one queued write. ack marks a Flush barrier: the writer commits
// everything queued before it, then reports the transaction result.
type ledgerOp struct {
	usage       *UsageEntry
	log         *RequestLog
	compression *CompressionEntry
	ack         chan error
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
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
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(%d)&_pragma=journal_size_limit(%d)",
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
	if err := ensureColumn(db, "request_logs", "account", "ALTER TABLE request_logs ADD COLUMN account TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating request log table: %w", err)
	}
	if err := migrateRequestLogs(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("dropping stored request bodies: %w", err)
	}
	if err := netUsageCache(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("netting cached tokens from usage: %w", err)
	}
	if _, err := db.Exec(compressionSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating compression table: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS usage_ts ON usage(ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating usage index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS usage_user_ts ON usage(user, ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating usage user index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS request_logs_ts ON request_logs(ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating request log index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS compression_ts ON compression(ts)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating compression index: %w", err)
	}
	store := &UsageStore{
		db:   db,
		ops:  make(chan ledgerOp, ledgerQueueDepth),
		done: make(chan struct{}),
	}
	go store.writer()
	return store, nil
}

// Close stops accepting writes, drains the queue, then truncates the WAL back
// into the main database so the -wal file does not linger after shutdown.
func (s *UsageStore) Close() error {
	s.closeOnce.Do(func() {
		s.lifeMu.Lock()
		s.closed = true
		close(s.ops)
		s.lifeMu.Unlock()
		<-s.done
		var busy, log, checkpointed int
		_ = s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// ensureUsageCostColumn adds cost_usd to databases created before cost
// tracking existed.
func ensureUsageCostColumn(db *sql.DB) error {
	return ensureColumn(db, "usage", "cost_usd", "ALTER TABLE usage ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0")
}

// migrateRequestLogs drops the stored bodies from an older request_logs table
// and adds the error column. Bodies dominated the file, so the table is rebuilt
// once instead of keeping dead columns forever.
func migrateRequestLogs(db *sql.DB) error {
	columns, err := tableColumns(db, "request_logs")
	if err != nil {
		return err
	}
	_, hasRequest := columns["request"]
	_, hasResponse := columns["response"]
	if !hasRequest && !hasResponse {
		return ensureColumn(db, "request_logs", "error", "ALTER TABLE request_logs ADD COLUMN error TEXT NOT NULL DEFAULT ''")
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// SQLite resolves table names at parse time, so the old table is renamed
	// aside first and the surviving columns are copied into the fresh one.
	if _, err := tx.Exec(`ALTER TABLE request_logs RENAME TO request_logs_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(requestLogSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO request_logs (id, ts, endpoint, provider, model, account, user, status, duration_ms, stream,
		error, prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens)
		SELECT id, ts, endpoint, provider, model, account, user, status, duration_ms, stream,
		'', prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens FROM request_logs_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE request_logs_old`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// The freed pages stay in the file until the database is rebuilt, which is
	// the whole point of dropping the bodies: give the space back.
	if _, err := db.Exec(`VACUUM`); err != nil {
		// A busy database cannot vacuum; the space returns on the next open.
		slog.Warn("could not vacuum usage database after dropping request bodies", "err", err)
	}
	return nil
}

// netUsageCache rewrites rows written before input was stored net of the cache.
// Those rows count the cached prefix twice: once inside prompt_tokens and again
// in cached_tokens, which is how the dashboard ended up showing 81,869 in and
// 81,536 cached for one request. It runs once, guarded by user_version, because
// subtracting twice would underflow the input figure to zero.
func netUsageCache(db *sql.DB) error {
	const version = 1
	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current >= version {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"usage", "request_logs"} {
		if _, err := tx.Exec(`UPDATE ` + table + ` SET
			prompt_tokens = prompt_tokens - MIN(cached_tokens, prompt_tokens),
			cached_tokens = MIN(cached_tokens, prompt_tokens)
			WHERE cached_tokens > 0`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

// tableColumns lists a table's column names so a migration can test for them.
func tableColumns(db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]struct{}{}
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
			return nil, err
		}
		columns[name] = struct{}{}
	}
	return columns, rows.Err()
}

// ensureColumn adds column to table when the table predates it.
func ensureColumn(db *sql.DB, table, column, addSQL string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
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
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(addSQL)
	return err
}

// Record queues one usage row, stamping TS with the current UTC time when unset.
func (s *UsageStore) Record(e UsageEntry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(usageTimestampLayout)
	}
	return s.enqueue(ledgerOp{usage: &e})
}

// Flush waits until every write queued before the call is committed. Read paths
// call it so callers keep read-your-writes semantics.
func (s *UsageStore) Flush() error {
	if s.pending.Load() == 0 {
		return nil
	}
	ack := make(chan error, 1)
	s.lifeMu.RLock()
	if s.closed {
		s.lifeMu.RUnlock()
		return errUsageStoreClosed
	}
	s.ops <- ledgerOp{ack: ack}
	s.lifeMu.RUnlock()
	return <-ack
}

// enqueue appends one write to the queue. A full queue falls back to a
// synchronous write so no row is lost while the database catches up.
func (s *UsageStore) enqueue(op ledgerOp) error {
	s.pending.Add(1)
	s.lifeMu.RLock()
	if s.closed {
		s.lifeMu.RUnlock()
		s.pending.Add(-1)
		return errUsageStoreClosed
	}
	select {
	case s.ops <- op:
		s.lifeMu.RUnlock()
		return nil
	default:
		err := s.writeOne(op)
		s.lifeMu.RUnlock()
		s.pending.Add(-1)
		if n := s.overflow.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("usage queue full; wrote synchronously", "count", n, "err", err)
		}
		return err
	}
}

// writer commits queued writes in batches, flushing on size, timer, or a Flush
// barrier. It exits after the queue is closed and drained.
func (s *UsageStore) writer() {
	defer close(s.done)
	batch := make([]ledgerOp, 0, ledgerBatchMax)
	var acks []chan error
	lastPrune := time.Now()
	timer := time.NewTimer(ledgerFlushEvery)
	defer timer.Stop()

	commit := func() {
		if len(batch) == 0 && len(acks) == 0 {
			return
		}
		n := len(batch)
		err := s.commitBatch(batch, &lastPrune)
		s.pending.Add(-int64(n))
		for _, ack := range acks {
			ack <- err
		}
		batch = batch[:0]
		acks = acks[:0]
		if err != nil {
			slog.Error("usage batch write failed", "ops", n, "err", err)
		}
	}
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(ledgerFlushEvery)
	}

	for {
		select {
		case op, ok := <-s.ops:
			if !ok {
				commit()
				return
			}
			if op.ack != nil {
				acks = append(acks, op.ack)
				commit()
				resetTimer()
				continue
			}
			batch = append(batch, op)
			if len(batch) >= ledgerBatchMax {
				commit()
				resetTimer()
			}
		case <-timer.C:
			commit()
			timer.Reset(ledgerFlushEvery)
		}
	}
}

// commitBatch writes one batch in a single transaction to amortize the WAL
// commit cost. Retention pruning is best-effort and rate-limited.
func (s *UsageStore) commitBatch(batch []ledgerOp, lastPrune *time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	logs := 0
	for i := range batch {
		op := &batch[i]
		switch {
		case op.usage != nil:
			err = insertUsage(tx, op.usage)
		case op.log != nil:
			err = insertRequestLog(tx, op.log)
			logs++
		case op.compression != nil:
			err = insertCompression(tx, op.compression)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	if logs > 0 && time.Since(*lastPrune) >= ledgerPruneEvery {
		if _, err := tx.Exec(pruneRequestLogsSQL, logRetention); err != nil {
			slog.Warn("request log prune failed", "err", err)
		}
		*lastPrune = time.Now()
	}
	return tx.Commit()
}

// writeOne is the synchronous fallback used when the queue is full.
func (s *UsageStore) writeOne(op ledgerOp) error {
	switch {
	case op.usage != nil:
		return insertUsage(s.db, op.usage)
	case op.log != nil:
		return insertRequestLog(s.db, op.log)
	case op.compression != nil:
		return insertCompression(s.db, op.compression)
	}
	return nil
}

const insertUsageSQL = `INSERT INTO usage (ts, provider, model, endpoint, user,
	prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens, cost_usd)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertUsage(db execer, e *UsageEntry) error {
	_, err := db.Exec(insertUsageSQL,
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
// Timestamps are stored in UTC, so the caller's offset shifts them onto the
// reader's clock before the bucket is cut: a "day" is their day, and an hour
// block runs from their :00 to the next :00 rather than from a UTC boundary.
func usageGroupExpr(groupBy string, offsetMinutes int) (string, bool) {
	// offsetMinutes is a bounded integer taken from the query string, so writing
	// it into the expression is safe and keeps the GROUP BY free of parameters
	// that would reorder the filter arguments.
	local := fmt.Sprintf("datetime(substr(ts, 1, 19), '%+d minutes')", offsetMinutes)
	switch groupBy {
	case "", "day":
		return "strftime('%Y-%m-%d', " + local + ")", true
	case "hour":
		return "strftime('%Y-%m-%dT%H', " + local + ")", true
	case "minute":
		return "strftime('%Y-%m-%dT%H:%M', " + local + ")", true
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
	if err := s.Flush(); err != nil {
		return UsageTotals{}, nil, err
	}
	where, args := usageWhere(f)

	var totals UsageTotals
	row := s.db.QueryRow("SELECT "+usageSumColumns+" FROM usage"+where, args...)
	if err := scanUsageTotals(row, &totals); err != nil {
		return UsageTotals{}, nil, fmt.Errorf("summing usage: %w", err)
	}

	expr, ok := usageGroupExpr(f.GroupBy, f.OffsetMinutes)
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

// usageUser attributes a request to a user: a tenant key's billing identity,
// else explicit X-Seavx-User header, else a short hash of the bearer token,
// else "anonymous".
func usageUser(r *http.Request) string {
	if k := tenantKeyFromContext(r.Context()); k != nil {
		return tenantUser(k.Name)
	}
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

// SpendSince sums recorded cost and total tokens for one attributed user,
// which is how tenant limits are enforced.
func (s *UsageStore) SpendSince(ctx context.Context, user string, since time.Time) (costUSD float64, totalTokens int64, err error) {
	if err := s.Flush(); err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(cost_usd), 0), COALESCE(SUM(total_tokens), 0) FROM usage WHERE user = ? AND ts >= ?",
		user, since.UTC().Format(usageTimestampLayout),
	).Scan(&costUSD, &totalTokens)
	if err != nil {
		return 0, 0, fmt.Errorf("summing usage spend: %w", err)
	}
	return costUSD, totalTokens, nil
}

// RecordCompression queues one compression event, stamping TS with the current
// UTC time when unset.
func (s *UsageStore) RecordCompression(e CompressionEntry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(usageTimestampLayout)
	}
	return s.enqueue(ledgerOp{compression: &e})
}

const insertCompressionSQL = `INSERT INTO compression (ts, endpoint, tool_results, compressed, bytes_before, bytes_after)
 VALUES (?, ?, ?, ?, ?, ?)`

func insertCompression(db execer, e *CompressionEntry) error {
	_, err := db.Exec(insertCompressionSQL,
		e.TS, e.Endpoint, e.ToolResults, e.Compressed, e.BytesBefore, e.BytesAfter,
	)
	return err
}

// QueryCompressionTotals returns persisted compression totals since start
// (nil = all time).
func (s *UsageStore) QueryCompressionTotals(start *time.Time) (CompressionTotals, error) {
	if err := s.Flush(); err != nil {
		return CompressionTotals{}, err
	}
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
	}
	if s.usage == nil {
		return
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	// Providers report input tokens inclusive of the cached prefix. Store the
	// uncached part so the figure is what you actually paid full price for, and
	// cached_tokens carries the rest.
	cached := min(u.CacheReadTokens, u.InputTokens)
	e := UsageEntry{
		Provider:         s.ownerOf(r.Context(), model),
		Model:            model,
		Endpoint:         endpoint,
		User:             usageUser(r),
		PromptTokens:     u.InputTokens - cached,
		CompletionTokens: u.OutputTokens,
		ThinkingTokens:   u.ThinkingTokens,
		CachedTokens:     cached,
		TotalTokens:      total,
		CostUSD:          s.pricing.Load().Cost(model, u),
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
	// A UTC offset is minutes east of UTC, so UTC+7 is 420. Anything outside a
	// real timezone is rejected rather than silently shifting the chart.
	if raw := q.Get("offset"); raw != "" {
		minutes, err := strconv.Atoi(raw)
		if err != nil || minutes < -12*60 || minutes > 14*60 {
			writeError(w, &apiError{http.StatusBadRequest, "invalid offset: want minutes east of UTC between -720 and 840", "invalid_request_error", "invalid_offset"})
			return
		}
		f.OffsetMinutes = minutes
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
	if _, ok := usageGroupExpr(f.GroupBy, f.OffsetMinutes); !ok {
		writeError(w, &apiError{http.StatusBadRequest, `invalid group_by (want day, hour, minute, provider, model, user or endpoint)`, "invalid_request_error", "invalid_group_by"})
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
