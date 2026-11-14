package kiro

import (
	"encoding/json"
	"fmt"
	"strings"

	"seavxrouter/internal/backend"
)

type kiroImageSource struct {
	Bytes string `json:"bytes"`
}

type kiroImage struct {
	Format string          `json:"format"`
	Source kiroImageSource `json:"source"`
}

type kiroToolResultText struct {
	Text string `json:"text"`
}

type kiroToolResult struct {
	ToolUseID string               `json:"toolUseId"`
	Content   []kiroToolResultText `json:"content"`
	Status    string               `json:"status"`
}

type kiroInputSchema struct {
	JSON map[string]any `json:"json"`
}

type kiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema kiroInputSchema `json:"inputSchema"`
}

type kiroTool struct {
	ToolSpecification kiroToolSpecification `json:"toolSpecification"`
}

type kiroToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

type kiroContext struct {
	Tools       []kiroTool       `json:"tools,omitempty"`
	ToolResults []kiroToolResult `json:"toolResults,omitempty"`
}

type kiroUserInputMessage struct {
	Content                 string       `json:"content"`
	ModelID                 string       `json:"modelId"`
	Origin                  string       `json:"origin"`
	Images                  []kiroImage  `json:"images,omitempty"`
	UserInputMessageContext *kiroContext `json:"userInputMessageContext,omitempty"`
}

type kiroAssistantMessage struct {
	Content  string        `json:"content"`
	ToolUses []kiroToolUse `json:"toolUses,omitempty"`
}

type kiroInferenceConfig struct {
	MaxTokens   *int     `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type kiroPayload struct {
	ConversationState kiroConversationState `json:"conversationState"`
	ProfileARN        string                `json:"profileArn,omitempty"`
	InferenceConfig   *kiroInferenceConfig  `json:"inferenceConfig,omitempty"`
}

type kiroConversationState struct {
	AgentContinuationID string           `json:"agentContinuationId,omitempty"`
	AgentTaskType       string           `json:"agentTaskType,omitempty"`
	ChatTriggerType     string           `json:"chatTriggerType"`
	ConversationID      string           `json:"conversationId"`
	CurrentMessage      kiroCurrentMsg   `json:"currentMessage"`
	History             []map[string]any `json:"history"`
}

type kiroCurrentMsg struct {
	UserInputMessage kiroUserInputMessage `json:"userInputMessage"`
}

// intermediate turn types used while assembling the wire payload
type kiroUserTurn struct {
	content     string
	images      []kiroImage
	toolResults []kiroToolResult
}

type kiroAssistantTurn struct {
	content  string
	toolUses []kiroToolUse
}

type kiroTurn struct {
	user      *kiroUserTurn
	assistant *kiroAssistantTurn
}

// buildKiroPayload converts unified chat messages into the Kiro runtime
// conversationState shape. Consecutive user-role content is merged into one
// turn, tool results attach to the following user turn, and any system prompt
// is folded into the first user turn.
func buildKiroPayload(model string, req *backend.ChatRequest, profileARN string) ([]byte, error) {
	var system strings.Builder
	turns := make([]kiroTurn, 0, len(req.Messages))
	var open *kiroUserTurn
	flush := func() {
		if open == nil {
			return
		}
		turns = append(turns, kiroTurn{user: open})
		open = nil
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(m.Content)
		case "assistant":
			flush()
			turn := &kiroAssistantTurn{content: m.Content}
			for _, call := range m.ToolCalls {
				input := map[string]any{}
				if call.Arguments != "" {
					if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
						input = map[string]any{}
					}
				}
				turn.toolUses = append(turn.toolUses, kiroToolUse{ToolUseID: call.ID, Name: call.Name, Input: input})
			}
			turns = append(turns, kiroTurn{assistant: turn})
		case "tool":
			if open == nil {
				open = &kiroUserTurn{}
			}
			open.toolResults = append(open.toolResults, kiroToolResult{
				ToolUseID: m.ToolCallID,
				Content:   []kiroToolResultText{{Text: m.Content}},
				Status:    "success",
			})
		default:
			if open == nil {
				open = &kiroUserTurn{}
			}
			if m.Content != "" {
				if open.content != "" {
					open.content += "\n\n"
				}
				open.content += m.Content
			}
			for _, img := range m.Images {
				if img.Data == "" {
					return nil, fmt.Errorf("kiro backend requires inline image data; remote image URLs are not supported")
				}
				open.images = append(open.images, kiroImage{
					Format: kiroImageFormat(img.MIMEType),
					Source: kiroImageSource{Bytes: img.Data},
				})
			}
		}
	}
	flush()

	if system.Len() > 0 {
		systemText := system.String()
		switch {
		case len(turns) == 0 || turns[0].user == nil:
			turns = append([]kiroTurn{{user: &kiroUserTurn{content: systemText}}}, turns...)
		case turns[0].user.content != "":
			turns[0].user.content = systemText + "\n\n" + turns[0].user.content
		default:
			turns[0].user.content = systemText
		}
	}
	if len(turns) == 0 {
		return nil, fmt.Errorf("kiro: empty conversation")
	}
	if turns[len(turns)-1].user == nil {
		turns = append(turns, kiroTurn{user: &kiroUserTurn{content: "Continue."}})
	}

	last := turns[len(turns)-1].user
	current := kiroUserInputMessage{
		Content: last.content,
		ModelID: model,
		Origin:  "AI_EDITOR",
		Images:  last.images,
	}
	context := &kiroContext{ToolResults: last.toolResults}
	tools := req.Tools
	if choice, ok := req.ToolChoice.(string); ok && choice == "none" {
		tools = nil
	}
	for _, tool := range tools {
		params := tool.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		context.Tools = append(context.Tools, kiroTool{ToolSpecification: kiroToolSpecification{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: kiroInputSchema{JSON: params},
		}})
	}
	if len(context.Tools) > 0 || len(context.ToolResults) > 0 {
		current.UserInputMessageContext = context
	}

	history := make([]map[string]any, 0, len(turns)-1)
	for _, turn := range turns[:len(turns)-1] {
		if turn.user != nil {
			msg := kiroUserInputMessage{
				Content: turn.user.content,
				ModelID: model,
				Origin:  "AI_EDITOR",
				Images:  turn.user.images,
			}
			if len(turn.user.toolResults) > 0 {
				msg.UserInputMessageContext = &kiroContext{ToolResults: turn.user.toolResults}
			}
			history = append(history, map[string]any{"userInputMessage": msg})
			continue
		}
		history = append(history, map[string]any{"assistantResponseMessage": kiroAssistantMessage{
			Content:  turn.assistant.content,
			ToolUses: turn.assistant.toolUses,
		}})
	}

	payload := kiroPayload{
		ProfileARN: profileARN,
		ConversationState: kiroConversationState{
			AgentContinuationID: backend.UUID(),
			AgentTaskType:       "vibe",
			ChatTriggerType:     "MANUAL",
			ConversationID:      backend.UUID(),
			CurrentMessage:      kiroCurrentMsg{UserInputMessage: current},
			History:             history,
		},
	}
	if req.MaxTokens != nil || req.Temperature != nil {
		payload.InferenceConfig = &kiroInferenceConfig{MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	}
	return json.Marshal(payload)
}

func kiroImageFormat(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return "png"
}
