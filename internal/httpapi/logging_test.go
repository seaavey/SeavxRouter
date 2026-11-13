package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"seavxrouter/internal/backend"
)

func newLoggingServer(t *testing.T, stub *chatStub) (*Server, string) {
	t.Helper()
	store, err := OpenUsageStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("OpenUsageStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewServer(stub, "", store), "/v1/chat/completions"
}

func TestRequestLogging(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{
		Content:      "hi there",
		FinishReason: "stop",
		Usage:        backend.Usage{InputTokens: 3, OutputTokens: 2, ThinkingTokens: 1, CacheReadTokens: 4, TotalTokens: 5},
	}}
	s, path := newLoggingServer(t, stub)
	h := s.Handler()

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("chat code %d body %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/logging", nil))
	if w.Code != 200 {
		t.Fatalf("logging code %d body %s", w.Code, w.Body.String())
	}
	var list struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Object != "logging" || len(list.Data) != 1 {
		t.Fatalf("list = %s", w.Body.String())
	}
	raw := list.Data[0]
	if _, ok := raw["request"]; ok {
		t.Fatalf("list must omit request body by default: %v", raw)
	}
	if raw["id"] == "" || raw["endpoint"] != "chat" || raw["provider"] != "antigravity" || raw["model"] != "m" || raw["status"] != float64(200) {
		t.Fatalf("entry = %v", raw)
	}
	if raw["request_bytes"].(float64) != float64(len(body)) || raw["response_bytes"].(float64) == 0 {
		t.Fatalf("body sizes = %v / %v", raw["request_bytes"], raw["response_bytes"])
	}
	usage := raw["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(3) || usage["completion_tokens"] != float64(2) ||
		usage["thinking_tokens"] != float64(1) || usage["cached_tokens"] != float64(4) ||
		usage["total_tokens"] != float64(5) {
		t.Fatalf("usage = %v", usage)
	}
	id := raw["id"].(string)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/logging?include=all&limit=1", nil))
	if w.Code != 200 {
		t.Fatalf("include=all code %d body %s", w.Code, w.Body.String())
	}
	var fullList struct {
		Data []struct {
			Request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			} `json:"request"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &fullList); err != nil {
		t.Fatal(err)
	}
	if len(fullList.Data) != 1 || len(fullList.Data[0].Request.Messages) != 1 ||
		fullList.Data[0].Request.Messages[0].Content != "hello" {
		t.Fatalf("include=all missing request body: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/logging/"+id, nil))
	if w.Code != 200 {
		t.Fatalf("detail code %d body %s", w.Code, w.Body.String())
	}
	var detail struct {
		ID       string `json:"id"`
		Endpoint string `json:"endpoint"`
		Request  struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		} `json:"request"`
		Response struct {
			Content      string `json:"content"`
			FinishReason string `json:"finish_reason"`
		} `json:"response"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != id || detail.Endpoint != "chat" {
		t.Fatalf("detail = %s", w.Body.String())
	}
	if detail.Request.Model != "m" || len(detail.Request.Messages) != 1 || detail.Request.Messages[0].Content != "hello" {
		t.Fatalf("detail request = %+v", detail.Request)
	}
	if detail.Response.Content != "hi there" || detail.Response.FinishReason != "stop" {
		t.Fatalf("detail response = %+v", detail.Response)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/logging/nope", nil))
	if w.Code != 404 {
		t.Fatalf("unknown id code %d body %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/logging?include=bogus", nil))
	if w.Code != 400 {
		t.Fatalf("bad include code %d body %s", w.Code, w.Body.String())
	}
}

func TestRequestLoggingStream(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{
		Content:      "hi there",
		FinishReason: "stop",
		Usage:        backend.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}}
	s, path := newLoggingServer(t, stub)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/logging/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream code %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	readSSEEvent(t, sc, "ready")

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	cr, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, cr.Body)
	cr.Body.Close()
	if cr.StatusCode != 200 {
		t.Fatalf("chat code %d", cr.StatusCode)
	}

	data := readSSEEvent(t, sc, "log")
	var e struct {
		Endpoint     string `json:"endpoint"`
		Model        string `json:"model"`
		Status       int    `json:"status"`
		RequestBytes int    `json:"request_bytes"`
		Usage        struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		t.Fatal(err)
	}
	if e.Endpoint != "chat" || e.Model != "m" || e.Status != 200 || e.RequestBytes != len(body) {
		t.Fatalf("streamed log = %s", data)
	}
	if e.Usage.PromptTokens != 3 || e.Usage.TotalTokens != 5 {
		t.Fatalf("streamed usage = %s", data)
	}
}

// readSSEEvent scans until the data line of the next named event.
func readSSEEvent(t *testing.T, sc *bufio.Scanner, want string) string {
	t.Helper()
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
			continue
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok && event == want {
			return data
		}
	}
	t.Fatalf("stream ended before event %q: %v", want, sc.Err())
	return ""
}

func TestRequestLoggingCapturesErrors(t *testing.T) {
	s, path := newLoggingServer(t, &chatStub{result: &backend.ChatResult{Content: "x"}})
	h := s.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"model":"m","messages":[]}`)))
	if w.Code != 400 {
		t.Fatalf("chat code %d body %s", w.Code, w.Body.String())
	}

	logs, err := s.usage.QueryLogs(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Status != 400 {
		t.Fatalf("logs = %+v", logs)
	}
	if !strings.Contains(logs[0].Request, `"messages":[]`) || !strings.Contains(logs[0].Response, "messages must not be empty") {
		t.Fatalf("request/response not captured: %+v", logs[0])
	}
}

func TestRequestLoggingDisabled(t *testing.T) {
	s := NewServer(&chatStub{}, "", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/logging", nil))
	if w.Code != 404 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
}
