package kiro

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

func TestModelID(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"auto":                       "auto",
		"auto-kiro":                  "auto",
		"kiro-auto":                  "auto",
		"claude-sonnet-4":            "claude-sonnet-4",
		"claude-sonnet-4-6":          "claude-sonnet-4.6",
		"claude-sonnet-4-6[1m]":      "claude-sonnet-4.6",
		"claude-haiku-4-5-20251001":  "claude-haiku-4.5",
		"claude-opus-4-6-thinking":   "claude-opus-4.6",
		"claude-4.5-opus-high":       "claude-opus-4.5",
		"claude-3-7-sonnet":          "claude-3.7-sonnet",
		"claude-3-5-sonnet-20241022": "claude-3.5-sonnet",
		"gpt-5":                      "gpt-5",
	}
	for in, want := range cases {
		if got := ModelID(in); got != want {
			t.Errorf("ModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildKiroPayload(t *testing.T) {
	tools := []backend.ToolDefinition{{
		Name:        "get_weather",
		Description: "look up weather",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
	}}
	req := &backend.ChatRequest{
		Messages: []backend.ChatMessage{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []backend.ToolCall{{ID: "call_1", Type: "function", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
			{Role: "tool", ToolCallID: "call_1", Name: "get_weather", Content: "sunny"},
		},
		Tools: tools,
	}
	data, err := buildKiroPayload("claude-sonnet-4.6", req, "arn:aws:codewhisperer:us-east-1:1:profile/p")
	if err != nil {
		t.Fatalf("buildKiroPayload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload["profileArn"] != "arn:aws:codewhisperer:us-east-1:1:profile/p" {
		t.Fatalf("profileArn = %v", payload["profileArn"])
	}
	cs := payload["conversationState"].(map[string]any)
	if cs["chatTriggerType"] != "MANUAL" {
		t.Fatalf("chatTriggerType = %v", cs["chatTriggerType"])
	}
	history := cs["history"].([]any)
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2: %v", len(history), history)
	}
	first := history[0].(map[string]any)["userInputMessage"].(map[string]any)
	if first["content"] != "be brief\n\nweather?" {
		t.Fatalf("first user content = %q", first["content"])
	}
	second := history[1].(map[string]any)["assistantResponseMessage"].(map[string]any)
	uses := second["toolUses"].([]any)
	if len(uses) != 1 {
		t.Fatalf("toolUses = %v", uses)
	}
	use := uses[0].(map[string]any)
	if use["toolUseId"] != "call_1" || use["name"] != "get_weather" {
		t.Fatalf("toolUse = %v", use)
	}
	if use["input"].(map[string]any)["city"] != "Paris" {
		t.Fatalf("toolUse input = %v", use["input"])
	}
	current := cs["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	if current["modelId"] != "claude-sonnet-4.6" || current["origin"] != "AI_EDITOR" {
		t.Fatalf("current message = %v", current)
	}
	ctx := current["userInputMessageContext"].(map[string]any)
	results := ctx["toolResults"].([]any)
	if len(results) != 1 {
		t.Fatalf("toolResults = %v", results)
	}
	result := results[0].(map[string]any)
	if result["toolUseId"] != "call_1" || result["status"] != "success" {
		t.Fatalf("toolResult = %v", result)
	}
	if result["content"].([]any)[0].(map[string]any)["text"] != "sunny" {
		t.Fatalf("toolResult content = %v", result["content"])
	}
	specs := ctx["tools"].([]any)
	spec := specs[0].(map[string]any)["toolSpecification"].(map[string]any)
	if spec["name"] != "get_weather" || spec["description"] != "look up weather" {
		t.Fatalf("toolSpecification = %v", spec)
	}
}

func TestBuildKiroPayloadOmitsEmptyProfileARN(t *testing.T) {
	data, err := buildKiroPayload("auto", &backend.ChatRequest{Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}}}, "")
	if err != nil {
		t.Fatalf("buildKiroPayload: %v", err)
	}
	var payload map[string]any
	json.Unmarshal(data, &payload)
	if _, ok := payload["profileArn"]; ok {
		t.Fatalf("profileArn must be omitted for Builder ID accounts: %v", payload)
	}
}

func TestBuildKiroPayloadImages(t *testing.T) {
	req := &backend.ChatRequest{Messages: []backend.ChatMessage{{Role: "user", Content: "look", Images: []backend.ImagePart{{MIMEType: "image/jpeg", Data: "aGk="}}}}}
	data, err := buildKiroPayload("auto", req, "")
	if err != nil {
		t.Fatalf("buildKiroPayload: %v", err)
	}
	var payload map[string]any
	json.Unmarshal(data, &payload)
	msg := payload["conversationState"].(map[string]any)["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	img := msg["images"].([]any)[0].(map[string]any)
	if img["format"] != "jpeg" || img["source"].(map[string]any)["bytes"] != "aGk=" {
		t.Fatalf("image = %v", img)
	}

	urlReq := &backend.ChatRequest{Messages: []backend.ChatMessage{{Role: "user", Content: "look", Images: []backend.ImagePart{{URL: "https://example.com/x.png"}}}}}
	if _, err := buildKiroPayload("auto", urlReq, ""); err == nil {
		t.Fatal("remote image URLs should be rejected")
	}
	if _, err := buildKiroPayload("auto", &backend.ChatRequest{}, ""); err == nil {
		t.Fatal("empty conversation should error")
	}
}

func TestKiroStreamParser(t *testing.T) {
	stream := "\x00\x00\x00\x10prelude-garbage" +
		`{"content":"Hi"}` +
		`{"content":"Hi"}` +
		`{"content":" there"}` +
		`{"followupPrompt":"chat about the weather"}` +
		`{"name":"get_weather","toolUseId":"tu_1","input":"{\"city\":"}` +
		`{"input":"\"Paris\"}"}` +
		`{"stop":true}` +
		`{"name":"broken","toolUseId":"tu_2","input":"not json"}` +
		`{"stop":true}` +
		`{"name":"get_weather","toolUseId":"tu_1","input":"{\"city\":\"Paris\"}","stop":true}` +
		`{"usage":1.5}` +
		`{"contextUsagePercentage":10}`

	for _, chunk := range []int{1, 2, 3, 7, 1024} {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			parser := &kiroStreamParser{}
			var content strings.Builder
			var toolDeltas int
			emit := func(d backend.ChatDelta) {
				content.WriteString(d.Content)
				toolDeltas += len(d.ToolCalls)
			}
			for i := 0; i < len(stream); i += chunk {
				end := i + chunk
				if end > len(stream) {
					end = len(stream)
				}
				parser.feed([]byte(stream[i:end]), emit)
			}
			calls := parser.finish(emit)
			if got := content.String(); got != "Hi there" {
				t.Fatalf("content = %q, want %q", got, "Hi there")
			}
			if len(calls) != 2 {
				t.Fatalf("calls = %d, want 2: %v", len(calls), calls)
			}
			if calls[0].ID != "tu_1" || calls[0].Name != "get_weather" || calls[0].Arguments != `{"city":"Paris"}` {
				t.Fatalf("call[0] = %+v", calls[0])
			}
			if calls[1].Name != "broken" || calls[1].Arguments != "{}" {
				t.Fatalf("call[1] = %+v", calls[1])
			}
			if toolDeltas != 2 {
				t.Fatalf("tool deltas = %d, want 2", toolDeltas)
			}
			if parser.contextPct != 10 {
				t.Fatalf("contextPct = %v", parser.contextPct)
			}
		})
	}
}

func TestKiroChatStreaming(t *testing.T) {
	stream := `{"content":"Hi"}` +
		`{"content":" there"}` +
		`{"name":"get_weather","toolUseId":"tu_1","input":"{\"city\":"}` +
		`{"input":"\"Paris\"}"}` +
		`{"stop":true}` +
		`{"contextUsagePercentage":10}`

	var sawRequest atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generateAssistantResponse" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-amz-target") != kiroTarget {
			t.Errorf("x-amz-target = %q", r.Header.Get("x-amz-target"))
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("payload: %v", err)
		}
		sawRequest.Store(true)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		fmt.Fprint(w, stream)
	}))
	defer srv.Close()

	engine := &Engine{
		HTTP:    srv.Client(),
		BaseURL: srv.URL,
		auths: []*auth.Auth{{
			Provider:     "kiro",
			AccessToken:  "tok",
			RefreshToken: "r1",
			Expiry:       time.Now().Add(time.Hour),
			ProfileARN:   "arn:aws:codewhisperer:us-east-1:1:profile/p",
		}},
	}
	maxTokens := 512
	req := &backend.ChatRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: &maxTokens,
		Messages:  []backend.ChatMessage{{Role: "user", Content: "weather?"}},
	}
	var deltas []backend.ChatDelta
	res, err := engine.Chat(context.Background(), req, func(d backend.ChatDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !sawRequest.Load() {
		t.Fatal("upstream never called")
	}
	if res.Content != "Hi there" {
		t.Fatalf("content = %q", res.Content)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Arguments != `{"city":"Paris"}` {
		t.Fatalf("toolCalls = %+v", res.ToolCalls)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", res.FinishReason)
	}
	if res.Usage.OutputTokens != 2 || res.Usage.InputTokens != 19998 || res.Usage.TotalTokens != 20000 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if len(deltas) != 3 {
		t.Fatalf("deltas = %d, want 3: %+v", len(deltas), deltas)
	}
}

func TestKiroChatRefreshesOnForbidden(t *testing.T) {
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			refreshes.Add(1)
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["grantType"] != "refresh_token" || body["clientId"] != "cid" {
				t.Errorf("refresh body = %v", body)
			}
			fmt.Fprint(w, `{"accessToken":"new","refreshToken":"r2","expiresIn":3600}`)
		case "/generateAssistantResponse":
			if r.Header.Get("Authorization") != "Bearer new" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"message":"token expired"}`)
				return
			}
			fmt.Fprint(w, `{"content":"fresh"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	acct := &auth.Auth{
		Provider:     "kiro",
		AccessToken:  "old",
		RefreshToken: "r1",
		Expiry:       time.Now().Add(time.Hour),
		Region:       "us-east-1",
		ClientID:     "cid",
		ClientSecret: "secret",
	}
	engine := &Engine{HTTP: srv.Client(), BaseURL: srv.URL, OIDCBase: srv.URL, auths: []*auth.Auth{acct}}
	res, err := engine.Chat(context.Background(), &backend.ChatRequest{Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Content != "fresh" {
		t.Fatalf("content = %q", res.Content)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	if acct.AccessToken != "new" || acct.RefreshToken != "r2" {
		t.Fatalf("tokens = %q / %q", acct.AccessToken, acct.RefreshToken)
	}
}

func TestKiroChatRotatesOnQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer tok1":
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"You have reached the limit."}`)
		case "Bearer tok2":
			fmt.Fprint(w, `{"content":"ok"}`)
		default:
			t.Errorf("unexpected token %q", r.Header.Get("Authorization"))
		}
	}))
	defer srv.Close()

	a1 := &auth.Auth{Provider: "kiro", AccessToken: "tok1", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}
	a2 := &auth.Auth{Provider: "kiro", AccessToken: "tok2", RefreshToken: "r2", Expiry: time.Now().Add(time.Hour)}
	engine := &Engine{HTTP: srv.Client(), BaseURL: srv.URL, auths: []*auth.Auth{a1, a2}}
	res, err := engine.Chat(context.Background(), &backend.ChatRequest{Model: "auto", Messages: []backend.ChatMessage{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q", res.Content)
	}
	q := engine.Quota()
	if q.Provider != "kiro" || len(q.Accounts) != 2 {
		t.Fatalf("quota = %+v", q)
	}
	if q.Accounts[0].Status != "limited" || q.Accounts[1].Status != "ready" {
		t.Fatalf("account statuses = %q / %q", q.Accounts[0].Status, q.Accounts[1].Status)
	}
}

func TestKiroLoginDeviceFlow(t *testing.T) {
	t.Setenv("SEAVX_NO_BROWSER", "1")
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/register":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if len(body["scopes"].([]any)) != 5 {
				t.Errorf("scopes = %v", body["scopes"])
			}
			fmt.Fprint(w, `{"clientId":"cid","clientSecret":"secret"}`)
		case "/device_authorization":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["startUrl"] != kiroStartURL {
				t.Errorf("startUrl = %q", body["startUrl"])
			}
			fmt.Fprint(w, `{"deviceCode":"dc","userCode":"ABCD-EFGH","verificationUri":"https://device.sso.example","expiresIn":600,"interval":1}`)
		case "/token":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			fmt.Fprintf(w, `{"accessToken":%q,"refreshToken":"refresh-1","idToken":%q,"expiresIn":3600}`, testJWT(`{"email":"dev@example.com","sub":"sub-1"}`), testJWT(`{"email":"dev@example.com","sub":"sub-1"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := auth.AuthStore{Path: t.TempDir() + "/auth.jsonc"}
	engine := &Engine{Store: store, HTTP: srv.Client(), OIDCBase: srv.URL, Region: "eu-west-1", PollInterval: time.Millisecond}
	if err := engine.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	auths, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("stored %d accounts", len(auths))
	}
	got := auths[0]
	if got.Provider != "kiro" || got.AccessToken == "" || got.RefreshToken != "refresh-1" {
		t.Fatalf("account = %+v", got)
	}
	if got.ClientID != "cid" || got.ClientSecret != "secret" || got.Region != "eu-west-1" {
		t.Fatalf("client fields = %+v", got)
	}
	if got.Email != "dev@example.com" {
		t.Fatalf("email = %q", got.Email)
	}
	if got.AccountID != "sub-1" {
		t.Fatalf("account id = %q", got.AccountID)
	}
	if got.Expiry.Before(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("expiry = %v", got.Expiry)
	}
}

func TestKiroRefreshKeepsOneAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"accessToken":"new","refreshToken":"r2","expiresIn":3600}`)
	}))
	defer srv.Close()

	store := auth.AuthStore{Path: t.TempDir() + "/auth.jsonc"}
	acct := &auth.Auth{Provider: "kiro", AccountID: "sub-1", AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour), ClientID: "cid", ClientSecret: "sec", Region: "us-east-1"}
	if err := store.Save([]*auth.Auth{acct}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	acct.Expiry = time.Now().Add(-time.Minute)
	engine := &Engine{Store: store, HTTP: srv.Client(), OIDCBase: srv.URL, auths: []*auth.Auth{acct}}
	if _, err := engine.token(context.Background(), acct); err != nil {
		t.Fatalf("token: %v", err)
	}
	auths, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("stored %d accounts after refresh: %+v", len(auths), auths)
	}
	if auths[0].RefreshToken != "r2" || auths[0].AccessToken != "new" {
		t.Fatalf("persisted tokens = %q / %q", auths[0].AccessToken, auths[0].RefreshToken)
	}
}

func TestKiroTokenRefresh(t *testing.T) {
	t.Run("oidc", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/token" {
				t.Errorf("path = %s", r.URL.Path)
			}
			fmt.Fprint(w, `{"accessToken":"new","refreshToken":"r2","expiresIn":3600}`)
		}))
		defer srv.Close()
		a := &auth.Auth{Provider: "kiro", AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Minute), ClientID: "cid", ClientSecret: "sec", Region: "us-east-1"}
		engine := &Engine{HTTP: srv.Client(), OIDCBase: srv.URL, auths: []*auth.Auth{a}}
		tok, err := engine.token(context.Background(), a)
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if tok != "new" || a.RefreshToken != "r2" {
			t.Fatalf("token = %q refresh = %q", tok, a.RefreshToken)
		}
		if !a.Expiry.After(time.Now().Add(50 * time.Minute)) {
			t.Fatalf("expiry = %v", a.Expiry)
		}
	})
	t.Run("desktop", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/refreshToken" {
				t.Errorf("path = %s", r.URL.Path)
			}
			fmt.Fprint(w, `{"accessToken":"dnew","refreshToken":"d2","expiresIn":3600,"profileArn":"arn:desktop"}`)
		}))
		defer srv.Close()
		a := &auth.Auth{Provider: "kiro", AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Minute), Region: "us-east-1"}
		engine := &Engine{HTTP: srv.Client(), AuthBase: srv.URL, auths: []*auth.Auth{a}}
		tok, err := engine.token(context.Background(), a)
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if tok != "dnew" || a.ProfileARN != "arn:desktop" {
			t.Fatalf("token = %q profileArn = %q", tok, a.ProfileARN)
		}
	})
}

func testJWT(payload string) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(payload) + "." + enc("sig")
}

func TestKiroCheck(t *testing.T) {
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Errorf("path = %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		refreshes.Add(1)
		fmt.Fprint(w, `{"accessToken":"new","refreshToken":"r2","expiresIn":3600}`)
	}))
	defer srv.Close()

	store := auth.AuthStore{Path: t.TempDir() + "/auth.jsonc"}
	acct := &auth.Auth{
		Provider: "kiro", AccessToken: "old", RefreshToken: "r1",
		Expiry: time.Now().Add(-time.Minute), Region: "us-east-1",
		ClientID: "cid", ClientSecret: "sec",
	}
	engine := &Engine{Store: store, HTTP: srv.Client(), OIDCBase: srv.URL, auths: []*auth.Auth{acct}}

	checks := engine.Check(context.Background())
	if len(checks) != 1 || checks[0].Err != nil {
		t.Fatalf("checks = %+v", checks)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	if len(checks[0].Models) != len(kiroCatalog) {
		t.Fatalf("models = %d, want %d", len(checks[0].Models), len(kiroCatalog))
	}
	if acct.AccessToken != "new" || acct.RefreshToken != "r2" {
		t.Fatalf("account tokens not refreshed: %+v", acct)
	}
	stored, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].AccessToken != "new" || stored[0].RefreshToken != "r2" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestKiroCheckReportsRefreshFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()

	engine := &Engine{HTTP: srv.Client(), OIDCBase: srv.URL, auths: []*auth.Auth{{
		Provider: "kiro", AccessToken: "old", RefreshToken: "r1", Region: "us-east-1",
		ClientID: "cid", ClientSecret: "sec",
	}}}
	checks := engine.Check(context.Background())
	if len(checks) != 1 || checks[0].Err == nil {
		t.Fatalf("Check must report refresh failures: %+v", checks)
	}
}

func TestKiroCheckKeepsFreshToken(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	acct := &auth.Auth{
		Provider: "kiro", AccessToken: "fresh", RefreshToken: "r1",
		Expiry: time.Now().Add(time.Hour), Region: "us-east-1",
		ClientID: "cid", ClientSecret: "sec",
	}
	engine := &Engine{HTTP: srv.Client(), OIDCBase: srv.URL, auths: []*auth.Auth{acct}}
	checks := engine.Check(context.Background())
	if len(checks) != 1 || checks[0].Err != nil {
		t.Fatalf("checks = %+v", checks)
	}
	if calls.Load() != 0 {
		t.Fatalf("fresh token must not be refreshed, got %d refresh calls", calls.Load())
	}
	if acct.RefreshToken != "r1" {
		t.Fatalf("refresh token rotated: %+v", acct)
	}
}
