package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

type Result struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
	Error          string `json:"error"`
	NumTurns       int    `json:"num_turns"`
	Usage          Usage  `json:"usage"`
}

type StepUpdate struct {
	StepIndex int    `json:"step_index"`
	State     string `json:"state"`
	StepType  string `json:"step_type"`
	TextDelta string `json:"text_delta"`
	Usage     *Usage `json:"usage"`
}

type Event struct {
	Event      string      `json:"event"`
	StepUpdate *StepUpdate `json:"step_update"`
	Result     *Result     `json:"result"`
}

type Model struct {
	ID   string
	Name string
}

type Backend struct {
	Bin          string
	WorkRoot     string
	Timeout      time.Duration
	DefaultModel string

	mu         sync.Mutex
	models     []Model
	modelsAt   time.Time
	modelsFail error
}

// Linux caps a single argv string at 128 KiB (MAX_ARG_STRLEN).
const maxPromptBytes = 120 * 1024

func (b *Backend) Models(ctx context.Context) ([]Model, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.models != nil && time.Since(b.modelsAt) < 5*time.Minute {
		return b.models, nil
	}
	out, err := b.command(ctx, nil, "models").Output()
	if err != nil {
		b.modelsFail = err
		return nil, fmt.Errorf("agy models: %w", err)
	}
	var models []Model
	for _, line := range strings.Split(string(out), "\n") {
		id, name, ok := strings.Cut(line, "\t")
		if !ok || id == "" || strings.ContainsAny(id, " \t") {
			continue
		}
		models = append(models, Model{ID: id, Name: name})
	}
	if len(models) == 0 {
		b.modelsFail = fmt.Errorf("no models parsed from agy output")
		return nil, b.modelsFail
	}
	b.models = models
	b.modelsAt = time.Now()
	b.modelsFail = nil
	return models, nil
}

func (b *Backend) HasModel(ctx context.Context, id string) bool {
	models, err := b.Models(ctx)
	if err != nil {
		return true // can't verify; let agy decide
	}
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (b *Backend) command(ctx context.Context, dir *string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.Bin, args...)
	if dir != nil {
		cmd.Dir = *dir
	}
	cmd.Env = os.Environ()
	return cmd
}

func (b *Backend) Default() string { return b.DefaultModel }

// Complete runs one headless turn and returns the final result.
func (b *Backend) Complete(ctx context.Context, model, prompt string) (*Result, error) {
	return b.run(ctx, model, prompt, false, nil)
}

// Stream runs one headless turn, calling onDelta for each assistant text chunk.
func (b *Backend) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*Result, error) {
	return b.run(ctx, model, prompt, true, onDelta)
}

func (b *Backend) run(ctx context.Context, model, prompt string, stream bool, onDelta func(string)) (*Result, error) {
	if len(prompt) > maxPromptBytes {
		return nil, fmt.Errorf("prompt too long: %d bytes (limit %d)", len(prompt), maxPromptBytes)
	}
	if model == "" {
		model = b.DefaultModel
	}

	workDir, err := os.MkdirTemp(b.WorkRoot, "req-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)

	format := "json"
	if stream {
		format = "stream-json"
	}
	args := []string{
		"-p=" + prompt,
		"--output-format", format,
		"--disable-slash-commands",
		"--print-timeout", b.Timeout.String(),
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := b.command(ctx, &workDir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var result *Result
	if stream {
		reader := bufio.NewReaderSize(stdout, 1024*1024)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var ev Event
				if json.Unmarshal(line, &ev) == nil {
					switch ev.Event {
					case "step_update":
						if ev.StepUpdate != nil && ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.TextDelta != "" && onDelta != nil {
							onDelta(ev.StepUpdate.TextDelta)
						}
					case "result":
						result = ev.Result
					}
				}
			}
			if err != nil {
				break
			}
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(stdout, 32*1024*1024))
		if err != nil {
			return nil, err
		}
		var r Result
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("bad agy json output: %w", err)
		}
		result = &r
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if result == nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if waitErr != nil {
			return nil, fmt.Errorf("agy exited: %v: %s", waitErr, msg)
		}
		return nil, fmt.Errorf("agy produced no result: %s", msg)
	}
	if result.Status != "SUCCESS" {
		detail := result.Error
		if detail == "" {
			detail = "status " + result.Status
		}
		return result, fmt.Errorf("agy: %s", detail)
	}
	return result, nil
}

func ResolveBinary(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		candidate := filepath.Join(home, ".local", "bin", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("binary %q not found on PATH or ~/.local/bin", name)
}
