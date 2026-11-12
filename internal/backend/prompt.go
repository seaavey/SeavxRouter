package backend

import "strings"

// --- shared wire/error helpers ---

type textMessage struct {
	Role string
	Text string
}

func buildPrompt(instructions string, msgs []textMessage) string {
	if instructions == "" && len(msgs) == 1 && msgs[0].Role == "user" {
		return msgs[0].Text
	}
	var b strings.Builder
	if instructions != "" {
		b.WriteString(instructions)
		b.WriteString("\n\n")
	}
	for _, m := range msgs {
		var label string
		switch m.Role {
		case "system", "developer":
			label = "[System instructions]\n"
		case "user":
			label = "User: "
		case "assistant":
			label = "Assistant: "
		case "tool":
			label = "Tool result: "
		default:
			continue
		}
		b.WriteString(label)
		b.WriteString(m.Text)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply to the final user message.")
	return b.String()
}
