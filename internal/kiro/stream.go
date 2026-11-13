package kiro

import (
	"bytes"
	"encoding/json"
	"strings"

	"seavxrouter/internal/backend"
)

// kiroEventKind identifies a JSON payload found in the event stream.
type kiroEventKind int

const (
	kiroEventContent kiroEventKind = iota
	kiroEventToolStart
	kiroEventToolInput
	kiroEventToolStop
	kiroEventIgnore
	kiroEventUsage
	kiroEventContext
)

// The runtime frames events in an AWS event-stream container, but the payloads
// are plain JSON objects; scanning for these prefixes avoids implementing the
// binary framing.
var kiroEventMarkers = []struct {
	marker string
	kind   kiroEventKind
}{
	{`{"content":`, kiroEventContent},
	{`{"name":`, kiroEventToolStart},
	{`{"input":`, kiroEventToolInput},
	{`{"stop":`, kiroEventToolStop},
	{`{"followupPrompt":`, kiroEventIgnore},
	{`{"usage":`, kiroEventUsage},
	{`{"contextUsagePercentage":`, kiroEventContext},
}

type kiroWireEvent struct {
	Content                string          `json:"content"`
	Name                   string          `json:"name"`
	ToolUseID              string          `json:"toolUseId"`
	Input                  json.RawMessage `json:"input"`
	Stop                   bool            `json:"stop"`
	Usage                  float64         `json:"usage"`
	ContextUsagePercentage float64         `json:"contextUsagePercentage"`
}

type kiroStreamParser struct {
	buf         []byte
	content     strings.Builder
	lastContent string
	current     *backend.ToolCall
	calls       []backend.ToolCall
	contextPct  float64
}

func (p *kiroStreamParser) feed(chunk []byte, emit func(backend.ChatDelta)) {
	p.buf = append(p.buf, chunk...)
	for {
		start, kind := nextKiroEvent(p.buf)
		if start < 0 {
			break
		}
		end := kiroJSONEnd(p.buf, start)
		if end < 0 {
			break
		}
		raw := p.buf[start : end+1]
		p.buf = p.buf[end+1:]
		p.handle(raw, kind, emit)
	}
}

func (p *kiroStreamParser) handle(raw []byte, kind kiroEventKind, emit func(backend.ChatDelta)) {
	var ev kiroWireEvent
	valid := json.Unmarshal(raw, &ev) == nil
	switch kind {
	case kiroEventContent:
		if !valid {
			return
		}
		// The runtime repeats the full content in some frames; drop duplicates.
		if ev.Content == p.lastContent {
			return
		}
		p.lastContent = ev.Content
		if ev.Content == "" {
			return
		}
		p.content.WriteString(ev.Content)
		if emit != nil {
			emit(backend.ChatDelta{Content: ev.Content})
		}
	case kiroEventToolStart:
		if !valid {
			return
		}
		p.finalizeTool()
		p.current = &backend.ToolCall{
			ID:        ev.ToolUseID,
			Type:      "function",
			Name:      ev.Name,
			Arguments: kiroToolInputString(ev.Input),
		}
		if ev.Stop {
			p.finalizeTool()
		}
	case kiroEventToolInput:
		if !valid || p.current == nil {
			return
		}
		p.current.Arguments += kiroToolInputString(ev.Input)
	case kiroEventToolStop:
		if !valid {
			return
		}
		if ev.Stop {
			p.finalizeTool()
		}
	case kiroEventContext:
		if valid && ev.ContextUsagePercentage > 0 {
			p.contextPct = ev.ContextUsagePercentage
		}
	}
}

func (p *kiroStreamParser) finalizeTool() {
	if p.current == nil {
		return
	}
	call := *p.current
	p.current = nil
	if call.ID == "" {
		call.ID = backend.NewID("call_")
	}
	call.Arguments = normalizeKiroArgs(call.Arguments)
	p.calls = append(p.calls, call)
}

func (p *kiroStreamParser) finish(emit func(backend.ChatDelta)) []backend.ToolCall {
	p.finalizeTool()
	calls := dedupeKiroToolCalls(p.calls)
	if emit != nil {
		for _, call := range calls {
			emit(backend.ChatDelta{ToolCalls: []backend.ToolCall{call}})
		}
	}
	return calls
}

func nextKiroEvent(buf []byte) (int, kiroEventKind) {
	best := -1
	kind := kiroEventIgnore
	for _, m := range kiroEventMarkers {
		if i := bytes.Index(buf, []byte(m.marker)); i >= 0 && (best < 0 || i < best) {
			best = i
			kind = m.kind
		}
	}
	return best, kind
}

// kiroJSONEnd returns the index of the '}' closing the object at start, or -1
// when the object is not yet complete.
func kiroJSONEnd(buf []byte, start int) int {
	depth := 0
	inStr, esc := false, false
	for i := start; i < len(buf); i++ {
		c := buf[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// kiroToolInputString renders an "input" field as text: strings verbatim,
// objects compacted to JSON. An empty object counts as no input.
func kiroToolInputString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == "{}" {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return s
		}
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, trimmed) == nil {
		return buf.String()
	}
	return ""
}

func normalizeKiroArgs(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "{}"
	}
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(args)) != nil {
		return "{}"
	}
	return buf.String()
}

func dedupeKiroToolCalls(calls []backend.ToolCall) []backend.ToolCall {
	kept := make([]backend.ToolCall, 0, len(calls))
	index := make(map[string]int, len(calls))
	for _, call := range calls {
		if call.ID != "" {
			if i, ok := index[call.ID]; ok {
				if len(call.Arguments) > len(kept[i].Arguments) {
					kept[i] = call
				}
				continue
			}
			index[call.ID] = len(kept)
		}
		kept = append(kept, call)
	}
	out := make([]backend.ToolCall, 0, len(kept))
	seen := make(map[string]bool, len(kept))
	for _, call := range kept {
		key := call.Name + "\x00" + call.Arguments
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, call)
	}
	return out
}
