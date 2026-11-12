package backend

import "context"

// BackendAPI is the provider-neutral contract every backend implements.
type BackendAPI interface {
	Models(context.Context) ([]Model, error)
	HasModel(context.Context, string) bool
	Complete(context.Context, string, string) (*Result, error)
	Stream(context.Context, string, string, func(string)) (*Result, error)
	Chat(context.Context, *ChatRequest, func(ChatDelta)) (*ChatResult, error)
	Default() string
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
