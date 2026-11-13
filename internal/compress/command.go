package compress

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"seavxrouter/internal/backend"
)

type outputKind int

const (
	kindGeneric outputKind = iota
	kindDiff
	kindStatus
	kindLog
	kindShow
	kindGrep
	kindList
	kindTest
	kindLint
)

// collectCommands maps tool call IDs to the shell command the assistant asked
// to run, so a tool result can be filtered by what produced it even when the
// content alone is ambiguous.
func collectCommands(msgs []backend.ChatMessage) map[string]string {
	commands := make(map[string]string)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if cmd := commandFromArgs(tc.Arguments); cmd != "" {
				commands[tc.ID] = cmd
			}
		}
	}
	return commands
}

func commandFromArgs(args string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &obj) != nil {
		return ""
	}
	for _, key := range []string{"command", "cmd", "script"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s != "" {
				return s
			}
			continue
		}
		var parts []string
		if json.Unmarshal(raw, &parts) == nil {
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
			continue
		}
		var objs []struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &objs) == nil {
			var cmds []string
			for _, o := range objs {
				if o.Command != "" {
					cmds = append(cmds, o.Command)
				}
			}
			if len(cmds) > 0 {
				return strings.Join(cmds, " ")
			}
		}
	}
	return ""
}

// classify picks a filter kind from the originating command first and falls
// back to sniffing the output format.
func classify(command, content string) outputKind {
	if k := classifyCommand(command); k != kindGeneric {
		return k
	}
	return classifyContent(content)
}

func classifyCommand(command string) outputKind {
	if strings.TrimSpace(command) == "" {
		return kindGeneric
	}
	for _, segment := range commandSegments(command) {
		fields := strings.Fields(segment)
		for len(fields) > 0 && isWrapperWord(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		switch filepath.Base(fields[0]) {
		case "git":
			args := fields[1:]
			switch gitSubcommand(args) {
			case "diff":
				return kindDiff
			case "status":
				return kindStatus
			case "show":
				return kindShow
			case "log":
				if hasAnyFlag(args, "-p", "--patch") {
					return kindShow
				}
				return kindLog
			}
		case "bash", "sh", "zsh", "dash":
			if inner := shellCommand(fields[1:]); inner != "" {
				if k := classifyCommand(inner); k != kindGeneric {
					return k
				}
			}
		case "rg", "grep", "egrep", "fgrep":
			return kindGrep
		case "ls", "tree", "find", "fd":
			return kindList
		case "go":
			if hasAnyWord(fields[1:], "test") {
				return kindTest
			}
			if hasAnyWord(fields[1:], "build", "vet") {
				return kindLint
			}
		case "cargo":
			if hasAnyWord(fields[1:], "test", "nextest") {
				return kindTest
			}
			if hasAnyWord(fields[1:], "check", "clippy", "build") {
				return kindLint
			}
		case "npm", "pnpm", "yarn", "bun":
			if hasAnyWord(fields[1:], "test", "jest", "vitest") {
				return kindTest
			}
			if hasAnyWord(fields[1:], "lint", "tsc") {
				return kindLint
			}
		case "jest", "vitest", "pytest", "playwright", "tox":
			return kindTest
		case "tsc", "eslint", "golangci-lint", "staticcheck", "mypy", "ruff", "pylint", "rubocop":
			return kindLint
		}
	}
	return kindGeneric
}

var commandSeparatorRE = regexp.MustCompile(`\s*(?:&&|\|\||;|\||\n)\s*`)

func commandSegments(command string) []string {
	return commandSeparatorRE.Split(command, -1)
}

func isWrapperWord(word string) bool {
	if word == "sudo" || word == "command" || word == "env" || word == "time" {
		return true
	}
	return strings.Contains(word, "=") && !strings.HasPrefix(word, "-")
}

func shellCommand(args []string) string {
	for i, arg := range args {
		if arg == "-c" || arg == "-lc" || (strings.HasPrefix(arg, "-") && strings.Contains(arg, "c")) {
			if i+1 < len(args) {
				return strings.Trim(strings.Join(args[i+1:], " "), `"'`)
			}
		}
	}
	return ""
}

func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if arg == "-C" || arg == "-c" || arg == "--git-dir" || arg == "--work-tree" {
				i++
			}
			continue
		}
		return arg
	}
	return ""
}

func hasAnyWord(fields []string, words ...string) bool {
	for _, f := range fields {
		for _, w := range words {
			if f == w {
				return true
			}
		}
	}
	return false
}

func hasAnyFlag(fields []string, flags ...string) bool {
	return hasAnyWord(fields, flags...)
}

var (
	commitHeaderRE = regexp.MustCompile(`(?m)^commit [0-9a-f]{7,40}$`)
	grepLineRE     = regexp.MustCompile(`(?m)^[^\s:]+:\d+:`)
	lintLineRE     = regexp.MustCompile(`(?m)^[^\s:][^:]*:\d+:\d+:\s*(error|warning)`)
)

func classifyContent(content string) outputKind {
	head := content
	if len(head) > 4096 {
		head = head[:4096]
	}
	switch {
	case strings.HasPrefix(content, "diff --git "), strings.Contains(head, "\ndiff --git "):
		return kindDiff
	case strings.HasPrefix(content, "On branch "), strings.Contains(head, "\nOn branch "),
		strings.HasPrefix(content, "Changes to be committed:"),
		strings.HasPrefix(content, "Changes not staged for commit:"),
		strings.HasPrefix(content, "## "):
		return kindStatus
	}
	if commitHeaderRE.MatchString(head) {
		return kindLog
	}
	if strings.Contains(content, "--- FAIL") || strings.Contains(content, "test result: ok") ||
		strings.Contains(content, "test result: FAILED") || strings.Contains(content, "=== FAILURES ===") ||
		strings.Contains(content, "--- PASS") || strings.Contains(content, "Tests:") ||
		strings.HasPrefix(content, "FAILED ") {
		return kindTest
	}
	if grepLineRE.MatchString(head) {
		return kindGrep
	}
	if lintLineRE.MatchString(head) {
		return kindLint
	}
	return kindGeneric
}
