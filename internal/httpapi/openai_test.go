package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
	"seavxrouter/internal/multi"
)

type chatStub struct {
	gotReq     *backend.ChatRequest
	result     *backend.ChatResult
	deltas     []backend.ChatDelta
	hasNoModel string
}

func (c *chatStub) Models(context.Context) ([]backend.Model, error) {
	return []backend.Model{{ID: "m"}}, nil
}
func (c *chatStub) HasModel(_ context.Context, id string) bool {
	return id != c.hasNoModel
}
func (c *chatStub) Complete(context.Context, string, string) (*backend.Result, error) {
	return &backend.Result{Status: "SUCCESS"}, nil
}
func (c *chatStub) Stream(context.Context, string, string, func(string)) (*backend.Result, error) {
	return &backend.Result{Status: "SUCCESS"}, nil
}
func (c *chatStub) Chat(_ context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	c.gotReq = req
	if onDelta != nil {
		for _, d := range c.deltas {
			onDelta(d)
		}
	}
	return c.result, nil
}
func (c *chatStub) Default() string { return "m" }

func TestOpenAIToolCalls(t *testing.T) {
	t.Run("non-streaming", func(t *testing.T) {
		stub := &chatStub{result: &backend.ChatResult{
			ToolCalls:    []backend.ToolCall{{ID: "call_1", Type: "function", Name: "get_weather", Arguments: `{"city":"SF"}`}},
			FinishReason: "tool_calls",
			Usage:        backend.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 7, TotalTokens: 15},
		}}
		s := &Server{backend: stub}
		body := `{"model":"m","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"d","parameters":{"type":"object"}}}]}`
		w := httptest.NewRecorder()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		var resp struct {
			Choices []struct {
				Message struct {
					Role      string  `json:"role"`
					Content   *string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens        int `json:"prompt_tokens"`
				TotalTokens         int `json:"total_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Choices) != 1 {
			t.Fatalf("choices: %s", w.Body.String())
		}
		ch := resp.Choices[0]
		if ch.FinishReason != "tool_calls" {
			t.Fatalf("finish_reason = %q", ch.FinishReason)
		}
		if len(ch.Message.ToolCalls) != 1 {
			t.Fatalf("tool_calls: %s", w.Body.String())
		}
		if ch.Message.Content != nil {
			t.Fatalf("content should be null with tool calls, got %q", *ch.Message.Content)
		}
		tc := ch.Message.ToolCalls[0]
		if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "get_weather" || tc.Function.Arguments != `{"city":"SF"}` {
			t.Fatalf("tool call = %+v", tc)
		}
		if resp.Usage.PromptTokens != 10 || resp.Usage.TotalTokens != 15 || resp.Usage.PromptTokensDetails.CachedTokens != 7 {
			t.Fatalf("usage = %+v", resp.Usage)
		}
		if stub.gotReq == nil || len(stub.gotReq.Tools) != 1 || stub.gotReq.Tools[0].Name != "get_weather" {
			t.Fatalf("tools not parsed: %+v", stub.gotReq)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		tc := backend.ToolCall{ID: "call_1", Type: "function", Name: "f", Arguments: `{"x":1}`}
		stub := &chatStub{
			result: &backend.ChatResult{ToolCalls: []backend.ToolCall{tc}, FinishReason: "tool_calls"},
			deltas: []backend.ChatDelta{{ToolCalls: []backend.ToolCall{tc}}},
		}
		s := &Server{backend: stub}
		body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		w := httptest.NewRecorder()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		var toolChunkFound bool
		var finalFinish string
		for _, line := range strings.Split(w.Body.String(), "\n") {
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok || payload == "[DONE]" {
				continue
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
				continue
			}
			c := chunk.Choices[0]
			if len(c.Delta.ToolCalls) > 0 {
				toolChunkFound = true
				got := c.Delta.ToolCalls[0]
				if got.Index != 0 || got.ID != "call_1" || got.Type != "function" || got.Function.Name != "f" || got.Function.Arguments != `{"x":1}` {
					t.Fatalf("stream tool chunk = %+v", got)
				}
			}
			if c.FinishReason != nil {
				finalFinish = *c.FinishReason
			}
		}
		if !toolChunkFound {
			t.Fatalf("no tool_calls chunk in %s", w.Body.String())
		}
		if finalFinish != "tool_calls" {
			t.Fatalf("final finish_reason = %q", finalFinish)
		}
	})

	t.Run("multi-turn parsing", func(t *testing.T) {
		stub := &chatStub{result: &backend.ChatResult{Content: "done", FinishReason: "stop"}}
		s := &Server{backend: stub}
		body := `{"model":"m","messages":[` +
			`{"role":"user","content":"weather?"},` +
			`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},` +
			`{"role":"tool","tool_call_id":"call_1","name":"get_weather","content":"sunny"}` +
			`]}`
		w := httptest.NewRecorder()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		if stub.gotReq == nil || len(stub.gotReq.Messages) != 3 {
			t.Fatalf("messages not parsed: %+v", stub.gotReq)
		}
		asst := stub.gotReq.Messages[1]
		if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Name != "get_weather" || asst.ToolCalls[0].Arguments != `{"city":"SF"}` {
			t.Fatalf("assistant tool_calls = %+v", asst)
		}
		tool := stub.gotReq.Messages[2]
		if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Name != "get_weather" || tool.Content != "sunny" {
			t.Fatalf("tool message = %+v", tool)
		}
	})

	t.Run("tool name resolved from tool_call_id", func(t *testing.T) {
		stub := &chatStub{result: &backend.ChatResult{Content: "done", FinishReason: "stop"}}
		s := &Server{backend: stub}
		body := `{"model":"m","messages":[` +
			`{"role":"user","content":"weather?"},` +
			`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},` +
			`{"role":"tool","tool_call_id":"call_1","content":"sunny"}` +
			`]}`
		w := httptest.NewRecorder()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		if stub.gotReq == nil || len(stub.gotReq.Messages) != 3 {
			t.Fatalf("messages not parsed: %+v", stub.gotReq)
		}
		tool := stub.gotReq.Messages[2]
		if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Name != "get_weather" {
			t.Fatalf("tool name not resolved from tool_call_id: %+v", tool)
		}
	})
}

func TestBuildChatRequestMaxCompletionTokens(t *testing.T) {
	var req chatRequest
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":5,"max_completion_tokens":42}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	cr, err := buildChatRequest(req, "m")
	if err != nil {
		t.Fatal(err)
	}
	if cr.MaxTokens == nil || *cr.MaxTokens != 42 {
		t.Fatalf("max_completion_tokens should win over max_tokens: %+v", cr.MaxTokens)
	}
}

func TestStreamUsageOptIn(t *testing.T) {
	newServer := func() *Server {
		return &Server{backend: &chatStub{result: &backend.ChatResult{
			Content: "hi", FinishReason: "stop",
			Usage: backend.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		}}}
	}
	streamChunks := func(body string) []map[string]any {
		w := httptest.NewRecorder()
		s := newServer()
		s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("code %d body %s", w.Code, w.Body.String())
		}
		var chunks []map[string]any
		for _, line := range strings.Split(w.Body.String(), "\n") {
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok || payload == "[DONE]" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(payload), &m); err != nil {
				t.Fatalf("bad chunk %q: %v", payload, err)
			}
			chunks = append(chunks, m)
		}
		return chunks
	}

	plain := streamChunks(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	for _, c := range plain {
		if _, ok := c["usage"]; ok {
			t.Fatalf("usage must be omitted unless stream_options.include_usage is set: %v", c)
		}
	}

	withUsage := streamChunks(`{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	var usageChunk map[string]any
	for _, c := range withUsage {
		if _, ok := c["usage"]; ok {
			usageChunk = c
		}
	}
	if usageChunk == nil {
		t.Fatal("missing usage chunk with include_usage")
	}
	if choices, ok := usageChunk["choices"].([]any); !ok || len(choices) != 0 {
		t.Fatalf("usage chunk choices must be empty: %v", usageChunk["choices"])
	}
	usage := usageChunk["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(10) || usage["total_tokens"] != float64(12) {
		t.Fatalf("usage = %v", usage)
	}
}

func TestBuildChatRequestServiceTier(t *testing.T) {
	var req chatRequest
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"service_tier":"fast"}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	cr, err := buildChatRequest(req, "m")
	if err != nil {
		t.Fatal(err)
	}
	if cr.ServiceTier != "fast" {
		t.Fatalf("service_tier = %q", cr.ServiceTier)
	}
}

func TestResponsesServiceTier(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "ok", FinishReason: "stop"}}
	s := &Server{backend: stub}
	body := `{"model":"m","input":"hi","service_tier":"priority"}`
	w := httptest.NewRecorder()
	s.handleResponses(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if stub.gotReq == nil || stub.gotReq.ServiceTier != "priority" {
		t.Fatalf("service_tier not forwarded: %+v", stub.gotReq)
	}
}

func TestHandleModel(t *testing.T) {
	s := &Server{backend: &chatStub{hasNoModel: "missing"}}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models/m", nil)
	req.SetPathValue("id", "m")
	s.handleModel(w, req)
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	var model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.ID != "m" || model.Object != "model" || model.OwnedBy == "" {
		t.Fatalf("model = %+v", model)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/v1/models/missing", nil)
	req.SetPathValue("id", "missing")
	s.handleModel(w, req)
	if w.Code != 404 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
}

func TestOwnerOf(t *testing.T) {
	mb := &multi.MultiBackend{Antigravity: &chatStub{}, Codex: &chatStub{hasNoModel: "gemini-2.5-flash"}}
	s := &Server{backend: mb}
	if got := s.ownerOf(context.Background(), "codex/gpt-5.6-luna"); got != "codex" {
		t.Fatalf("prefixed codex owner = %q", got)
	}
	if got := s.ownerOf(context.Background(), "gemini-2.5-flash"); got != "antigravity" {
		t.Fatalf("antigravity owner = %q", got)
	}
}

const testPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func TestChatImageContent(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "red", FinishReason: "stop"}}
	s := &Server{backend: stub}
	body := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what color?"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + testPNGB64 + `"}},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/b.jpg"}}` +
		`]}]}`
	w := httptest.NewRecorder()
	s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if stub.gotReq == nil || len(stub.gotReq.Messages) != 1 {
		t.Fatalf("messages not parsed: %+v", stub.gotReq)
	}
	msg := stub.gotReq.Messages[0]
	if msg.Content != "what color?" {
		t.Fatalf("content = %q", msg.Content)
	}
	if len(msg.Images) != 2 {
		t.Fatalf("images = %+v", msg.Images)
	}
	if msg.Images[0].MIMEType != "image/png" || msg.Images[0].Data != testPNGB64 || msg.Images[0].URL != "" {
		t.Fatalf("inline image = %+v", msg.Images[0])
	}
	if msg.Images[1].URL != "https://example.com/b.jpg" {
		t.Fatalf("remote image = %+v", msg.Images[1])
	}
}

func TestChatRejectsUnsupportedContent(t *testing.T) {
	cases := []struct {
		name string
		part string
		want string
	}{
		{"audio part", `{"type":"input_audio","input_audio":{}}`, "only text and images are supported"},
		{"bad image url", `{"type":"image_url","image_url":{"url":"ftp://example.com/a.png"}}`, "data: URI or an http(s) URL"},
		{"non-image data url", `{"type":"image_url","image_url":{"url":"data:text/plain;base64,aGk="}}`, "unsupported image media type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{backend: &chatStub{result: &backend.ChatResult{Content: "x"}}}
			body := `{"model":"m","messages":[{"role":"user","content":[` + tc.part + `]}]}`
			w := httptest.NewRecorder()
			s.handleChat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if w.Code != 400 {
				t.Fatalf("code %d body %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("body %s missing %q", w.Body.String(), tc.want)
			}
		})
	}
}

func TestResponsesImageInput(t *testing.T) {
	stub := &chatStub{result: &backend.ChatResult{Content: "described", FinishReason: "stop"}}
	s := &Server{backend: stub}
	body := `{"model":"m","instructions":"be brief","input":[{"role":"user","content":[` +
		`{"type":"input_text","text":"describe"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,` + testPNGB64 + `"}` +
		`]}]}`
	w := httptest.NewRecorder()
	s.handleResponses(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Object string `json:"object"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "response" || len(resp.Output) != 1 || resp.Output[0].Content[0].Text != "described" {
		t.Fatalf("response = %s", w.Body.String())
	}
	if stub.gotReq == nil || len(stub.gotReq.Messages) != 2 {
		t.Fatalf("messages not parsed: %+v", stub.gotReq)
	}
	if stub.gotReq.Messages[0].Role != "system" || stub.gotReq.Messages[0].Content != "be brief" {
		t.Fatalf("instructions = %+v", stub.gotReq.Messages[0])
	}
	user := stub.gotReq.Messages[1]
	if user.Content != "describe" || len(user.Images) != 1 || user.Images[0].Data != testPNGB64 {
		t.Fatalf("user message = %+v", user)
	}
}

type providerQuotaStub struct{ chatStub }

func (providerQuotaStub) Quota() auth.ProviderQuota {
	return auth.ProviderQuota{Provider: "qoder", DefaultModel: "m"}
}

type quotaStub struct{ chatStub }

func (quotaStub) Quota() []auth.ProviderQuota {
	return []auth.ProviderQuota{{
		Provider:     "antigravity",
		DefaultModel: "m",
		Accounts:     []auth.AccountQuota{{Provider: "antigravity", Email: "a@example.com", Status: "ready"}},
	}}
}

func TestHandleQuota(t *testing.T) {
	s := &Server{backend: &quotaStub{}}
	w := httptest.NewRecorder()
	s.handleQuota(w, httptest.NewRequest("GET", "/v1/quota", nil))
	if w.Code != 200 {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			Provider string `json:"provider"`
			Accounts []struct {
				Email  string `json:"email"`
				Status string `json:"status"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "quota" || len(resp.Data) != 1 || resp.Data[0].Provider != "antigravity" {
		t.Fatalf("resp = %s", w.Body.String())
	}
	if len(resp.Data[0].Accounts) != 1 || resp.Data[0].Accounts[0].Email != "a@example.com" || resp.Data[0].Accounts[0].Status != "ready" {
		t.Fatalf("accounts = %+v", resp.Data[0].Accounts)
	}
}

func TestMultiBackendCodeBuddyRouting(t *testing.T) {
	agy := &chatStub{result: &backend.ChatResult{Content: "a"}}
	cb := &chatStub{result: &backend.ChatResult{Content: "c"}}
	mb := &multi.MultiBackend{Antigravity: agy, CodeBuddy: cb}
	ctx := context.Background()

	if _, err := mb.Chat(ctx, &backend.ChatRequest{Model: "codebuddy/glm5.1"}, nil); err != nil {
		t.Fatal(err)
	}
	if cb.gotReq == nil || cb.gotReq.Model != "glm-5.1" || agy.gotReq != nil {
		t.Fatalf("prefixed codebuddy not routed: cb=%+v agy=%+v", cb.gotReq, agy.gotReq)
	}
	// Bare IDs keep their historical routing to antigravity.
	if _, err := mb.Chat(ctx, &backend.ChatRequest{Model: "claude-4.0"}, nil); err != nil {
		t.Fatal(err)
	}
	if agy.gotReq == nil || agy.gotReq.Model != "claude-4.0" {
		t.Fatalf("bare id not routed to antigravity: %+v", agy.gotReq)
	}
	models, err := mb.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var prefixed bool
	for _, m := range models {
		if strings.HasPrefix(m.ID, "codebuddy/") {
			prefixed = true
		}
	}
	if !prefixed {
		t.Fatalf("codebuddy models not prefixed: %+v", models)
	}
	s := &Server{backend: mb}
	if got := s.ownerOf(ctx, "codebuddy/auto-chat"); got != "codebuddy" {
		t.Fatalf("owner = %q", got)
	}
	if !mb.HasModel(ctx, "codebuddy/auto-chat") {
		t.Fatal("HasModel should accept prefixed codebuddy model")
	}
}

func TestMultiBackendQoderRouting(t *testing.T) {
	q := &providerQuotaStub{}
	mb := &multi.MultiBackend{Qoder: q}
	ctx := context.Background()

	if _, err := mb.Chat(ctx, &backend.ChatRequest{Model: "qoder/Qwen3.7-Max"}, nil); err != nil {
		t.Fatal(err)
	}
	if q.gotReq == nil || q.gotReq.Model != "Qwen3.7-Max" {
		t.Fatalf("prefixed qoder not routed: %+v", q.gotReq)
	}
	// Bare IDs fall back to qoder when it is the only provider.
	if _, err := mb.Chat(ctx, &backend.ChatRequest{Model: "Qwen3.7-Max"}, nil); err != nil {
		t.Fatal(err)
	}
	if q.gotReq.Model != "Qwen3.7-Max" {
		t.Fatalf("bare id not routed to qoder: %+v", q.gotReq)
	}
	if mb.Default() != "m" {
		t.Fatalf("MultiBackend default = %q", mb.Default())
	}
	if qs := mb.Quota(); len(qs) != 1 || qs[0].Provider != "qoder" {
		t.Fatalf("quota = %+v", qs)
	}
	s := &Server{backend: mb}
	if owner := s.ownerOf(ctx, "qoder/Qwen3.7-Max"); owner != "qoder" {
		t.Fatalf("ownerOf = %q, want qoder", owner)
	}
	models, err := mb.Models(ctx)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	found := false
	for _, mo := range models {
		if mo.ID == "qoder/m" {
			found = true
		}
	}
	if !found {
		t.Fatalf("qoder models should be prefixed, got %+v", models)
	}
}
