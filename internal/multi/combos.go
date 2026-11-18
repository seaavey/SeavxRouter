package multi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"seavxrouter/internal/auth"
)

// ComboPrefix marks a model ID that names a combo: an ordered list of models
// tried across providers until one answers.
const ComboPrefix = "combo/"

// ComboOwner is the owned_by reported for combo IDs: a combo is assembled by
// the router, not served by any single upstream provider.
const ComboOwner = "seavx"

// Combos is the operator's combo table. It re-reads combos.jsonc when the file
// changes, so edits apply to a running server without a restart.
type Combos struct {
	path    string
	mu      sync.Mutex
	entries map[string][]string
	modTime int64
	loaded  bool
}

// NewCombos loads the combo table from path. A missing or unreadable file
// leaves the table empty; the server still serves provider models.
func NewCombos(path string) *Combos {
	c := &Combos{path: path}
	c.reload()
	return c
}

// Path reports the backing file.
func (c *Combos) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Get returns the ordered members of one combo.
func (c *Combos) Get(name string) ([]string, bool) {
	if c == nil {
		return nil, false
	}
	c.refresh()
	c.mu.Lock()
	defer c.mu.Unlock()
	members := c.entries[name]
	if len(members) == 0 {
		return nil, false
	}
	return append([]string(nil), members...), true
}

// Names lists the defined combo names in sorted order.
func (c *Combos) Names() []string {
	if c == nil {
		return nil
	}
	c.refresh()
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.entries))
	for name, members := range c.entries {
		if len(members) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// refresh reloads when the file's mtime changed.
func (c *Combos) refresh() {
	if c.path == "" {
		return
	}
	info, err := os.Stat(c.path)
	if err != nil {
		c.mu.Lock()
		wasLoaded := c.loaded
		c.loaded = true
		if wasLoaded {
			c.entries = nil
			c.modTime = 0
		}
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	stale := !c.loaded || info.ModTime().UnixNano() != c.modTime
	c.mu.Unlock()
	if stale {
		c.reload()
	}
}

func (c *Combos) reload() {
	if c.path == "" {
		return
	}
	// Stat and read through one handle: taking the mtime after the read from a
	// separate os.Stat lets an atomic replace slip in between, pairing old
	// contents with a new mtime so refresh treats the stale table as current.
	f, err := os.Open(c.path)
	if err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.loaded = true
		if errors.Is(err, os.ErrNotExist) {
			c.entries = nil
			c.modTime = 0
		}
		return
	}
	info, statErr := f.Stat()
	data, readErr := io.ReadAll(f)
	f.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = true
	if readErr != nil || statErr != nil {
		return
	}
	entries, err := parseCombos(data)
	if err != nil {
		// Keep the last good table: a half-written edit must not silently
		// disable every combo.
		slog.Warn("cannot parse combos file, keeping previous combos", "path", c.path, "err", err)
		return
	}
	c.entries = entries
	c.modTime = info.ModTime().UnixNano()
}

func parseCombos(data []byte) (map[string][]string, error) {
	clean := bytes.TrimSpace(auth.StripJSONC(data))
	if len(clean) == 0 {
		return map[string][]string{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(clean, &raw); err != nil {
		return nil, fmt.Errorf("want an object of name -> [model IDs]: %w", err)
	}
	out := make(map[string][]string, len(raw))
	for name, value := range raw {
		var members []string
		if err := json.Unmarshal(value, &members); err != nil {
			return nil, fmt.Errorf("combo %q must be an array of model IDs: %w", name, err)
		}
		cleaned := make([]string, 0, len(members))
		for _, member := range members {
			if member = strings.TrimSpace(member); member != "" {
				cleaned = append(cleaned, member)
			}
		}
		if len(cleaned) > 0 {
			out[name] = cleaned
		}
	}
	return out, nil
}

// LoadCombosFile reads a combos.jsonc file. A missing file is an empty table.
func LoadCombosFile(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading combos: %w", err)
	}
	entries, err := parseCombos(data)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return entries, nil
}

// SaveCombosFile replaces path with the given combos. The write is atomic so a
// running server never reads a half-written file.
func SaveCombosFile(path string, entries map[string][]string) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	out := append([]byte("// SeavxRouter model combos. Each combo is an ordered fallback list.\n"), data...)
	out = append(out, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".combos-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
