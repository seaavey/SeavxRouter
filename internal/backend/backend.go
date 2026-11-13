package backend

import (
	"context"
	"time"
)

// BackendAPI is the provider-neutral contract every backend implements.
type BackendAPI interface {
	Models(context.Context) ([]Model, error)
	HasModel(context.Context, string) bool
	Complete(context.Context, string, string) (*Result, error)
	Stream(context.Context, string, string, func(string)) (*Result, error)
	Chat(context.Context, *ChatRequest, func(ChatDelta)) (*ChatResult, error)
	Default() string
}

// AccountCheck is the outcome of one account's live credential and model
// check. Err is nil when the account is usable.
type AccountCheck struct {
	Account   string
	AccountID string
	Models    []Model
	Err       error
	Latency   time.Duration
}

// Checker verifies every configured account against its upstream.
type Checker interface {
	Check(context.Context) []AccountCheck
}

// Usage is the token accounting reported by a backend for one request.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

// Result is the outcome of a plain-text Complete/Stream call.
type Result struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
	Error          string `json:"error"`
	NumTurns       int    `json:"num_turns"`
	Usage          Usage  `json:"usage"`
}

// Model is one model ID advertised by a backend.
type Model struct {
	ID   string
	Name string
}
