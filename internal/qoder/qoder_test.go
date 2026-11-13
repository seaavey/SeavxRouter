package qoder

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

func TestQoderEncodeVectors(t *testing.T) {
	cases := []struct{ plain, encoded string }{
		{"test", "$$F^J_JH"},
		{"hello world", "YuHp$Hq&J(WPHFru"},
		{`{"key":"value","number":42}`, "uSKLN*ByjRKW#S%.D,BrBOQ.mYKUDxz*l*K("},
		{"a", "$p$#"},
		{"ab", "$SB#"},
		{"abc", "MSK#"},
		{"abcd", "$$KMD_#S"},
		{"中文测试", "GycAF)PZQUSQG*tQ"},
	}
	for _, tc := range cases {
		if got := qoderEncode([]byte(tc.plain)); got != tc.encoded {
			t.Errorf("qoderEncode(%q) = %q, want %q", tc.plain, got, tc.encoded)
		}
		dec, err := qoderDecode(tc.encoded)
		if err != nil {
			t.Errorf("qoderDecode(%q) failed: %v", tc.encoded, err)
			continue
		}
		if string(dec) != tc.plain {
			t.Errorf("qoderDecode(%q) = %q, want %q", tc.encoded, dec, tc.plain)
		}
	}
	if _, err := qoderDecode("not*valid$"); err == nil {
		t.Fatalf("qoderDecode should reject characters outside the alphabet")
	}
}

func TestQoderRegionDefault(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "global"},
		{"global", "global"},
		{"intl", "global"},
		{"international", "global"},
		{"Global", "global"},
		{"cn", "cn"},
		{"CN", "cn"},
	}
	for _, tc := range cases {
		if got := qoderRegionFor(tc.in).Name; got != tc.want {
			t.Errorf("qoderRegionFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQoderSign(t *testing.T) {
	t.Setenv("QODER_SIGNATURE_SECRET", "")
	date := "Mon, 22 Jul 2026 05:30:00 GMT"
	if got := qoderSign(date); got != "549a179af939a97f816ed77605db041c" {
		t.Fatalf("qoderSign(%q) = %q", date, got)
	}
	t.Setenv("QODER_SIGNATURE_SECRET", "custom-secret")
	if got := qoderSign(date); got == "549a179af939a97f816ed77605db041c" {
		t.Fatalf("qoderSign should honor QODER_SIGNATURE_SECRET")
	}
}

func TestQoderMachineIdentityStable(t *testing.T) {
	id1, tok1, typ1 := qoderMachineIdentity("pt-abc")
	id2, tok2, typ2 := qoderMachineIdentity("pt-abc")
	if id1 != id2 || tok1 != tok2 || typ1 != typ2 {
		t.Fatalf("machine identity must be deterministic")
	}
	if other, _, _ := qoderMachineIdentity("pt-def"); other == id1 {
		t.Fatalf("different PATs should derive different machine ids")
	}
	if len(id1) != 36 || id1[8] != '-' || id1[13] != '-' || id1[18] != '-' || id1[23] != '-' {
		t.Fatalf("machine id %q is not UUID-shaped", id1)
	}
	if len(tok1) != 32 {
		t.Fatalf("machine token %q should be 32 hex chars", tok1)
	}
	if typ1 != id1[:18] {
		t.Fatalf("machine type %q should be the id prefix", typ1)
	}
}

func TestQoderAESRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte(`{"name":"tester","user_type":"personal_pro"}`)
	enc, err := qoderAESEncrypt(plain, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(enc)%aes.BlockSize != 0 {
		t.Fatalf("ciphertext not block-aligned")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	dec := make([]byte, len(enc))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(dec, enc)
	pad := int(dec[len(dec)-1])
	if pad <= 0 || pad > aes.BlockSize {
		t.Fatalf("bad PKCS7 padding byte %d", pad)
	}
	if string(dec[:len(dec)-pad]) != string(plain) {
		t.Fatalf("round-trip mismatch: %q", dec[:len(dec)-pad])
	}
}

func TestQoderSessionAndBearer(t *testing.T) {
	sess, err := qoderNewSession(qoderIdentity{
		Name: "Tester", Aid: "u1", UID: "u1", UserType: "personal_pro",
		SecurityOauth: "so", RefreshToken: "rt",
	}, "machine-id", "machine-token", "machine-type")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	cosyKey, err := base64.StdEncoding.DecodeString(sess.CosyKey)
	if err != nil || len(cosyKey) != 128 {
		t.Fatalf("cosy key should be a base64 1024-bit RSA block, got %d bytes (err=%v)", len(cosyKey), err)
	}
	info, err := base64.StdEncoding.DecodeString(sess.Info)
	if err != nil || len(info) == 0 || len(info)%aes.BlockSize != 0 {
		t.Fatalf("info should be base64 AES-CBC ciphertext, got %d bytes (err=%v)", len(info), err)
	}
	if sess.UID != "u1" || sess.UserType != "personal_pro" || sess.MachineType != "machine-type" {
		t.Fatalf("session fields not carried over: %+v", sess)
	}

	bearer := sess.bearer("1721600000", `{"hello":1}`, "/api/v2/model/list")
	prefix, ok := strings.CutPrefix(bearer, "Bearer COSY.")
	if !ok {
		t.Fatalf("bearer %q missing COSY prefix", bearer)
	}
	payloadB64, sig, ok := strings.Cut(prefix, ".")
	if !ok {
		t.Fatalf("bearer %q missing signature", bearer)
	}
	raw, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		t.Fatalf("payload not base64: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["cosyVersion"] != qoderClientVersion || payload["version"] != "v1" || payload["info"] != sess.Info {
		t.Fatalf("unexpected payload: %v", payload)
	}
	if id, _ := payload["requestId"].(string); id == "" {
		t.Fatalf("payload missing requestId")
	}
	want := qoderMD5(payloadB64 + "\n" + sess.CosyKey + "\n1721600000\n" + `{"hello":1}` + "\n/api/v2/model/list")
	if sig != want {
		t.Fatalf("signature %q, want %q", sig, want)
	}
}

func TestQoderSigPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1", "/api/v2/model/list"},
		{"https://api1.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=x", "/api/v2/service/pro/sse/agent_chat_generation"},
		{"https://x/other/path", "/other/path"},
	}
	for _, tc := range cases {
		if got := qoderSigPath(tc.in); got != tc.want {
			t.Errorf("qoderSigPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQoderRejectsImages(t *testing.T) {
	e := &QoderEngine{}
	_, err := e.Chat(context.Background(), &backend.ChatRequest{Messages: []backend.ChatMessage{{Role: "user", Images: []backend.ImagePart{{MIMEType: "image/png", Data: "aGVsbG8="}}}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support image content") {
		t.Fatalf("err = %v, want image rejection", err)
	}
}

func TestQoderConvertMessage(t *testing.T) {
	tools := []backend.ToolCall{{ID: "call_1", Type: "function", Name: "lookup", Arguments: `{"q":"x"}`}}

	m := qoderConvertMessage(backend.ChatMessage{Role: "developer", Content: "be nice"}, false, false)
	if m == nil || m.Role != "system" {
		t.Fatalf("developer should map to system, got %+v", m)
	}

	m = qoderConvertMessage(backend.ChatMessage{Role: "assistant", Content: "calling", ToolCalls: tools}, false, false)
	if m == nil || m.Role != "assistant" || !strings.Contains(m.Content, "Tool calls:") || !strings.Contains(m.Content, "lookup") {
		t.Fatalf("tools-off assistant calls should be rendered as text, got %+v", m)
	}

	m = qoderConvertMessage(backend.ChatMessage{Role: "assistant", Content: "calling", ToolCalls: tools}, true, false)
	if m == nil || m.Role != "assistant" || len(m.ToolCalls) != 0 || !strings.Contains(m.Content, "Previously planned but unexecuted tool calls: lookup.") {
		t.Fatalf("unresolved calls should collapse to a summary, got %+v", m)
	}

	m = qoderConvertMessage(backend.ChatMessage{Role: "assistant", Content: "calling", ToolCalls: tools}, true, true)
	if m == nil || len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "call_1" || m.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("structured tool calls expected, got %+v", m)
	}

	m = qoderConvertMessage(backend.ChatMessage{Role: "tool", Name: "lookup", ToolCallID: "call_1", Content: "42"}, false, false)
	if m == nil || m.Role != "user" || !strings.Contains(m.Contents[0].Text, "Tool result (lookup) [call_1]:") {
		t.Fatalf("tools-off tool result should become user text, got %+v", m)
	}

	m = qoderConvertMessage(backend.ChatMessage{Role: "tool", Name: "lookup", ToolCallID: "call_1", Content: "42"}, true, false)
	if m == nil || m.Role != "tool" || m.Name != "lookup" || m.ToolCallID != "call_1" {
		t.Fatalf("tools-on tool result should stay structured, got %+v", m)
	}

	if m := qoderConvertMessage(backend.ChatMessage{Role: "user", Content: "  "}, false, false); m != nil {
		t.Fatalf("empty user message should be dropped, got %+v", m)
	}

	msgs := qoderBuildMessages(nil, "please help", false)
	if len(msgs) != 1 || msgs[0].Role != "user" || len(msgs[0].Contents) != 1 || msgs[0].Contents[0].Text != "please help" {
		t.Fatalf("fallback prompt message expected, got %+v", msgs)
	}
}

func TestQoderCatalogExtraction(t *testing.T) {
	raw := map[string]any{
		"chat": []any{
			map[string]any{"key": "m1", "display_name": "backend.Model One", "max_output_tokens": float64(8000), "thinking_config": map[string]any{"enabled": true}},
			map[string]any{"key": "auto", "display_name": "Auto"},
			map[string]any{"key": "off", "display_name": "Off", "enable": false},
			map[string]any{"key": "m2", "display_name": "backend.Model Two", "is_reasoning": false},
			map[string]any{"key": "m1dup", "display_name": "backend.Model One"},
		},
		"app": []any{
			map[string]any{"key": "m3", "display_name": "backend.Model Three"},
		},
	}
	cat := qoderExtractCatalog(raw)
	if cat == nil {
		t.Fatalf("catalog should extract")
	}
	if len(cat.keys) != 3 {
		t.Fatalf("expected 3 models after dedupe/filter, got %v", cat.keys)
	}
	if cat.keys["backend.Model One"] != "m1" || cat.keys["backend.Model Two"] != "m2" || cat.keys["backend.Model Three"] != "m3" {
		t.Fatalf("unexpected keys: %v", cat.keys)
	}
	if cat.maxOutput("m1") != 8000 || !cat.reasoning("m1") {
		t.Fatalf("m1 caps wrong: %+v", cat.caps["m1"])
	}
	if cat.reasoning("m2") {
		t.Fatalf("m2 should be non-reasoning")
	}
	if cat.defaultName != "backend.Model One" {
		t.Fatalf("default should be the sorted-first name, got %q", cat.defaultName)
	}

	fallback := qoderFallbackCatalog()
	if fallback.defaultName != "Qwen3.7-Max" {
		t.Fatalf("fallback default = %q, want Qwen3.7-Max", fallback.defaultName)
	}
	if len(fallback.keys) != 13 {
		t.Fatalf("fallback should list 13 models, got %d", len(fallback.keys))
	}
	if got := qoderNameForKey(map[string]string{"Flash One": "k1", "Other": "k2"}, "none"); got != "Flash One" {
		t.Fatalf("name preference should favor flash, got %q", got)
	}
}

func TestQoderParseStreamLine(t *testing.T) {
	frame := func(obj map[string]any) string {
		b, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("marshal frame: %v", err)
		}
		return "data: " + string(b)
	}

	ev := qoderParseStreamLine(frame(map[string]any{"body": `{"choices":[{"delta":{"content":"hi"}}]}`}))
	if ev.delta == nil || ev.delta.content != "hi" {
		t.Fatalf("content frame: %+v", ev)
	}

	ev = qoderParseStreamLine(frame(map[string]any{"body": `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`}))
	if ev.delta == nil || len(ev.delta.toolCalls) != 1 || ev.delta.toolCalls[0].name != "f" || ev.delta.toolCalls[0].index == nil || *ev.delta.toolCalls[0].index != 0 {
		t.Fatalf("tool frame: %+v", ev.delta)
	}

	ev = qoderParseStreamLine(frame(map[string]any{"body": `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"completion_tokens_details":{"reasoning_tokens":1},"prompt_tokens_details":{"cached_tokens":3}}}`}))
	if ev.delta == nil || ev.delta.usage == nil || ev.delta.usage.prompt != 5 || ev.delta.usage.reasoning != 1 || ev.delta.usage.cached != 3 {
		t.Fatalf("usage frame: %+v", ev.delta)
	}

	if ev = qoderParseStreamLine("data: [DONE]"); !ev.done {
		t.Fatalf("bare [DONE] should terminate")
	}
	if ev = qoderParseStreamLine(frame(map[string]any{"body": "[DONE]"})); !ev.done {
		t.Fatalf("wrapped [DONE] should terminate")
	}
	if ev = qoderParseStreamLine("event: finish"); !ev.done {
		t.Fatalf("event: finish should terminate")
	}
	if ev = qoderParseStreamLine("event: error"); ev.err == nil {
		t.Fatalf("event: error should surface an error")
	}

	ev = qoderParseStreamLine(frame(map[string]any{"statusCodeValue": float64(401), "body": `{"code":105,"message":"login expired"}`}))
	if ev.auth == nil || ev.auth.status != 401 {
		t.Fatalf("auth frame: %+v", ev)
	}

	ev = qoderParseStreamLine(frame(map[string]any{"statusCodeValue": float64(401), "body": `{"code":"10605","message":"{\"retryAfterSeconds\":3}"}`}))
	if ev.busy == nil || ev.busy.retryAfter != 3*time.Second {
		t.Fatalf("busy frame: %+v", ev.busy)
	}

	ev = qoderParseStreamLine(frame(map[string]any{"body": `{"code":105,"message":"expired"}`}))
	if ev.auth == nil {
		t.Fatalf("code 105 frame should be an auth error: %+v", ev)
	}

	ev = qoderParseStreamLine(frame(map[string]any{"statusCodeValue": float64(500), "body": "boom"}))
	if ev.err == nil {
		t.Fatalf("generic statusCodeValue error should surface")
	}

	ev = qoderParseStreamLine(frame(map[string]any{"success": false, "message": "quota exhausted"}))
	if ev.err == nil || !strings.Contains(ev.err.Error(), "quota exhausted") {
		t.Fatalf("success:false frame: %+v", ev.err)
	}

	if ev = qoderParseStreamLine("garbage"); ev.delta != nil || ev.err != nil || ev.done {
		t.Fatalf("garbage line should be ignored: %+v", ev)
	}
}

func TestQoderToolAccumulator(t *testing.T) {
	idx0, idx1 := 0, 1
	acc := &qoderToolAccumulator{}
	acc.append([]qoderToolCallDelta{
		{index: &idx0, id: "call_a", typ: "function", name: "first", arguments: `{"a":`},
	})
	acc.append([]qoderToolCallDelta{
		{index: &idx0, arguments: `1}`},
		{index: &idx1, id: "call_b", name: "second", arguments: `{}`},
	})
	calls := acc.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 accumulated calls, got %+v", calls)
	}
	if calls[0].ID != "call_a" || calls[0].Name != "first" || calls[0].Arguments != `{"a":1}` || calls[0].Type != "function" {
		t.Fatalf("call 0 merge wrong: %+v", calls[0])
	}
	if calls[1].ID != "call_b" || calls[1].Name != "second" || calls[1].Arguments != "{}" {
		t.Fatalf("call 1 wrong: %+v", calls[1])
	}

	acc2 := &qoderToolAccumulator{}
	acc2.append([]qoderToolCallDelta{{name: "solo"}})
	calls2 := acc2.snapshot()
	if len(calls2) != 1 || calls2[0].ID == "" || calls2[0].Name != "solo" ||
		calls2[0].Arguments != "{}" || calls2[0].Type != "function" {
		t.Fatalf("snapshot should fill defaults, got %+v", calls2)
	}
}

func TestQoderMapUsage(t *testing.T) {
	u := qoderMapUsage(&qoderUsage{prompt: 5, completion: 2, reasoning: 1, cached: 3})
	if u.InputTokens != 5 || u.OutputTokens != 2 || u.ThinkingTokens != 1 || u.CacheReadTokens != 3 || u.TotalTokens != 7 {
		t.Fatalf("usage mapping wrong: %+v", u)
	}
	u = qoderMapUsage(&qoderUsage{prompt: 5, completion: 2, total: 9})
	if u.TotalTokens != 9 {
		t.Fatalf("explicit total should win: %+v", u)
	}
}

// --- E2E chat over a mock gateway ---

type qoderMock struct {
	mu          sync.Mutex
	badPAT      string
	statusTier  string
	catalog     any
	catalogFail bool
	chatFn      func(n int, w http.ResponseWriter)

	jobTokens     int
	badTokens     int
	goodTokens    int
	refreshCalls  int
	needRefresh   bool
	chatCalls     int
	lastBody      []byte
	lastAuth      string
	lastCosyKey   string
	lastCosyDate  string
	lastModelKey  string
	lastToolCount int
}

func (m *qoderMock) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/algo/api/v3/user/jobToken":
			raw, _ := io.ReadAll(r.Body)
			dec, err := qoderDecode(string(raw))
			if err != nil {
				t.Errorf("jobToken body not encoded: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var outer struct {
				Payload       string `json:"payload"`
				EncodeVersion string `json:"encodeVersion"`
			}
			if err := json.Unmarshal(dec, &outer); err != nil || outer.EncodeVersion != "1" || outer.Payload == "" {
				t.Errorf("jobToken envelope malformed: %s", dec)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var inner struct {
				PersonalToken string `json:"personalToken"`
				RefreshToken  string `json:"refreshToken"`
				NeedRefresh   bool   `json:"needRefresh"`
			}
			_ = json.Unmarshal([]byte(outer.Payload), &inner)
			m.mu.Lock()
			m.jobTokens++
			if inner.PersonalToken == m.badPAT {
				m.badTokens++
				m.mu.Unlock()
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"code":401,"message":"bad token"}`)
				return
			}
			m.goodTokens++
			if inner.NeedRefresh {
				m.refreshCalls++
				m.needRefresh = true
			}
			m.mu.Unlock()
			fmt.Fprintf(w, `{"id":"u1","name":"Tester","expireTime":%d,"securityOauthToken":"so-1","refreshToken":"rt-1","userType":"personal_pro"}`,
				time.Now().Add(24*time.Hour).UnixMilli())
		case r.Method == http.MethodPost && r.URL.Path == "/algo/api/v3/user/status":
			fmt.Fprintf(w, `{"userType":%q}`, m.statusTier)
		case r.Method == http.MethodGet && r.URL.Path == "/algo/api/v2/model/list":
			if m.catalogFail || m.catalog == nil {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"code":401,"message":"nope"}`)
				return
			}
			b, _ := json.Marshal(m.catalog)
			w.Write(b)
		case r.Method == http.MethodPost && r.URL.Path == "/algo/api/v2/service/pro/sse/agent_chat_generation":
			raw, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.chatCalls++
			n := m.chatCalls
			m.lastBody = raw
			m.lastAuth = r.Header.Get("authorization")
			m.lastCosyKey = r.Header.Get("cosy-key")
			m.lastCosyDate = r.Header.Get("cosy-date")
			m.lastModelKey = r.Header.Get("x-model-key")
			fn := m.chatFn
			m.mu.Unlock()
			w.Header().Set("content-type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if fn != nil {
				fn(n, w)
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func qoderTestEngine(t *testing.T, mock *qoderMock, auths ...*auth.Auth) *QoderEngine {
	t.Helper()
	srv := httptest.NewServer(mock.handler(t))
	t.Cleanup(srv.Close)
	e, err := NewQoderEngine(auth.AuthStore{}, auths)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	e.HTTP = srv.Client()
	e.AuthBase = srv.URL
	e.ChatBase = srv.URL
	return e
}

func qoderSSEFrame(t *testing.T, obj any) string {
	t.Helper()
	inner, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal frame body: %v", err)
	}
	outer, err := json.Marshal(map[string]string{"body": string(inner)})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return "data: " + string(outer) + "\n"
}

// qoderSSERawFrame emits a top-level gateway frame (statusCodeValue errors are
// not nested inside another "body" envelope).
func qoderSSERawFrame(t *testing.T, obj any) string {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal raw frame: %v", err)
	}
	return "data: " + string(b) + "\n"
}

func qoderChatText(w io.Writer, t *testing.T, text string) {
	fmt.Fprint(w, qoderSSEFrame(t, map[string]any{"choices": []any{
		map[string]any{"delta": map[string]any{"content": text}},
	}}))
}

func qoderChatDone(t *testing.T, w io.Writer) {
	fmt.Fprint(w, qoderSSEFrame(t, map[string]any{"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7,
			"completion_tokens_details": map[string]any{"reasoning_tokens": 1},
			"prompt_tokens_details":     map[string]any{"cached_tokens": 3},
		}}))
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func qoderMockCatalog() map[string]any {
	return map[string]any{"chat": []any{
		map[string]any{"key": "qmodel_latest", "display_name": "Qwen-Unit", "max_output_tokens": float64(4096)},
		map[string]any{"key": "qfmodel", "display_name": "Qwen-Unit-Flash", "max_output_tokens": float64(2048)},
	}}
}

func TestQoderChatEndToEnd(t *testing.T) {
	mock := &qoderMock{
		statusTier: "personal_pro",
		catalog:    qoderMockCatalog(),
		chatFn: func(_ int, w http.ResponseWriter) {
			qoderChatText(w, t, "hel")
			qoderChatText(w, t, "lo")
			qoderChatDone(t, w)
		},
	}
	e := qoderTestEngine(t, mock, &auth.Auth{Provider: "qoder", AccessToken: "pt-good-123", Region: "cn"})

	var deltas []string
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:       "Qwen-Unit",
		Messages:    []backend.ChatMessage{{Role: "user", Content: "hi there"}},
		Temperature: ptr(0.3),
		MaxTokens:   ptr(123),
		Tools: []backend.ToolDefinition{{
			Name: "get_weather", Description: "weather lookup",
			Parameters: map[string]any{"type": "object"},
		}},
	}, func(d backend.ChatDelta) {
		if d.Content != "" {
			deltas = append(deltas, d.Content)
		}
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "hello" || res.FinishReason != "stop" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Usage.InputTokens != 5 || res.Usage.OutputTokens != 2 || res.Usage.ThinkingTokens != 1 ||
		res.Usage.CacheReadTokens != 3 || res.Usage.TotalTokens != 7 {
		t.Fatalf("usage not mapped: %+v", res.Usage)
	}
	if strings.Join(deltas, "") != "hello" {
		t.Fatalf("streamed deltas = %v", deltas)
	}

	if mock.lastModelKey != "qmodel_latest" {
		t.Fatalf("x-model-key = %q", mock.lastModelKey)
	}
	auth := mock.lastAuth
	prefix, ok := strings.CutPrefix(auth, "Bearer COSY.")
	if !ok {
		t.Fatalf("authorization header %q", auth)
	}
	payloadB64, sig, ok := strings.Cut(prefix, ".")
	if !ok {
		t.Fatalf("authorization missing signature: %q", auth)
	}
	payloadRaw, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		t.Fatalf("payload b64: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if payload["cosyVersion"] != qoderClientVersion || payload["version"] != "v1" || payload["info"] == "" {
		t.Fatalf("payload fields: %v", payload)
	}
	wantSig := qoderMD5(payloadB64 + "\n" + mock.lastCosyKey + "\n" + mock.lastCosyDate + "\n" +
		string(mock.lastBody) + "\n/api/v2/service/pro/sse/agent_chat_generation")
	if sig != wantSig {
		t.Fatalf("request signature %q, want %q", sig, wantSig)
	}

	dec, err := qoderDecode(string(mock.lastBody))
	if err != nil {
		t.Fatalf("chat body decode: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(dec, &body); err != nil {
		t.Fatalf("chat body json: %v", err)
	}
	if body["request_id"] != body["chat_record_id"] {
		t.Fatalf("request/chat record ids should match: %v", body)
	}
	if body["stream"] != true || body["chat_task"] != "FREE_INPUT" || body["session_type"] != "qodercli" {
		t.Fatalf("chat envelope fields: %v", body)
	}
	if body["aliyun_user_type"] != "personal_pro" || body["agent_id"] != "agent_common" || body["task_id"] != "common" {
		t.Fatalf("user/agent fields: %v", body)
	}
	cc, _ := body["chat_context"].(map[string]any)
	if cc == nil {
		t.Fatalf("missing chat_context")
	}
	if text, _ := cc["text"].(map[string]any); text["text"] != "hi there" {
		t.Fatalf("chat_context.text = %v", cc["text"])
	}
	extra, _ := cc["extra"].(map[string]any)
	if mc, _ := extra["modelConfig"].(map[string]any); mc["key"] != "qmodel_latest" {
		t.Fatalf("chat_context modelConfig = %v", extra)
	}
	if mc, _ := body["model_config"].(map[string]any); mc["key"] != "qmodel_latest" || mc["source"] != "system" || mc["is_reasoning"] != true {
		t.Fatalf("model_config = %v", body["model_config"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", body["messages"])
	}
	msg0, _ := msgs[0].(map[string]any)
	if msg0["role"] != "user" {
		t.Fatalf("message role = %v", msg0["role"])
	}
	contents, _ := msg0["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("user message contents = %v", msg0["contents"])
	}
	if part, _ := contents[0].(map[string]any); part["text"] != "hi there" || part["type"] != "text" {
		t.Fatalf("content part = %v", contents[0])
	}
	params, _ := body["parameters"].(map[string]any)
	if params["max_tokens"] != float64(123) || params["temperature"] != 0.3 {
		t.Fatalf("parameters = %v", params)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	if fn, _ := tools[0].(map[string]any)["function"].(map[string]any); fn["name"] != "get_weather" {
		t.Fatalf("tool function = %v", tools[0])
	}
	biz, _ := body["business"].(map[string]any)
	if biz["name"] != "hi there" || biz["id"] == "" {
		t.Fatalf("business = %v", biz)
	}
}

func TestQoderChatDefaultMaxTokensAndToolCalls(t *testing.T) {
	mock := &qoderMock{
		statusTier: "personal_pro",
		catalog:    qoderMockCatalog(),
		chatFn: func(_ int, w http.ResponseWriter) {
			fmt.Fprint(w, qoderSSEFrame(t, map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "id": "call_a", "type": "function",
					"function": map[string]any{"name": "get_weather", "arguments": `{"ci`},
				}}},
			}}}))
			fmt.Fprint(w, qoderSSEFrame(t, map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "function": map[string]any{"arguments": `ty":"Paris"}`},
				}}},
				"finish_reason": "tool_calls",
			}}}))
			fmt.Fprint(w, "data: [DONE]\n\n")
		},
	}
	e := qoderTestEngine(t, mock, &auth.Auth{Provider: "qoder", AccessToken: "pt-good-123", Region: "cn"})

	var finalCalls []backend.ToolCall
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "Qwen-Unit",
		Messages: []backend.ChatMessage{{Role: "user", Content: "weather?"}},
	}, func(d backend.ChatDelta) {
		if len(d.ToolCalls) > 0 {
			finalCalls = d.ToolCalls
		}
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.FinishReason != "tool_calls" || len(res.ToolCalls) != 1 {
		t.Fatalf("result: %+v", res)
	}
	call := res.ToolCalls[0]
	if call.ID != "call_a" || call.Type != "function" || call.Name != "get_weather" || call.Arguments != `{"city":"Paris"}` {
		t.Fatalf("merged tool call: %+v", call)
	}
	if len(finalCalls) != 1 || finalCalls[0].Arguments != `{"city":"Paris"}` {
		t.Fatalf("onDelta should receive the complete call, got %+v", finalCalls)
	}

	dec, err := qoderDecode(string(mock.lastBody))
	if err != nil {
		t.Fatalf("body decode: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(dec, &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	if params, _ := body["parameters"].(map[string]any); params["max_tokens"] != float64(4096) {
		t.Fatalf("catalog default max_tokens = %v", body["parameters"])
	}
	if _, has := body["tools"]; has {
		t.Fatalf("tools should be omitted when the client sent none")
	}
}

func TestQoderChatRotatesAccounts(t *testing.T) {
	mock := &qoderMock{
		badPAT:     "pt-bad",
		statusTier: "personal_standard",
		catalog:    qoderMockCatalog(),
		chatFn: func(_ int, w http.ResponseWriter) {
			qoderChatText(w, t, "from-good")
			qoderChatDone(t, w)
		},
	}
	e := qoderTestEngine(t, mock,
		&auth.Auth{Provider: "qoder", AccessToken: "pt-bad", Region: "cn"},
		&auth.Auth{Provider: "qoder", AccessToken: "pt-good", Region: "cn"},
	)
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "Qwen-Unit",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatalf("chat should rotate to the second account: %v", err)
	}
	if res.Content != "from-good" {
		t.Fatalf("content = %q", res.Content)
	}
	mock.mu.Lock()
	bad, good := mock.badTokens, mock.goodTokens
	mock.mu.Unlock()
	if bad < 2 {
		t.Fatalf("bad account should be attempted then retried once, saw %d jobToken calls", bad)
	}
	if good < 1 {
		t.Fatalf("good account never used")
	}
}

func TestQoderChatRefreshesOnAuthFrame(t *testing.T) {
	mock := &qoderMock{
		statusTier: "personal_pro",
		catalog:    qoderMockCatalog(),
		chatFn: func(n int, w http.ResponseWriter) {
			if n == 1 {
				fmt.Fprint(w, qoderSSERawFrame(t, map[string]any{
					"statusCodeValue": float64(401),
					"body":            `{"code":105,"message":"login expired"}`,
				}))
				return
			}
			qoderChatText(w, t, "recovered")
			qoderChatDone(t, w)
		},
	}
	e := qoderTestEngine(t, mock, &auth.Auth{Provider: "qoder", AccessToken: "pt-good", Region: "cn"})
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "Qwen-Unit",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatalf("chat should refresh and retry: %v", err)
	}
	if res.Content != "recovered" {
		t.Fatalf("content = %q", res.Content)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.refreshCalls != 1 || !mock.needRefresh {
		t.Fatalf("expected one needRefresh jobToken call, got refreshCalls=%d needRefresh=%v", mock.refreshCalls, mock.needRefresh)
	}
	if mock.chatCalls != 2 {
		t.Fatalf("expected 2 chat attempts, got %d", mock.chatCalls)
	}
}

func TestQoderChatBusyBackoff(t *testing.T) {
	old := qoderBusyBackoff
	qoderBusyBackoff = 5 * time.Millisecond
	t.Cleanup(func() { qoderBusyBackoff = old })

	mock := &qoderMock{
		statusTier: "personal_pro",
		catalog:    qoderMockCatalog(),
		chatFn: func(n int, w http.ResponseWriter) {
			if n == 1 {
				fmt.Fprint(w, qoderSSERawFrame(t, map[string]any{
					"statusCodeValue": float64(401),
					"body":            `{"code":"10605","message":"system busy"}`,
				}))
				return
			}
			qoderChatText(w, t, "after-busy")
			qoderChatDone(t, w)
		},
	}
	e := qoderTestEngine(t, mock, &auth.Auth{Provider: "qoder", AccessToken: "pt-good", Region: "cn"})
	res, err := e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "Qwen-Unit",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatalf("chat should retry after busy: %v", err)
	}
	if res.Content != "after-busy" {
		t.Fatalf("content = %q", res.Content)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.chatCalls != 2 {
		t.Fatalf("expected 2 chat attempts, got %d", mock.chatCalls)
	}
}

func TestQoderFallbackCatalogOnListFailure(t *testing.T) {
	mock := &qoderMock{statusTier: "personal_standard", catalogFail: true}
	e := qoderTestEngine(t, mock, &auth.Auth{Provider: "qoder", AccessToken: "pt-good", Region: "cn"})

	models, err := e.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 13 {
		t.Fatalf("fallback should expose 13 models, got %d", len(models))
	}
	if e.Default() != "Qwen3.7-Max" {
		t.Fatalf("default = %q", e.Default())
	}
	if !e.HasModel(context.Background(), "Qwen3.7-Max") {
		t.Fatalf("fallback model should be supported")
	}
	if e.HasModel(context.Background(), "definitely-not-a-model") {
		t.Fatalf("unknown model should not be supported")
	}
}

func TestQoderChatUnsupportedModel(t *testing.T) {
	e, err := NewQoderEngine(auth.AuthStore{}, []*auth.Auth{{Provider: "qoder", AccessToken: "pt-x"}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	e.catalog = qoderFallbackCatalog()
	e.catalogAt = time.Now()
	_, err = e.Chat(context.Background(), &backend.ChatRequest{
		Model:    "mystery-model",
		Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported qoder model") {
		t.Fatalf("expected unsupported model error, got %v", err)
	}
}

func TestQoderQuota(t *testing.T) {
	e, err := NewQoderEngine(auth.AuthStore{}, []*auth.Auth{{
		Provider: "qoder", AccessToken: "pt-x", Email: "tester", Region: "global",
	}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	e.catalog = qoderFallbackCatalog()
	e.catalogAt = time.Now()

	q := e.Quota()
	if q.Provider != "qoder" || len(q.Accounts) != 1 {
		t.Fatalf("quota = %+v", q)
	}
	if q.DefaultModel != "Qwen3.7-Max" {
		t.Fatalf("quota default model = %q", q.DefaultModel)
	}
}

func TestQoderCheck(t *testing.T) {
	mock := &qoderMock{statusTier: "personal_pro", catalog: qoderMockCatalog()}
	e := qoderTestEngine(t, mock, &auth.Auth{Provider: "qoder", AccessToken: "pt-good", Region: "cn"})

	checks := e.Check(context.Background())
	if len(checks) != 1 || checks[0].Err != nil {
		t.Fatalf("checks = %+v", checks)
	}
	models := checks[0].Models
	if len(models) != 2 || models[0].ID != "Qwen-Unit" || models[1].ID != "Qwen-Unit-Flash" {
		t.Fatalf("models = %+v", models)
	}
}

func TestQoderCheckReportsFailure(t *testing.T) {
	mock := &qoderMock{statusTier: "personal_pro", catalogFail: true}
	e := qoderTestEngine(t, mock, &auth.Auth{Provider: "qoder", AccessToken: "pt-good", Region: "cn"})

	checks := e.Check(context.Background())
	if len(checks) != 1 || checks[0].Err == nil {
		t.Fatalf("Check must report upstream failures instead of falling back to the built-in catalog: %+v", checks)
	}
}
