// Package auth stores provider credentials and shared account metadata.
package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Auth is one stored provider account.
type Auth struct {
	Provider     string    `json:"provider,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	ProjectID    string    `json:"project_id,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	Tier         string    `json:"tier,omitempty"`
	Email        string    `json:"email,omitempty"`
	Region       string    `json:"region,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	ProfileARN   string    `json:"profile_arn,omitempty"`
	// BaseURL marks a custom OpenAI-compatible provider: the endpoint the
	// bearer key talks to. Empty for built-in providers.
	BaseURL string `json:"base_url,omitempty"`
}

// Label identifies the account for request logs: email when the provider
// reports one, else the upstream account ID, else a short fingerprint of the
// API key so distinct keys stay distinguishable without exposing the secret.
func (a *Auth) Label() string {
	switch {
	case a.Email != "":
		return a.Email
	case a.AccountID != "":
		return a.AccountID
	case a.AccessToken != "":
		sum := sha256.Sum256([]byte(a.AccessToken))
		return "key:" + hex.EncodeToString(sum[:])[:8]
	}
	return ""
}

// AuthStore reads and writes the JSONC credentials file.
type AuthStore struct{ Path string }

// DefaultPath returns ~/.seavx/auth.jsonc.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "auth.jsonc"
	}
	return filepath.Join(home, ".seavx", "auth.jsonc")
}

// Load parses the credentials file, accepting both the legacy single-object
// form and the current array form.
func (s AuthStore) Load() ([]*Auth, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	clean := bytes.TrimSpace(StripJSONC(data))
	var auths []*Auth
	if len(clean) > 0 && clean[0] == '[' {
		err = json.Unmarshal(clean, &auths)
	} else {
		var a Auth
		err = json.Unmarshal(clean, &a)
		auths = []*Auth{&a}
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path, err)
	}
	for _, a := range auths {
		if a.Provider == "" {
			a.Provider = "antigravity"
		}
		// Bearer-only providers hold a long-lived API key and never refresh;
		// the rest rotate tokens through a refresh_token. Custom providers
		// carry their base URL and also authenticate with a bare key.
		if a.RefreshToken == "" && !bearerOnlyProviders[a.Provider] && a.BaseURL == "" {
			return nil, fmt.Errorf("%s: an account has no refresh_token; run login again", s.Path)
		}
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("%s contains no accounts", s.Path)
	}
	return auths, nil
}

// bearerOnlyProviders lists providers whose credential is an API key that is
// presented as-is, with no refresh cycle.
var bearerOnlyProviders = map[string]bool{
	"codebuddy":  true,
	"qoder":      true,
	"verboo":     true,
	"nvidia":     true,
	"groq":       true,
	"cloudflare": true,
	"opencode":   true,
}

var storeMu sync.Mutex

// Save merges with the accounts already on disk so an engine holding only one
// provider's accounts never drops the others.
func (s AuthStore) Save(auths []*Auth) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	var existing []*Auth
	if _, err := os.Stat(s.Path); err == nil {
		var err error
		existing, err = s.Load()
		if err != nil {
			return fmt.Errorf("refusing to overwrite unreadable %s: %w", s.Path, err)
		}
	}
	merged := make([]*Auth, 0, len(existing)+len(auths))
	for _, old := range existing {
		replacement := old
		for _, a := range auths {
			if sameAccount(old, a) {
				replacement = a
				break
			}
		}
		merged = append(merged, replacement)
	}
	for _, a := range auths {
		found := false
		for _, old := range existing {
			if sameAccount(old, a) {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, a)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return s.write(merged)
}

// write replaces the credentials file with exactly the accounts given.
func (s AuthStore) write(auths []*Auth) error {
	data, err := json.MarshalIndent(auths, "", "  ")
	if err != nil {
		return err
	}
	out := append([]byte("// SeavxRouter credentials. Keep this file private.\n"), data...)
	out = append(out, '\n')
	return os.WriteFile(s.Path, out, 0o600)
}

// RemoveProvider deletes every stored account for provider and reports how
// many entries were removed. Removing the last account deletes the file so a
// later Save starts from a clean slate.
func (s AuthStore) RemoveProvider(provider string) (int, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	all, err := s.Load()
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	kept := make([]*Auth, 0, len(all))
	for _, a := range all {
		if a.Provider != provider {
			kept = append(kept, a)
		}
	}
	removed := len(all) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	if len(kept) == 0 {
		if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		return removed, nil
	}
	return removed, s.write(kept)
}

func sameAccount(a, b *Auth) bool {
	if a.Provider != b.Provider {
		return false
	}
	if a.Email != "" && b.Email != "" {
		return strings.EqualFold(a.Email, b.Email)
	}
	// Kiro refresh tokens rotate on every use, so accounts are keyed by the
	// stable AWS subject instead.
	if a.Provider == "kiro" && a.AccountID != "" && b.AccountID != "" {
		return a.AccountID == b.AccountID
	}
	// Custom providers and the bearer-token providers below have no refresh
	// token; their API key identifies the account.
	if a.BaseURL != "" || b.BaseURL != "" || a.Provider == "codebuddy" || a.Provider == "qoder" || a.Provider == "verboo" {
		return a.AccessToken != "" && a.AccessToken == b.AccessToken
	}
	return a.RefreshToken == b.RefreshToken
}

// StripJSONC removes // and /* */ comments that are outside JSON strings.
func StripJSONC(src []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}
