package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"seavxrouter/internal/backend"
	"sort"
	"strings"
	"sync"
	"time"
)

type ModelManager interface {
	ProviderModels(context.Context, string) ([]backend.Model, error)
	AddModel(context.Context, string, string) error
	DeleteModel(context.Context, string, string) error
}
type ModelProbe func(context.Context, string, string) error
type ProviderModelManager struct {
	Base    ModelManager
	Path    string
	Probe   ModelProbe
	mu      sync.Mutex
	entries map[string]map[string]string
}

func NewProviderModelManager(base ModelManager, path string, probe ModelProbe) (*ProviderModelManager, error) {
	m := &ProviderModelManager{Base: base, Path: path, Probe: probe, entries: map[string]map[string]string{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}
func (m *ProviderModelManager) load() error {
	b, e := os.ReadFile(m.Path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	return json.Unmarshal(b, &m.entries)
}
func (m *ProviderModelManager) save() error {
	if e := os.MkdirAll(filepath.Dir(m.Path), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(m.entries)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(m.Path), ".models-*")
	if e != nil {
		return e
	}
	n := f.Name()
	defer os.Remove(n)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(n, m.Path)
}
func (m *ProviderModelManager) ProviderModels(ctx context.Context, p string, base []backend.Model) ([]backend.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entries[p]
	out := make([]backend.Model, 0, len(base))
	seen := map[string]bool{}
	for _, v := range base {
		if s[v.ID] != "disabled" {
			out = append(out, v)
			seen[v.ID] = true
		}
	}
	for id, state := range s {
		if state == "enabled" && !seen[id] {
			out = append(out, backend.Model{ID: id, Name: id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *ProviderModelManager) Enabled(p, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[p][id] == "enabled"
}

func (m *ProviderModelManager) Disabled(p, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[p][id] == "disabled"
}
func (m *ProviderModelManager) AddModel(ctx context.Context, p, id string) error {
	if strings.TrimSpace(p) == "" || strings.TrimSpace(p) != p || strings.ContainsAny(p, "/\\") || strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n") {
		return fmt.Errorf("invalid model ID")
	}
	if m.Probe == nil {
		return fmt.Errorf("verification unsupported")
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if e := m.Probe(c, p, id); e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, hadEntry := m.entries[p][id]
	if m.entries[p] == nil {
		m.entries[p] = map[string]string{}
	}
	m.entries[p][id] = "enabled"
	if err := m.save(); err != nil {
		if hadEntry {
			m.entries[p][id] = previous
		} else {
			delete(m.entries[p], id)
		}
		return err
	}
	return nil
}
func (m *ProviderModelManager) DeleteModel(ctx context.Context, p, id string) error {
	base := []backend.Model{}
	if m.Base != nil {
		var err error
		base, err = m.Base.ProviderModels(ctx, p)
		if err != nil {
			return err
		}
	}
	found := false
	for _, v := range base {
		if v.ID == id {
			found = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !found && m.entries[p][id] != "enabled" {
		return fmt.Errorf("model not found")
	}
	previous, hadEntry := m.entries[p][id]
	if m.entries[p] == nil {
		m.entries[p] = map[string]string{}
	}
	m.entries[p][id] = "disabled"
	if e := m.save(); e != nil {
		if hadEntry {
			m.entries[p][id] = previous
		} else {
			delete(m.entries[p], id)
		}
		return e
	}
	return nil
}
func StrictProbe(ctx context.Context, endpoint, model string, client *http.Client) error {
	return strictProbe(ctx, endpoint, "", model, client)
}
func StrictProbeWithAuth(ctx context.Context, endpoint, key, model string, client *http.Client) error {
	return strictProbe(ctx, endpoint, key, model, client)
}
func strictProbe(ctx context.Context, endpoint, key, model string, client *http.Client) error {
	b, _ := json.Marshal(map[string]any{"model": model, "messages": []any{map[string]string{"role": "user", "content": "Reply with OK"}}, "max_tokens": 2, "stream": false})
	r, e := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(b)))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, e := client.Do(r)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream HTTP %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if e = json.NewDecoder(resp.Body).Decode(&out); e != nil {
		return fmt.Errorf("invalid chat response: %w", e)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return fmt.Errorf("chat response contained no result")
	}
	return nil
}
