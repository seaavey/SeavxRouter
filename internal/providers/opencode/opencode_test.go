package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

const modelsFixture = `{"data":[` +
	`{"id":"free-one","providerID":"opencode","type":"chat","name":"Free One","enabled":true,"cost":[{"input":0,"output":0}]},` +
	`{"id":"paid","providerID":"opencode","type":"chat","name":"Paid","enabled":true,"cost":[{"input":1,"output":2}]},` +
	`{"id":"other","providerID":"another","type":"chat","name":"Other","enabled":true,"cost":[]},` +
	`{"id":"disabled","providerID":"opencode","type":"chat","name":"Disabled","enabled":false,"cost":[]}` +
	`]}`

type fakeServer struct {
	mu             sync.Mutex
	models         string
	createBody     map[string]any
	prompts        []string
	deletes        int
	generateStatus int
	generateBody   string
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass != "pass" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Authentication required"}`))
			return false
		}
		return true
	}
	mux.HandleFunc("/api/model", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		f.mu.Lock()
		body := f.models
		f.mu.Unlock()
		if body == "" {
			body = modelsFixture
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode session body: %v", err)
		}
		f.mu.Lock()
		f.createBody = body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"ses_test"}}`))
	})
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		f.mu.Lock()
		status, errBody := f.generateStatus, f.generateBody
		path := r.URL.Path
		f.mu.Unlock()
		switch {
		case path == "/api/session/ses_test/generate" && r.Method == http.MethodPost:
			var body struct {
				Prompt string `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode prompt: %v", err)
			}
			f.mu.Lock()
			f.prompts = append(f.prompts, body.Prompt)
			f.mu.Unlock()
			if status != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(errBody))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"text":"hello","tokens":{"input":11,"output":7,"reasoning":2,"cache":{"read":3,"write":4}},"cost":0}}`))
		case path == "/api/session/ses_test" && r.Method == http.MethodDelete:
			f.mu.Lock()
			f.deletes++
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func newFakeEngine(t *testing.T, fake *fakeServer) *Engine {
	t.Helper()
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	engine, err := NewEngine(Config{
		URL:      srv.URL,
		Username: "opencode",
		Password: "pass",
		HTTP:     srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func TestModelsKeepsOnlyFreeOpenCodeChatModels(t *testing.T) {
	engine := newFakeEngine(t, &fakeServer{})
	models, err := engine.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "free-one" || models[0].Name != "Free One" {
		t.Fatalf("models = %+v", models)
	}
}

func TestChatUsesTransientSession(t *testing.T) {
	fake := &fakeServer{}
	engine := newFakeEngine(t, fake)
	var deltas []string
	res, err := engine.Chat(context.Background(), &backend.ChatRequest{
		Messages: []backend.ChatMessage{
			{Role: "system", Content: "be nice"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "previous"},
			{Role: "user", Content: "again"},
		},
		Stream: true,
	}, func(d backend.ChatDelta) {
		deltas = append(deltas, d.Content)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello" || res.FinishReason != "stop" {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 7 || res.Usage.ThinkingTokens != 2 ||
		res.Usage.CacheReadTokens != 3 || res.Usage.TotalTokens != 20 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if len(deltas) != 1 || deltas[0] != "hello" {
		t.Fatalf("deltas = %v", deltas)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	model, _ := fake.createBody["model"].(map[string]any)
	if model["providerID"] != "opencode" || model["id"] != "big-pickle" {
		t.Fatalf("session model = %+v", fake.createBody["model"])
	}
	if len(fake.prompts) != 1 {
		t.Fatalf("prompts = %v", fake.prompts)
	}
	for _, want := range []string{"[System instructions]", "be nice", "User: hi", "Assistant: previous", "User: again"} {
		if !strings.Contains(fake.prompts[0], want) {
			t.Fatalf("prompt missing %q: %s", want, fake.prompts[0])
		}
	}
	if fake.deletes != 1 {
		t.Fatalf("session deletes = %d", fake.deletes)
	}
}

func TestChatCleansUpSessionOnQuotaError(t *testing.T) {
	fake := &fakeServer{
		generateStatus: http.StatusBadGateway,
		generateBody:   `{"_tag":"ProviderRequestError","providerID":"opencode","reason":"rate_limit","message":"Free usage limit reached","status":429}`,
	}
	engine := newFakeEngine(t, fake)
	_, err := engine.Chat(context.Background(), &backend.ChatRequest{
		Model:    "free-one",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !backend.IsQuotaError(err) {
		t.Fatalf("err = %v, want quota", err)
	}
	if backend.StatusCode(err) != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", backend.StatusCode(err))
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.deletes != 1 {
		t.Fatalf("session deletes = %d", fake.deletes)
	}
}

func TestLoginStoresLocalMarker(t *testing.T) {
	fake := &fakeServer{}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	t.Setenv("OPENCODE_SERVER_URL", srv.URL)
	t.Setenv("OPENCODE_SERVER_USERNAME", "opencode")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "pass")
	t.Setenv("OPENCODE_BIN", "/nonexistent/opencode")

	store := auth.AuthStore{Path: filepath.Join(t.TempDir(), "auth.jsonc")}
	var out strings.Builder
	if err := Login(context.Background(), store, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 free model(s) available via "+srv.URL) {
		t.Fatalf("output = %q", out.String())
	}
	auths, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 1 || auths[0].Provider != "opencode" || auths[0].AccessToken != "local" {
		t.Fatalf("auths = %+v", auths)
	}
}
