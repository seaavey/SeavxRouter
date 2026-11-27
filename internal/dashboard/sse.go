package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/httpapi"
)

// sseStatus reports the live log feed state to the render loop.
type sseStatus struct {
	connected bool
	err       error
}

// sseLog mirrors the JSON shape of requestLogSummaryView.
type sseLog struct {
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	Endpoint      string `json:"endpoint"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Account       string `json:"account"`
	User          string `json:"user"`
	Status        int    `json:"status"`
	DurationMS    int64  `json:"duration_ms"`
	Stream        bool   `json:"stream"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	Usage         struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		ThinkingTokens   int `json:"thinking_tokens"`
		CachedTokens     int `json:"cached_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (e sseLog) summary() httpapi.LogSummary {
	return httpapi.LogSummary{
		ID:         e.ID,
		TS:         e.Timestamp,
		Endpoint:   e.Endpoint,
		Provider:   e.Provider,
		Model:      e.Model,
		Account:    e.Account,
		User:       e.User,
		Status:     e.Status,
		DurationMS: e.DurationMS,
		Stream:     e.Stream,
		Usage: backend.Usage{
			InputTokens:     e.Usage.PromptTokens,
			OutputTokens:    e.Usage.CompletionTokens,
			ThinkingTokens:  e.Usage.ThinkingTokens,
			CacheReadTokens: e.Usage.CachedTokens,
			TotalTokens:     e.Usage.TotalTokens,
		},
	}
}

// streamLogs subscribes to GET /v1/logging/stream and calls onLog for every
// finished request. It returns when the stream ends or ctx is cancelled.
func streamLogs(ctx context.Context, baseURL, apiKey string, onConnect func(), onLog func(httpapi.LogSummary)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/logging/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if onConnect != nil {
		onConnect()
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var (
		event string
		data  strings.Builder
	)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if event == "log" && data.Len() > 0 {
				var e sseLog
				if json.Unmarshal([]byte(data.String()), &e) == nil && onLog != nil {
					onLog(e.summary())
				}
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// runSSE keeps the log stream connected until ctx is cancelled, reconnecting
// with capped exponential backoff.
func (d *Dashboard) runSSE(ctx context.Context, events chan<- httpapi.LogSummary, status chan<- sseStatus) {
	send := func(st sseStatus) {
		select {
		case status <- st:
		default:
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		send(sseStatus{})
		err := streamLogs(ctx, d.opts.Server, d.opts.APIKey, func() {
			backoff = time.Second
			send(sseStatus{connected: true})
		}, func(e httpapi.LogSummary) {
			select {
			case events <- e:
			default:
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err == nil || errors.Is(err, io.EOF) {
			err = errors.New("stream closed")
		}
		send(sseStatus{err: err})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
