package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/backend"
)

const (
	// logRetention bounds the request log table to the newest entries.
	logRetention = 1000
	// maxErrorLength caps the stored failure message. Response bodies are not
	// kept at all: they dominated the database and nobody read them twice.
	maxErrorLength = 500
)

// RequestLog is one captured API request with its token usage. Bodies are not
// stored; only a short failure message, when the request failed.
type RequestLog struct {
	ID         string
	TS         string
	Endpoint   string
	Provider   string
	Model      string
	Account    string
	User       string
	Status     int
	DurationMS int64
	Stream     bool
	Error      string
	backend.Usage
}

const requestLogSchema = `CREATE TABLE IF NOT EXISTS request_logs (
	id TEXT PRIMARY KEY,
	ts TEXT NOT NULL,
	endpoint TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	account TEXT NOT NULL DEFAULT '',
	user TEXT NOT NULL DEFAULT '',
	status INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	stream INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	thinking_tokens INTEGER NOT NULL DEFAULT 0,
	cached_tokens INTEGER NOT NULL DEFAULT 0,
	total_tokens INTEGER NOT NULL DEFAULT 0
)`

// RecordLog queues one request log. Retention pruning runs in batches with the
// other queued writes instead of on every request.
func (s *UsageStore) RecordLog(e RequestLog) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(usageTimestampLayout)
	}
	return s.enqueue(ledgerOp{log: &e})
}

const insertRequestLogSQL = `INSERT INTO request_logs (id, ts, endpoint, provider, model, account, user, status, duration_ms, stream,
	error, prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertRequestLog(db execer, e *RequestLog) error {
	_, err := db.Exec(insertRequestLogSQL,
		e.ID, e.TS, e.Endpoint, e.Provider, e.Model, e.Account, e.User, e.Status, e.DurationMS, e.Stream,
		e.Error,
		e.InputTokens, e.OutputTokens, e.ThinkingTokens, e.CacheReadTokens, e.TotalTokens,
	)
	return err
}

const pruneRequestLogsSQL = `DELETE FROM request_logs WHERE rowid IN
	(SELECT rowid FROM request_logs ORDER BY ts DESC, rowid DESC LIMIT -1 OFFSET ?)`

const requestLogColumns = `id, ts, endpoint, provider, model, account, user, status, duration_ms, stream,
	error, prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens`

func scanRequestLog(row interface{ Scan(...any) error }) (RequestLog, error) {
	var e RequestLog
	err := row.Scan(&e.ID, &e.TS, &e.Endpoint, &e.Provider, &e.Model, &e.Account, &e.User,
		&e.Status, &e.DurationMS, &e.Stream, &e.Error,
		&e.InputTokens, &e.OutputTokens, &e.ThinkingTokens, &e.CacheReadTokens, &e.TotalTokens)
	return e, err
}

// QueryLogs returns the newest logs first.
func (s *UsageStore) QueryLogs(limit, offset int) ([]RequestLog, error) {
	if err := s.Flush(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		"SELECT "+requestLogColumns+" FROM request_logs ORDER BY ts DESC, rowid DESC LIMIT ? OFFSET ?",
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RequestLog{}
	for rows.Next() {
		e, err := scanRequestLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LogSummary is the list row the dashboard renders. Bodies are no longer
// stored, so it carries no size fields.
type LogSummary = RequestLog

// QueryLogSummaries returns the newest logs first.
func (s *UsageStore) QueryLogSummaries(limit, offset int) ([]LogSummary, error) {
	return s.QueryLogs(limit, offset)
}

// QueryLog returns one log by ID; (nil, nil) when it does not exist.
func (s *UsageStore) QueryLog(id string) (*RequestLog, error) {
	if err := s.Flush(); err != nil {
		return nil, err
	}
	e, err := scanRequestLog(s.db.QueryRow("SELECT "+requestLogColumns+" FROM request_logs WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// logEntry is the in-flight log record attached to a request context.
type logEntry struct {
	RequestLog
	started        time.Time
	statusOverride int
}

type logKey struct{}

// userKey carries the request used for caller attribution into contexts that
// only expose a context (the completions package).
type userKey struct{}

// withUser stores the request so usage attribution can read its headers.
func withUser(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, userKey{}, r)
}

func logFromContext(ctx context.Context) *logEntry {
	entry, _ := ctx.Value(logKey{}).(*logEntry)
	return entry
}

// logFailure records a backend failure on the in-flight request log. Streaming
// handlers call it after the 200 header was already sent, so the log and the
// live feed report the real upstream status instead of a fake success.
func logFailure(r *http.Request, status int, err error) {
	entry := logFromContext(r.Context())
	if entry == nil {
		return
	}
	entry.SetError(status, err.Error())
}

// SetRequestMeta records the model and streaming mode parsed by the handler.
func (e *logEntry) SetRequestMeta(model string, stream bool) {
	e.Model = model
	e.Stream = stream
}

// SetModel records which model and provider served the request.
func (e *logEntry) SetModel(model, provider string) {
	e.Model = model
	e.Provider = provider
}

// SetAccount records which provider account served the request.
func (e *logEntry) SetAccount(account string) {
	e.Account = account
}

// SetUsage stores the token accounting reported for the request. Input is kept
// net of the cached prefix, matching what the usage ledger stores, so the two
// views never disagree.
func (e *logEntry) SetUsage(u backend.Usage) {
	cached := min(u.CacheReadTokens, u.InputTokens)
	u.InputTokens -= cached
	u.CacheReadTokens = cached
	e.Usage = u
}

// SetResult is kept so the completions package can report a finished result;
// the body itself is not stored, only the failure message is.
func (e *logEntry) SetResult(content string, toolCalls []backend.ToolCall, finishReason string) {
}

// SetError marks the log entry failed with the upstream status and message.
func (e *logEntry) SetError(status int, message string) {
	e.statusOverride = status
	if e.Error != "" {
		return
	}
	e.Error = truncateError(message)
}

// truncateError keeps only the opening of a failure message, which is where the
// useful part lives.
func truncateError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxErrorLength {
		return message
	}
	return message[:maxErrorLength] + "…"
}

// logResponseWriter records the status code so the log reports what was sent.
// Bodies are not stored; a failed response keeps a short prefix in memory only
// long enough to pull its error message out.
type logResponseWriter struct {
	http.ResponseWriter
	status int
	tail   []byte
}

func (w *logResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *logResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && len(w.tail) < maxErrorLength {
		room := maxErrorLength - len(w.tail)
		if room > len(b) {
			room = len(b)
		}
		w.tail = append(w.tail, b[:room]...)
	}
	return w.ResponseWriter.Write(b)
}

func (w *logResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// errorMessage pulls the message out of an error response body. Providers and
// the gateway both answer with {"error":{"message":"…"}}, but a plain string is
// accepted too so nothing is lost when the shape differs.
func (w *logResponseWriter) errorMessage() string {
	if len(w.tail) == 0 {
		return ""
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.tail, &body); err == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return strings.TrimSpace(string(w.tail))
}

// logged captures the final status, the model reported by the handler, and the
// message of a failed response. Request and response bodies are never stored.
func (s *Server) logged(endpoint string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entry := &logEntry{
			RequestLog: RequestLog{ID: backend.UUID(), Endpoint: endpoint, User: usageUser(r)},
			started:    time.Now(),
		}
		lw := &logResponseWriter{ResponseWriter: w}
		next(lw, r.WithContext(withUser(context.WithValue(r.Context(), logKey{}, entry), r)))
		entry.Status = lw.status
		if entry.statusOverride != 0 {
			entry.Status = entry.statusOverride
		}
		if entry.Error == "" && entry.Status >= 400 {
			entry.Error = truncateError(lw.errorMessage())
		}
		if entry.Provider == "" && entry.Model != "" {
			entry.Provider = s.ownerOf(r.Context(), entry.Model)
		}
		entry.DurationMS = time.Since(entry.started).Milliseconds()
		entry.TS = entry.started.UTC().Format(usageTimestampLayout)
		if s.usage != nil {
			if err := s.usage.RecordLog(entry.RequestLog); err != nil {
				slog.Warn("request log failed", "endpoint", endpoint, "err", err)
			}
		}
		s.publishLog(entry.RequestLog)
	}
}

// logBroker fans finished request logs out to SSE subscribers.
type logBroker struct {
	mu   sync.Mutex
	subs map[chan RequestLog]struct{}
}

func newLogBroker() *logBroker {
	return &logBroker{subs: map[chan RequestLog]struct{}{}}
}

func (b *logBroker) publish(e RequestLog) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop instead of stalling the request path
		}
	}
}

func (b *logBroker) subscribe() chan RequestLog {
	ch := make(chan RequestLog, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *logBroker) unsubscribe(ch chan RequestLog) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (s *Server) publishLog(e RequestLog) {
	if s.logs != nil {
		s.logs.publish(e)
	}
}

// rawOrString embeds body JSON as-is, falling back to a JSON string.
func rawOrString(body string) any {
	if body != "" && json.Valid([]byte(body)) {
		return json.RawMessage(body)
	}
	return body
}

func requestLogMeta(e RequestLog) map[string]any {
	return map[string]any{
		"id":          e.ID,
		"timestamp":   e.TS,
		"endpoint":    e.Endpoint,
		"provider":    e.Provider,
		"model":       e.Model,
		"account":     e.Account,
		"user":        e.User,
		"status":      e.Status,
		"duration_ms": e.DurationMS,
		"stream":      e.Stream,
		"usage": map[string]any{
			"prompt_tokens":     e.InputTokens,
			"completion_tokens": e.OutputTokens,
			"thinking_tokens":   e.ThinkingTokens,
			"cached_tokens":     e.CacheReadTokens,
			"total_tokens":      e.TotalTokens,
		},
	}
}

func requestLogView(e RequestLog) map[string]any {
	return requestLogMeta(e)
}

func (s *Server) handleLogging(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		writeError(w, &apiError{http.StatusNotFound, "logging is disabled", "invalid_request_error", "logging_disabled"})
		return
	}
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, &apiError{http.StatusBadRequest, "invalid limit", "invalid_request_error", "invalid_limit"})
			return
		}
		limit = min(n, logRetention)
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, &apiError{http.StatusBadRequest, "invalid offset", "invalid_request_error", "invalid_offset"})
			return
		}
		offset = n
	}
	include := q.Get("include")
	if include != "" && include != "all" {
		writeError(w, &apiError{http.StatusBadRequest, `invalid include (want "all")`, "invalid_request_error", "invalid_include"})
		return
	}
	// Bodies are no longer stored, so `include` changes nothing; it is still
	// accepted so older clients keep working.
	logs, err := s.usage.QueryLogs(limit, offset)
	if err != nil {
		slog.Warn("log query failed", "err", err)
		writeError(w, &apiError{http.StatusInternalServerError, "log query failed", "server_error", "log_query_failed"})
		return
	}
	data := make([]map[string]any, 0, len(logs))
	for _, e := range logs {
		data = append(data, requestLogView(e))
	}
	writeJSON(w, map[string]any{
		"object":       "logging",
		"generated_at": time.Now().UTC(),
		"data":         data,
	})
}

// handleLogStream live-tails request logs as Server-Sent Events.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	s.streamLogs(w, r, nil)
}

func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, valid func() bool) {
	if s.usage == nil {
		writeError(w, &apiError{http.StatusNotFound, "logging is disabled", "invalid_request_error", "logging_disabled"})
		return
	}
	include := r.URL.Query().Get("include")
	if include != "" && include != "all" {
		writeError(w, &apiError{http.StatusBadRequest, `invalid include (want "all")`, "invalid_request_error", "invalid_include"})
		return
	}
	if s.logs == nil {
		writeError(w, &apiError{http.StatusInternalServerError, "log stream unavailable", "server_error", "stream_unavailable"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, &apiError{http.StatusInternalServerError, "streaming unsupported", "server_error", "streaming_unsupported"})
		return
	}
	ch := s.logs.subscribe()
	defer s.logs.unsubscribe(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\nevent: ready\ndata: {}\n\n")
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case e := <-ch:
			if valid != nil && !valid() {
				return
			}
			view := requestLogView(e)
			data, _ := json.Marshal(view)
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ping.C:
			if valid != nil && !valid() {
				return
			}
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		writeError(w, &apiError{http.StatusNotFound, "logging is disabled", "invalid_request_error", "logging_disabled"})
		return
	}
	e, err := s.usage.QueryLog(r.PathValue("id"))
	if err != nil {
		slog.Warn("log query failed", "err", err)
		writeError(w, &apiError{http.StatusInternalServerError, "log query failed", "server_error", "log_query_failed"})
		return
	}
	if e == nil {
		writeError(w, &apiError{http.StatusNotFound, "log not found", "invalid_request_error", "log_not_found"})
		return
	}
	writeJSON(w, requestLogView(*e))
}
