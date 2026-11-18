package httpapi

import (
	"bytes"
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
	// maxLogBody caps captured response bodies. Request bodies are never read
	// or persisted: they dwarf everything else in the DB.
	maxLogBody = 10 << 20
)

// RequestLog is one captured API request with its response and token usage.
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
	Request    string
	Response   string
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
	request TEXT NOT NULL DEFAULT '',
	response TEXT NOT NULL DEFAULT '',
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
	request, response, prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertRequestLog(db execer, e *RequestLog) error {
	_, err := db.Exec(insertRequestLogSQL,
		e.ID, e.TS, e.Endpoint, e.Provider, e.Model, e.Account, e.User, e.Status, e.DurationMS, e.Stream,
		e.Request, e.Response,
		e.InputTokens, e.OutputTokens, e.ThinkingTokens, e.CacheReadTokens, e.TotalTokens,
	)
	return err
}

const pruneRequestLogsSQL = `DELETE FROM request_logs WHERE rowid IN
	(SELECT rowid FROM request_logs ORDER BY ts DESC, rowid DESC LIMIT -1 OFFSET ?)`

const requestLogColumns = `id, ts, endpoint, provider, model, account, user, status, duration_ms, stream,
	request, response, prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens`

func scanRequestLog(row interface{ Scan(...any) error }) (RequestLog, error) {
	var e RequestLog
	err := row.Scan(&e.ID, &e.TS, &e.Endpoint, &e.Provider, &e.Model, &e.Account, &e.User,
		&e.Status, &e.DurationMS, &e.Stream, &e.Request, &e.Response,
		&e.InputTokens, &e.OutputTokens, &e.ThinkingTokens, &e.CacheReadTokens, &e.TotalTokens)
	return e, err
}

// QueryLogs returns the newest logs first, including request and response bodies.
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

// LogSummary is a list row: request/response bodies are replaced by their sizes
// so a dozen screenshot-sized bodies do not dominate the response payload.
type LogSummary struct {
	RequestLog
	RequestBytes  int64
	ResponseBytes int64
}

const requestLogSummaryColumns = `id, ts, endpoint, provider, model, account, user, status, duration_ms, stream,
	length(CAST(request AS BLOB)), length(CAST(response AS BLOB)),
	prompt_tokens, completion_tokens, thinking_tokens, cached_tokens, total_tokens`

// QueryLogSummaries returns the newest logs first without body payloads.
func (s *UsageStore) QueryLogSummaries(limit, offset int) ([]LogSummary, error) {
	if err := s.Flush(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		"SELECT "+requestLogSummaryColumns+" FROM request_logs ORDER BY ts DESC, rowid DESC LIMIT ? OFFSET ?",
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LogSummary{}
	for rows.Next() {
		var e LogSummary
		err := rows.Scan(&e.ID, &e.TS, &e.Endpoint, &e.Provider, &e.Model, &e.Account, &e.User,
			&e.Status, &e.DurationMS, &e.Stream, &e.RequestBytes, &e.ResponseBytes,
			&e.InputTokens, &e.OutputTokens, &e.ThinkingTokens, &e.CacheReadTokens, &e.TotalTokens)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
	entry.statusOverride = status
	if entry.Response != "" {
		return
	}
	data, mErr := json.Marshal(map[string]any{
		"error": map[string]any{"message": err.Error(), "type": "backend_error"},
	})
	if mErr == nil {
		entry.Response = string(data)
	}
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

// SetUsage stores the token accounting reported for the request.
func (e *logEntry) SetUsage(u backend.Usage) {
	e.Usage = u
}

// SetResult stores the response body captured from the completed chat result.
func (e *logEntry) SetResult(content string, toolCalls []backend.ToolCall, finishReason string) {
	if e.Response != "" {
		return
	}
	resp := struct {
		Content      string             `json:"content,omitempty"`
		ToolCalls    []backend.ToolCall `json:"tool_calls,omitempty"`
		FinishReason string             `json:"finish_reason,omitempty"`
	}{Content: content, ToolCalls: toolCalls, FinishReason: finishReason}
	if b, err := json.Marshal(resp); err == nil {
		e.Response = string(b)
	}
}

// SetError marks the log entry failed with the upstream status and message.
func (e *logEntry) SetError(status int, message string) {
	e.statusOverride = status
	if e.Response != "" {
		return
	}
	data, err := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "backend_error"},
	})
	if err == nil {
		e.Response = string(data)
	}
}

// logResponseWriter records status and buffered body (non-stream only).
type logResponseWriter struct {
	http.ResponseWriter
	status  int
	body    bytes.Buffer
	capture bool
}

func (w *logResponseWriter) WriteHeader(status int) {
	w.status = status
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.capture = false
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *logResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.capture && w.body.Len() < maxLogBody {
		n := maxLogBody - w.body.Len()
		if n > len(b) {
			n = len(b)
		}
		w.body.Write(b[:n])
	}
	return w.ResponseWriter.Write(b)
}

func (w *logResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logged captures the final status and, for completion endpoints, the
// structured result recorded by the handler. Request bodies are never read
// here; handlers report the model and stream flag via SetRequestMeta.
func (s *Server) logged(endpoint string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entry := &logEntry{
			RequestLog: RequestLog{ID: backend.UUID(), Endpoint: endpoint, User: usageUser(r)},
			started:    time.Now(),
		}
		lw := &logResponseWriter{ResponseWriter: w, capture: true}
		next(lw, r.WithContext(withUser(context.WithValue(r.Context(), logKey{}, entry), r)))
		entry.Status = lw.status
		if entry.statusOverride != 0 {
			entry.Status = entry.statusOverride
		}
		if entry.Response == "" && lw.body.Len() > 0 {
			entry.Response = lw.body.String()
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
	v := requestLogMeta(e)
	v["request"] = rawOrString(e.Request)
	v["response"] = rawOrString(e.Response)
	return v
}

func requestLogSummaryView(e LogSummary) map[string]any {
	v := requestLogMeta(e.RequestLog)
	v["request_bytes"] = e.RequestBytes
	v["response_bytes"] = e.ResponseBytes
	return v
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
	data := []map[string]any{}
	if include == "all" {
		logs, err := s.usage.QueryLogs(limit, offset)
		if err != nil {
			slog.Warn("log query failed", "err", err)
			writeError(w, &apiError{http.StatusInternalServerError, "log query failed", "server_error", "log_query_failed"})
			return
		}
		for _, e := range logs {
			data = append(data, requestLogView(e))
		}
	} else {
		logs, err := s.usage.QueryLogSummaries(limit, offset)
		if err != nil {
			slog.Warn("log query failed", "err", err)
			writeError(w, &apiError{http.StatusInternalServerError, "log query failed", "server_error", "log_query_failed"})
			return
		}
		for _, e := range logs {
			data = append(data, requestLogSummaryView(e))
		}
	}
	writeJSON(w, map[string]any{
		"object":       "logging",
		"generated_at": time.Now().UTC(),
		"data":         data,
	})
}

// handleLogStream live-tails request logs as Server-Sent Events.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\nevent: ready\ndata: {}\n\n")
	flusher.Flush()

	ch := s.logs.subscribe()
	defer s.logs.unsubscribe(ch)

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case e := <-ch:
			var view map[string]any
			if include == "all" {
				view = requestLogView(e)
			} else {
				view = requestLogSummaryView(LogSummary{
					RequestLog:    e,
					RequestBytes:  int64(len(e.Request)),
					ResponseBytes: int64(len(e.Response)),
				})
			}
			data, _ := json.Marshal(view)
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ping.C:
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
