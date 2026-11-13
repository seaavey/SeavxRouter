package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// liveQuotaTTL bounds how often GET /v1/quota queries the usage endpoint.
const liveQuotaTTL = time.Minute

// rateLimitSnapshot is the newest rate-limit state reported for an account,
// either in response headers or by the usage endpoint (the same data the Codex
// CLI renders as "5h limit" and "Weekly limit").
type rateLimitSnapshot struct {
	windows []auth.QuotaWindow
	at      time.Time
}

// recordRateLimits parses x-codex-primary-* / x-codex-secondary-* response
// headers and stores the newest snapshot for account a.
func (e *CodexEngine) recordRateLimits(a *auth.Auth, h http.Header) {
	windows := codexRateLimitWindows(h)
	if len(windows) == 0 {
		return
	}
	e.quotaMu.Lock()
	defer e.quotaMu.Unlock()
	if e.rateLimits == nil {
		e.rateLimits = map[*auth.Auth]*rateLimitSnapshot{}
	}
	e.rateLimits[a] = &rateLimitSnapshot{windows: windows, at: time.Now()}
}

func codexRateLimitWindows(h http.Header) []auth.QuotaWindow {
	windows := make([]auth.QuotaWindow, 0, 2)
	for _, kind := range []string{"Primary", "Secondary"} {
		if w, ok := codexWindow(h, kind); ok {
			windows = append(windows, w)
		}
	}
	return windows
}

func codexWindow(h http.Header, kind string) (auth.QuotaWindow, bool) {
	raw := h.Get("X-Codex-" + kind + "-Used-Percent")
	if raw == "" {
		return auth.QuotaWindow{}, false
	}
	used, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return auth.QuotaWindow{}, false
	}
	used = math.Max(0, math.Min(100, used))
	w := auth.QuotaWindow{Name: strings.ToLower(kind), UsedPercent: &used}
	if v := h.Get("X-Codex-" + kind + "-Window-Minutes"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil && minutes > 0 {
			w.WindowMinutes = minutes
			if label := codexWindowLabel(minutes); label != "" {
				w.Name = label
			}
		}
	}
	if v := h.Get("X-Codex-" + kind + "-Reset-At"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil && sec > 0 {
			t := time.Unix(sec, 0).UTC()
			w.ResetsAt = &t
		}
	}
	return w, true
}

func codexWindowLabel(minutes int) string {
	switch {
	case minutes == 5*60:
		return "5h"
	case minutes == 7*24*60:
		return "weekly"
	case minutes%1440 == 0:
		return fmt.Sprintf("%dd", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return ""
}

// Quota reports per-account availability for GET /v1/quota, including the
// 5-hour and weekly rate-limit windows. Fresh header snapshots are reused;
// otherwise the usage endpoint is queried (at most once per liveQuotaTTL).
func (e *CodexEngine) Quota() auth.ProviderQuota {
	auths := e.snapshotAuths()
	e.quotaMu.Lock()
	limited := make(map[*auth.Auth]time.Time, len(e.quotaErr))
	for a, t := range e.quotaErr {
		limited[a] = t
	}
	e.quotaMu.Unlock()
	var windows map[*auth.Auth][]auth.QuotaWindow
	if e.HTTP != nil {
		windows = e.liveQuotaWindows(auths)
	}
	q := auth.ProviderQuota{Provider: "codex", DefaultModel: e.DefaultModel, Accounts: make([]auth.AccountQuota, 0, len(auths))}
	for _, a := range auths {
		ac := auth.AccountStatus("codex", a, limited)
		if w := windows[a]; len(w) > 0 {
			ac.Windows = append([]auth.QuotaWindow(nil), w...)
		}
		q.Accounts = append(q.Accounts, ac)
	}
	return q
}

// liveQuotaWindows refreshes quota windows for all accounts in parallel.
func (e *CodexEngine) liveQuotaWindows(auths []*auth.Auth) map[*auth.Auth][]auth.QuotaWindow {
	out := make(map[*auth.Auth][]auth.QuotaWindow, len(auths))
	if len(auths) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range auths {
		wg.Add(1)
		go func(a *auth.Auth) {
			defer wg.Done()
			if w := e.accountQuotaWindows(ctx, a); len(w) > 0 {
				mu.Lock()
				out[a] = w
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	return out
}

func (e *CodexEngine) accountQuotaWindows(ctx context.Context, a *auth.Auth) []auth.QuotaWindow {
	e.quotaMu.Lock()
	snap := e.rateLimits[a]
	e.quotaMu.Unlock()
	if snap != nil && time.Since(snap.at) < liveQuotaTTL {
		return snap.windows
	}
	windows, err := e.fetchUsageWindows(ctx, a)
	if err != nil {
		slog.Warn("codex usage fetch failed", "email", a.Email, "err", err)
		if snap != nil {
			return snap.windows
		}
		return nil
	}
	e.quotaMu.Lock()
	if e.rateLimits == nil {
		e.rateLimits = map[*auth.Auth]*rateLimitSnapshot{}
	}
	e.rateLimits[a] = &rateLimitSnapshot{windows: windows, at: time.Now()}
	e.quotaMu.Unlock()
	return windows
}

type usageResponse struct {
	RateLimit *struct {
		PrimaryWindow   *usageWindow `json:"primary_window"`
		SecondaryWindow *usageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type usageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

func (e *CodexEngine) usageURL() string {
	if e.BaseURL != "" {
		return strings.TrimSuffix(e.BaseURL, "/") + "/wham/usage"
	}
	return "https://chatgpt.com/backend-api/wham/usage"
}

// fetchUsageWindows calls the ChatGPT usage endpoint, which reports the same
// rate-limit windows the Codex CLI shows.
func (e *CodexEngine) fetchUsageWindows(ctx context.Context, a *auth.Auth) ([]auth.QuotaWindow, error) {
	url := e.usageURL()
	for attempt := 0; ; attempt++ {
		token, err := e.token(ctx, a)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		e.setHeaders(req, token, a, false)
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			e.mu.Lock()
			a.Expiry = time.Time{}
			e.mu.Unlock()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, backend.APIHTTPError(resp)
		}
		var data usageResponse
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if data.RateLimit == nil {
			return nil, fmt.Errorf("usage response has no rate_limit")
		}
		windows := []auth.QuotaWindow{}
		if w, ok := usageQuotaWindow("primary", data.RateLimit.PrimaryWindow); ok {
			windows = append(windows, w)
		}
		if w, ok := usageQuotaWindow("secondary", data.RateLimit.SecondaryWindow); ok {
			windows = append(windows, w)
		}
		return windows, nil
	}
}

func usageQuotaWindow(kind string, w *usageWindow) (auth.QuotaWindow, bool) {
	if w == nil {
		return auth.QuotaWindow{}, false
	}
	used := math.Max(0, math.Min(100, w.UsedPercent))
	qw := auth.QuotaWindow{Name: kind, UsedPercent: &used}
	if w.LimitWindowSeconds > 0 {
		qw.WindowMinutes = int(w.LimitWindowSeconds / 60)
		if label := codexWindowLabel(qw.WindowMinutes); label != "" {
			qw.Name = label
		}
	}
	if w.ResetAt > 0 {
		t := time.Unix(w.ResetAt, 0).UTC()
		qw.ResetsAt = &t
	}
	return qw, true
}
