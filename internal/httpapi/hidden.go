package httpapi

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/backend"
	"seavxrouter/internal/completions"
)

// hiddenModelsFile returns the path of the hidden-model list, next to the
// credentials file so one config directory holds all operator state.
func hiddenModelsFile(authPath string) string {
	dir := filepath.Dir(authPath)
	if authPath == "" {
		dir = filepath.Dir(defaultAuthPath())
	}
	return filepath.Join(dir, "hidden-models.txt")
}

func defaultAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "auth.jsonc"
	}
	return filepath.Join(home, ".seavx", "auth.jsonc")
}

// HiddenModels is an operator-maintained blocklist of model IDs that must not
// appear in /v1/models. Entries are literal IDs or "prefix/" wildcards; blank
// lines and #-comments are ignored. The file is re-read when its mtime changes,
// so the list can be edited without restarting the server.
type HiddenModels struct {
	path    string
	mu      sync.Mutex
	ids     map[string]bool
	prefix  []string
	modTime time.Time
	loaded  bool
}

// NewHiddenModels loads the blocklist from path. A missing file is not an
// error: it means nothing is hidden.
func NewHiddenModels(path string) *HiddenModels {
	h := &HiddenModels{path: path}
	h.reload()
	return h
}

// Path reports the backing file so handlers can surface it in errors.
func (h *HiddenModels) Path() string { return h.path }

func (h *HiddenModels) reload() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loaded = true
	h.ids = nil
	h.prefix = nil

	info, err := os.Stat(h.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return
		}
		return
	}
	h.modTime = info.ModTime()

	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	h.ids = map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, "/") {
			// "nvidia/" hides every model that provider serves.
			h.prefix = append(h.prefix, line)
			continue
		}
		h.ids[line] = true
	}
}

// refresh re-reads the file when its mtime changed.
func (h *HiddenModels) refresh() {
	if h == nil {
		return
	}
	info, err := os.Stat(h.path)
	if err != nil {
		h.mu.Lock()
		if h.ids != nil || h.prefix != nil {
			h.ids, h.prefix = nil, nil
		}
		h.mu.Unlock()
		return
	}
	h.mu.Lock()
	stale := !h.loaded || !info.ModTime().Equal(h.modTime)
	h.mu.Unlock()
	if stale {
		h.reload()
	}
}

// Hidden reports whether a model ID is in the blocklist.
func (h *HiddenModels) Hidden(id string) bool {
	if h == nil {
		return false
	}
	h.refresh()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ids[id] {
		return true
	}
	for _, p := range h.prefix {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// IDs returns the current blocklist contents (literal IDs plus prefixes) for
// diagnostics.
func (h *HiddenModels) IDs() []string {
	if h == nil {
		return nil
	}
	h.refresh()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.ids)+len(h.prefix))
	for id := range h.ids {
		out = append(out, id)
	}
	out = append(out, h.prefix...)
	return out
}

// hiddenBackend wraps a backend and filters its model catalog. Everything else
// passes through untouched, so routing, failover and quota are unaffected.
type hiddenBackend struct {
	backend.BackendAPI
	hidden *HiddenModels
}

// WithHiddenModels wraps b so hidden models disappear from Models(). Chat
// requests for a hidden model still work, so an operator can keep an ID out of
// discovery without breaking a client that already knows it.
func WithHiddenModels(b backend.BackendAPI, h *HiddenModels) backend.BackendAPI {
	if b == nil || h == nil {
		return b
	}
	return &hiddenBackend{BackendAPI: b, hidden: h}
}

func (h *hiddenBackend) Models(ctx context.Context) ([]backend.Model, error) {
	models, err := h.BackendAPI.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backend.Model, 0, len(models))
	for _, m := range models {
		if h.hidden.Hidden(m.ID) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (h *hiddenBackend) HasModel(ctx context.Context, id string) bool {
	if h.hidden.Hidden(id) {
		return false
	}
	return h.BackendAPI.HasModel(ctx, id)
}

// Provider preserves provider attribution: embedding the BackendAPI interface
// does not promote Provider, and without this the HTTP layer falls back to the
// default owner for every model.
func (h *hiddenBackend) Provider(ctx context.Context, model string) string {
	if pn, ok := h.BackendAPI.(completions.ProviderNamer); ok {
		return pn.Provider(ctx, model)
	}
	return ""
}
