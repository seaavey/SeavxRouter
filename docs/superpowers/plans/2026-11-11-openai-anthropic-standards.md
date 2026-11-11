# OpenAI & Anthropic Provider Standards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transform SeavxRouter into a fully compliant proxy for both OpenAI (`/v1/chat/completions`) and Anthropic (`/v1/messages`) APIs, complete with tool-calling translation to Antigravity/Gemini native `functionDeclarations` and model alias normalization.

**Architecture:** Introduce unified structured chat models (`ChatRequest`, `ChatMessage`, `ToolDefinition`, `ToolCall`, `ChatDelta`), extend `BackendAPI.Chat`, implement native Gemini function-calling conversion in `engine.go`, update `openai.go` for OpenAI tool_calls streaming, add `anthropic.go` for `/v1/messages`, and normalize model aliases in `router.go`.

**Tech Stack:** Go 1.24+ (Standard Library only: `net/http`, `encoding/json`, `bufio`, `bytes`, `strings`, `sync`, `testing`).

**Spec:** [docs/specs/2026-11-11-openai-anthropic-standards-design.md](file:///home/seaavey/Projects/SeavxRouter/docs/specs/2026-11-11-openai-anthropic-standards-design.md)

## Global Constraints
- Pure Go standard library: no new external dependencies or `go.mod` additions.
- Preserve existing endpoints `/v1/models`, `/v1/responses`, and `/healthz`.
- Do not break existing plain text completions.
- Model aliases must gracefully resolve dots and dashes (e.g. `gemini.3.8-flash` and `gemini-3.8-flash`).

## Review Focus
1. **Tool call streaming delta serialization:** Partial tool call chunk indices and JSON escaping in OpenAI SSE chunks must strictly conform to OpenAI spec so client SDKs (Hermes, OpenAI Python SDK) don't crash.
2. **Anthropic SSE event sequence:** Streaming must emit events in exact order (`message_start` -> `content_block_start` -> `content_block_delta` -> `content_block_stop` -> `message_delta` -> `message_stop`).
3. **Antigravity `functionCall` handling:** Upstream `functionCall` must not trigger `MALFORMED_FUNCTION_CALL` by always ensuring `functionDeclarations` are sent whenever tools are provided.
4. **Tool response multi-turn continuity:** `role: "tool"` (OpenAI) and `tool_result` (Anthropic) must correctly map to Gemini `role: "function"` with `functionResponse`.
5. **Model alias resolution:** Unrecognized models must fall back cleanly without breaking existing explicit provider prefixes (`codex/` or `antigravity/`).

---

### Task 1: Model Alias Normalization & Routing

**Files:**
- Modify: `router.go:20-60`
- Test: `router_test.go`

**Interfaces:**
- Produces: `normalizeModel(model string) string` in `router.go`
- Behavior:
  - Maps `gemini.3.8-flash`, `gemini-3.8-flash`, `gemini-3.8-flash-preview` -> `gemini-3.8-flash-high`
  - Maps `gemini.2.5-flash`, `gemini-2.5-flash` -> `gemini-2.5-flash`
  - Maps `gemini.2.5-pro`, `gemini-2.5-pro` -> `gemini-2.5-pro`
  - Maps `claude-3-7-sonnet*`, `claude-3-5-sonnet*` -> `claude-sonnet-4-6`
  - Maps `claude-3-opus*` -> `claude-opus-4-6-thinking`

- [ ] **Step 1: Write the failing test for model normalization in `router_test.go`**
- [ ] **Step 2: Run `go test -run TestNormalizeModel` to verify failure**
- [ ] **Step 3: Implement `normalizeModel` in `router.go` and hook it into `MultiBackend.resolve` and `HasModel`**
- [ ] **Step 4: Run `go test -run TestNormalizeModel` to verify pass**

---

### Task 2: Unified Chat Types and Backend Interface

**Files:**
- Create: `chat.go`
- Modify: `main.go:20-30`
- Modify: `router.go`
- Test: `chat_test.go`

**Interfaces:**
- Produces:
  ```go
  type ToolDefinition struct {
      Name        string         `json:"name"`
      Description string         `json:"description,omitempty"`
      Parameters  map[string]any `json:"parameters,omitempty"`
  }
  type ToolCall struct {
      ID        string `json:"id"`
      Type      string `json:"type"`
      Name      string `json:"name"`
      Arguments string `json:"arguments"`
  }
  type ChatMessage struct {
      Role       string     `json:"role"`
      Content    string     `json:"content"`
      Name       string     `json:"name,omitempty"`
      ToolCallID string     `json:"tool_call_id,omitempty"`
      ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
  }
  type ChatRequest struct {
      Model       string           `json:"model"`
      Messages    []ChatMessage    `json:"messages"`
      Tools       []ToolDefinition `json:"tools,omitempty"`
      ToolChoice  any              `json:"tool_choice,omitempty"`
      Stream      bool             `json:"stream"`
      Temperature *float64         `json:"temperature,omitempty"`
      MaxTokens   *int             `json:"max_tokens,omitempty"`
  }
  type ChatDelta struct {
      Content      string
      ToolCalls    []ToolCall
      FinishReason string
      Usage        *Usage
  }
  type ChatResult struct {
      Content      string
      ToolCalls    []ToolCall
      Usage        Usage
      FinishReason string
  }
  ```
- Updates `BackendAPI`:
  Add `Chat(context.Context, *ChatRequest, func(ChatDelta)) (*ChatResult, error)`.

- [ ] **Step 1: Write unit tests in `chat_test.go` for chat request/delta structures**
- [ ] **Step 2: Define types in `chat.go` and update `BackendAPI` interface in `main.go`**
- [ ] **Step 3: Implement delegating `Chat` method in `MultiBackend` in `router.go` and stubs in `agy.go`**
- [ ] **Step 4: Run `go test ./...` to verify build and test pass**

---

### Task 3: Antigravity Native Tool Calling & Structured Contents in `engine.go`

**Files:**
- Modify: `engine.go:580-745`
- Test: `engine_test.go`

**Interfaces:**
- Consumes: `ChatRequest`, `ChatMessage`, `ToolDefinition`, `ToolCall`, `ChatDelta`, `ChatResult` from `chat.go`
- Produces: `(e *Engine) Chat(ctx context.Context, req *ChatRequest, onDelta func(ChatDelta)) (*ChatResult, error)`

**Details:**
1. Convert `req.Messages` to Antigravity `contents`:
   - `user`: `parts: [{"text": msg.Content}]`
   - `assistant`: if `len(msg.ToolCalls) > 0`, `parts: [{"functionCall": {"name": tc.Name, "args": unmarshaledArgs, "id": tc.ID}}]`, else `parts: [{"text": msg.Content}]`
   - `tool`: `role: "function"`, `parts: [{"functionResponse": {"name": msg.Name, "response": {"output": msg.Content}}}]`
2. Convert `req.Tools` to `tools: [{"functionDeclarations": [...]}]` where each declaration has `name`, `description`, and `parameters` (standardized to uppercase types like `"OBJECT"`, `"STRING"` if needed, or passed as schema).
3. In `streamOnce`:
   - Parse `cand.Content.Parts`:
     - If `part.Text != "" && !part.Thought`: append to content and emit `ChatDelta{Content: part.Text}`.
     - If `part.FunctionCall != nil`: extract name, args (marshaled to JSON string), id, and emit `ChatDelta{ToolCalls: [...]}`.
   - Parse `cand.FinishReason`: map `"STOP"` to `"stop"`, `"MAX_TOKENS"` to `"length"`. If tool calls present, set finish reason to `"tool_calls"`.

- [ ] **Step 1: Write unit tests in `engine_test.go` verifying message conversion and function declaration conversion**
- [ ] **Step 2: Run `go test -run TestEngineConversions` to verify failure**
- [ ] **Step 3: Implement Antigravity conversion and `(e *Engine) Chat` in `engine.go`**
- [ ] **Step 4: Run `go test -run TestEngineConversions` to verify pass**

---

### Task 4: OpenAI Chat Completions Tool Calling (`openai.go`)

**Files:**
- Modify: `openai.go:160-260`
- Test: `openai_test.go`

**Interfaces:**
- Consumes: `BackendAPI.Chat`
- Produces: OpenAI compliant `/v1/chat/completions` handling `tools`, `tool_choice`, `role: "tool"`, and emitting `tool_calls` in both non-streaming and streaming chunk SSE modes.

**Details:**
- Parse incoming `chatRequest`:
  - `tools`: `[]openAITool` (`type: "function"`, `function: {name, description, parameters}`)
  - `messages`: support `tool_calls` on assistant messages and `tool_call_id` on tool messages.
- Non-streaming:
  - If `len(result.ToolCalls) > 0`: `choices[0].message.tool_calls = result.ToolCalls`, `choices[0].finish_reason = "tool_calls"`.
- Streaming SSE:
  - When `delta.ToolCalls` arrive:
    ```json
    "delta": {
      "tool_calls": [
        {
          "index": 0,
          "id": "...",
          "type": "function",
          "function": {"name": "...", "arguments": "..."}
        }
      ]
    }
    ```
  - When finished with tool calls: `finish_reason: "tool_calls"`.

- [ ] **Step 1: Write test in `openai_test.go` for OpenAI tool calling request parsing and streaming chunks**
- [ ] **Step 2: Run `go test -run TestOpenAIToolCalls` to verify failure**
- [ ] **Step 3: Update `handleChat` in `openai.go` to use `backend.Chat` and serialize tool calls**
- [ ] **Step 4: Run `go test -run TestOpenAIToolCalls` to verify pass**

---

### Task 5: Anthropic Messages API (`/v1/messages` in `anthropic.go`)

**Files:**
- Create: `anthropic.go`
- Modify: `main.go:155-170`
- Test: `anthropic_test.go`

**Interfaces:**
- Consumes: `BackendAPI.Chat`
- Produces: `(s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request)`

**Details:**
- Route: `POST /v1/messages`
- Request:
  - `model`: string
  - `system`: string or array of text blocks
  - `messages`: array of `role: "user" | "assistant"`, `content: string | []contentBlock`
  - `tools`: array of `{"name": "...", "description": "...", "input_schema": {...}}`
  - `stream`: bool
  - `max_tokens`: int
- Response (Non-streaming):
  - `id`: `"msg_" + uuid`
  - `type`: `"message"`
  - `role`: `"assistant"`
  - `content`: array of blocks:
    - Text: `{"type": "text", "text": "..."}`
    - Tool call: `{"type": "tool_use", "id": "...", "name": "...", "input": {...}}`
  - `stop_reason`: `"end_turn"` or `"tool_use"`
  - `usage`: `{"input_tokens": ..., "output_tokens": ...}`
- Response (Streaming SSE):
  - Event `message_start`
  - Event `content_block_start`
  - Event `content_block_delta` (text_delta or input_json_delta)
  - Event `content_block_stop`
  - Event `message_delta` (stop_reason, usage)
  - Event `message_stop`

- [ ] **Step 1: Write unit tests in `anthropic_test.go` for `/v1/messages` serialization and streaming**
- [ ] **Step 2: Run `go test -run TestAnthropicMessages` to verify failure**
- [ ] **Step 3: Implement `anthropic.go` and mount endpoint in `main.go`**
- [ ] **Step 4: Run `go test -run TestAnthropicMessages` to verify pass**

---

### Task 6: Rebuild, Deploy, and Live End-to-End Verification

**Files:**
- Rebuild binary: `seavxrouter`
- Restart daemon task
- Verify Hermes CLI live execution

- [ ] **Step 1: Recompile `seavxrouter` with `go build -o seavxrouter .`**
- [ ] **Step 2: Restart `seavxrouter` process on `127.0.0.1:8080`**
- [ ] **Step 3: Verify OpenAI tool calling via curl**
- [ ] **Step 4: Verify Anthropic `/v1/messages` via curl**
- [ ] **Step 5: Run live Hermes CLI test with `hermes chat -m gemini.3.8-flash -q "Create /tmp/hermes_test.txt with 'hello from hermes' and read it back" --oneshot -Q --yolo` and verify success**
