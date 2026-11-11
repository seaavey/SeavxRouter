# Design Document: OpenAI & Anthropic Provider Standards for SeavxRouter

## Context & Motivation
SeavxRouter acts as a local OpenAI-compatible router and gateway proxying requests to Antigravity (Google internal Gemini/Claude) and OpenAI Codex. 

Currently:
1. `/v1/chat/completions` does not handle OpenAI tool-calling specifications (`tools`, `tool_choice`, `tool_calls`, `role: "tool"`).
2. It collapses multi-turn chat messages into a single text prompt string. When tools are discussed, Antigravity's model produces `call:<tool_name>{...}`, which causes upstream Google servers to reject the response with `finishReason: "MALFORMED_FUNCTION_CALL"` and return an empty response.
3. Model names like `gemini.3.8-flash` or `gemini-3.8-flash` fail with HTTP 404 because SeavxRouter expects exact model IDs like `gemini-3.8-flash-high`.
4. Anthropic's wire protocol (`/v1/messages`) is completely absent, preventing clients configured for Anthropic provider protocol (including Hermes Agent, Claude Code CLI, etc.) from interacting with SeavxRouter.

## Goals
1. **Full OpenAI Compatibility (`/v1/chat/completions`):**
   - Accept and process `tools`, `tool_choice`, and multi-turn messages containing tool calls and tool responses.
   - Stream `delta.tool_calls` and non-streaming `choices[0].message.tool_calls` with `finish_reason: "tool_calls"`.
2. **Full Anthropic Compatibility (`/v1/messages`):**
   - Expose `POST /v1/messages` following Anthropic Messages API v1 specification.
   - Support `system`, `messages` (text blocks, `tool_use` blocks, `tool_result` blocks), and `tools` (with `input_schema`).
   - Stream standard Anthropic SSE events: `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`.
3. **Upstream Antigravity Native Function Calling Support:**
   - Translate OpenAI/Anthropic tool schemas into Gemini `functionDeclarations`.
   - Translate multi-turn message history into Gemini structured `contents` (`user`, `model`, and `function`/`functionResponse`).
   - Parse `functionCall` parts from Antigravity SSE stream and emit them cleanly to the client.
4. **Model Alias Normalization:**
   - Automatically normalize common user model requests (e.g., `gemini.3.8-flash`, `gemini-3.8-flash`, `gemini-flash` $\rightarrow$ `gemini-3.8-flash-high`).
   - Normalize Claude aliases (`claude-3-7-sonnet`, `claude-3-5-sonnet` $\rightarrow$ `claude-sonnet-4-6`).

## Non-Goals & Anti-Goals
- Modifying upstream Google or OpenAI APIs.
- Breaking backward compatibility with simple text queries or existing `/v1/responses` endpoints.
- Introducing external non-standard dependencies (stick to Go standard library).

## Architecture & Implementation Details

### 1. Unified Types
Define unified structures for structured chat communication across backends:
- `ToolDefinition`: `Name`, `Description`, `Parameters` (JSON schema map).
- `ToolCall`: `ID`, `Name`, `Arguments` (JSON string).
- `ChatMessage`: `Role`, `Content`, `Name`, `ToolCallID`, `ToolCalls []ToolCall`.
- `ChatRequest`: `Model`, `Messages`, `Tools`, `Stream`, `Temperature`, `MaxTokens`.
- `ChatDelta`: `Content`, `ToolCalls []ToolCall`, `FinishReason`, `Usage`.

### 2. Backend Interface Extension
Update `BackendAPI` in `main.go` / `router.go`:
```go
type BackendAPI interface {
    Models(context.Context) ([]Model, error)
    HasModel(context.Context, string) bool
    Complete(context.Context, string, string) (*Result, error)
    Stream(context.Context, string, string, func(string)) (*Result, error)
    Chat(context.Context, *ChatRequest, func(ChatDelta)) (*ChatResult, error)
    Default() string
}
```

### 3. Antigravity Engine (`engine.go`)
- **Tool Mapping:**
  Convert `ToolDefinition` list into Antigravity `tools: [{"functionDeclarations": [...]}]`.
- **History Mapping:**
  Convert `ChatMessage` history into Gemini `contents`:
  - `user` role $\rightarrow$ `role: "user"`, `parts: [{"text": ...}]`
  - `assistant` role without tool calls $\rightarrow$ `role: "model"`, `parts: [{"text": ...}]`
  - `assistant` role with tool calls $\rightarrow$ `role: "model"`, `parts: [{"functionCall": {"name": ..., "args": ...}}]`
  - `tool` role $\rightarrow$ `role: "function"`, `parts: [{"functionResponse": {"name": ..., "response": {"output": ...}}}]`
- **Stream Parser:**
  In `streamOnce`:
  Parse both `parts[].text` and `parts[].functionCall`.
  Detect `finishReason: "STOP"` vs `"MALFORMED_FUNCTION_CALL"`.
  Emit `ChatDelta` with content or tool calls.

### 4. OpenAI Handler (`openai.go`)
- Update `handleChat`:
  - Decode `tools`, `tool_choice`, `messages` (including role `tool` and `tool_calls`).
  - Invoke `backend.Chat(...)`.
  - Handle streaming: emit `choices[0].delta.content` or `choices[0].delta.tool_calls` with matching index and ID.
  - Handle non-streaming: emit `choices[0].message.content` or `choices[0].message.tool_calls`.

### 5. Anthropic Handler (`anthropic.go`)
- Add handler `handleAnthropicMessages`:
  - Decode `system`, `messages` (parsing `content` strings or blocks `type: "text"`, `type: "tool_use"`, `type: "tool_result"`), `tools` (`input_schema`), `stream`.
  - Convert to unified `ChatRequest`.
  - Invoke `backend.Chat(...)`.
  - Format response:
    - Non-streaming: Anthropic message object with `content: [{type: "text", text: ...}, {type: "tool_use", ...}]`.
    - Streaming: Anthropic SSE events (`message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`).

### 6. Model Aliasing & Routing (`router.go`)
Normalize model IDs before routing:
- `gemini.3.8-flash`, `gemini-3.8-flash`, `gemini-3.8-flash-preview` $\rightarrow$ `gemini-3.8-flash-high`
- `gemini.2.5-flash`, `gemini-2.5-flash` $\rightarrow$ `gemini-2.5-flash`
- `claude-3-7-sonnet*`, `claude-3-5-sonnet*` $\rightarrow$ `claude-sonnet-4-6`
- `claude-3-opus*` $\rightarrow$ `claude-opus-4-6-thinking`

## Verification Plan
1. Unit tests:
   - Request serialization & deserialization (OpenAI & Anthropic).
   - Tool declaration mapping to Gemini schema.
   - Function call parsing and stream delta emissions.
   - Model alias normalization tests.
2. Integration / E2E tests:
   - Curl test for OpenAI tool calling endpoint.
   - Curl test for Anthropic `/v1/messages` endpoint (both streaming and non-streaming).
   - Live Hermes CLI test with `-m gemini.3.8-flash` performing file generation / tool calling.
