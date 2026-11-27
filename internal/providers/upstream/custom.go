package upstream

import (
	"context"
	"fmt"
	"strings"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// ProbeCatalog validates an API key by fetching the provider's model catalog.
// This is the gate the dashboard uses when a provider is added: a key that
// cannot list models gives the router nothing to route to. Unlike ProbeKey it
// sends no chat completion, so no upstream tokens are spent and a provider with
// no chat allowance on its free tier can still be connected.
func ProbeCatalog(ctx context.Context, name, baseURL, key, sdk string) ([]string, error) {
	acct := &auth.Auth{Provider: name, AccessToken: key}
	e, err := NewEngineSDK(name, baseURL, sdk, auth.AuthStore{}, []*auth.Auth{acct})
	if err != nil {
		return nil, err
	}
	list, err := e.fetchModelsWith(ctx, acct)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s returned an empty model catalog", name)
	}
	return ids, nil
}

// ProbeKey verifies a bearer key against a custom base URL. It requires a
// non-empty model catalog and sends one tiny chat completion, so endpoints that
// publish /models without authentication still get their key checked. A quota
// rejection means the key authenticated but the account has no allowance; that
// is reported as a note instead of a failure. probeModel pins the smoke test to
// one model, for providers whose first catalog entries are outside the caller's
// free tier; empty tries up to three chat-capable models. sdk selects the wire
// protocol: "anthropic" for the Messages API, anything else for the
// OpenAI-compatible surface.
func ProbeKey(ctx context.Context, name, baseURL, key, probeModel, sdk string) (models []string, note string, err error) {
	acct := &auth.Auth{Provider: name, AccessToken: key}
	e, err := NewEngineSDK(name, baseURL, sdk, auth.AuthStore{}, []*auth.Auth{acct})
	if err != nil {
		return nil, "", err
	}
	list, err := e.fetchModelsWith(ctx, acct)
	if err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ID)
	}

	candidates := probeModels(ids)
	if probeModel != "" {
		candidates = []string{probeModel}
	}
	maxTokens := 16
	var lastErr error
	for _, model := range candidates {
		_, chatErr := e.Chat(ctx, &backend.ChatRequest{
			Model:     model,
			Messages:  []backend.ChatMessage{{Role: "user", Content: "ping"}},
			MaxTokens: &maxTokens,
		}, nil)
		if chatErr == nil {
			return ids, "", nil
		}
		if backend.IsQuotaError(chatErr) {
			return ids, fmt.Sprintf("key accepted, but %s currently reports no allowance: %v", name, chatErr), nil
		}
		if backend.IsAuthError(chatErr) {
			return nil, "", fmt.Errorf("API key rejected: %w", chatErr)
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		lastErr = chatErr
	}
	return nil, "", fmt.Errorf("chat check failed: %w", lastErr)
}

// probeModels picks up to three chat-capable catalog entries, skipping models
// that clearly serve embeddings, reranking, audio, images or moderation.
func probeModels(ids []string) []string {
	var chat []string
	for _, id := range ids {
		lower := strings.ToLower(id)
		skip := false
		for _, marker := range []string{"embed", "rerank", "whisper", "moderation", "tts", "image", "audio", "vision-encoder"} {
			if strings.Contains(lower, marker) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		chat = append(chat, id)
		if len(chat) == 3 {
			break
		}
	}
	if len(chat) == 0 {
		chat = ids
		if len(chat) > 3 {
			chat = chat[:3]
		}
	}
	return chat
}
