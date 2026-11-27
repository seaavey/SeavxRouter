package auth

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// BuiltinProviders lists the providers the binary wires on its own. A custom
// provider may not reuse one of these names.
var BuiltinProviders = []string{
	"antigravity", "codex", "codebuddy", "qoder", "kiro", "verboo",
	"nvidia", "groq", "cloudflare", "opencode",
}

// customProviderNameRE constrains a custom provider's model prefix: it becomes
// the left side of "<name>/<model>", so it stays lowercase and separator-free.
var customProviderNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// reservedProviderNames are unroutable because they spell a routing keyword.
var reservedProviderNames = map[string]bool{"cf": true, "auto": true, "default": true, "combo": true}

// IsBuiltinProvider reports whether name is one of the providers the binary
// configures itself.
func IsBuiltinProvider(name string) bool {
	for _, p := range BuiltinProviders {
		if p == name {
			return true
		}
	}
	return false
}

// NormalizeCustomProviderName validates a user-supplied provider name and
// returns its canonical lowercase form.
func NormalizeCustomProviderName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if !customProviderNameRE.MatchString(name) {
		return "", fmt.Errorf("provider name %q must start with a letter and use only a-z, 0-9, _ or - (max 32 chars)", raw)
	}
	if IsBuiltinProvider(name) {
		return "", fmt.Errorf("provider name %q is built in; use: seavxrouter login %s", name, name)
	}
	if reservedProviderNames[name] {
		return "", fmt.Errorf("provider name %q is reserved", name)
	}
	return name, nil
}

// NormalizeCustomBaseURL validates an upstream API root: scheme, host, no
// query string or fragment, no trailing slash.
func NormalizeCustomBaseURL(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid base URL %q: want http(s)://host[/path]", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid base URL %q: remove the query string and fragment", raw)
	}
	return base, nil
}

// NormalizeSDK canonicalizes the wire protocol selector. Only the two
// surfaces the router implements are accepted.
func NormalizeSDK(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "openai":
		return "openai", nil
	case "anthropic":
		return "anthropic", nil
	}
	return "", fmt.Errorf("unsupported SDK %q: want openai or anthropic", raw)
}
