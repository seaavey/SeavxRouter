package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// liveQuotaTTL bounds how often GET /v1/quota queries upstream quota buckets.
const liveQuotaTTL = time.Minute

type liveQuota struct {
	windows []auth.QuotaWindow
	at      time.Time
}

// Quota reports per-account availability for GET /v1/quota, including the
// grouped quota windows the Antigravity CLI shows in /usage.
func (e *Engine) Quota() auth.ProviderQuota {
	auths := e.snapshotAuths()
	e.quotaMu.Lock()
	limited := make(map[*auth.Auth]time.Time, len(e.quotaErr))
	for a, t := range e.quotaErr {
		limited[a] = t
	}
	e.quotaMu.Unlock()
	var windows map[*auth.Auth][]auth.QuotaWindow
	if e.BaseURL != "" && e.HTTP != nil {
		windows = e.liveQuotaWindows(auths)
	}
	q := auth.ProviderQuota{Provider: "antigravity", DefaultModel: e.DefaultModel, Accounts: make([]auth.AccountQuota, 0, len(auths))}
	for _, a := range auths {
		ac := auth.AccountStatus("antigravity", a, limited)
		ac.Windows = windows[a]
		q.Accounts = append(q.Accounts, ac)
	}
	return q
}

// liveQuotaWindows refreshes cached quota buckets for all accounts in parallel.
func (e *Engine) liveQuotaWindows(auths []*auth.Auth) map[*auth.Auth][]auth.QuotaWindow {
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

func (e *Engine) accountQuotaWindows(ctx context.Context, a *auth.Auth) []auth.QuotaWindow {
	e.quotaMu.Lock()
	snap, cached := e.quotaLive[a]
	e.quotaMu.Unlock()
	if cached && time.Since(snap.at) < liveQuotaTTL {
		return snap.windows
	}
	windows, err := e.fetchQuotaWindows(ctx, a)
	if err != nil {
		slog.Warn("antigravity quota fetch failed", "email", a.Email, "err", err)
		return snap.windows
	}
	e.quotaMu.Lock()
	if e.quotaLive == nil {
		e.quotaLive = map[*auth.Auth]liveQuota{}
	}
	e.quotaLive[a] = liveQuota{windows: windows, at: time.Now()}
	e.quotaMu.Unlock()
	return windows
}

type quotaSummary struct {
	Groups []quotaGroup `json:"groups"`
}

// quotaGroup mirrors one entry of retrieveUserQuotaSummary: the same grouping
// agy /usage renders ("Gemini Models", "Claude and GPT models").
type quotaGroup struct {
	DisplayName string               `json:"displayName"`
	Buckets     []quotaSummaryBucket `json:"buckets"`
}

type quotaSummaryBucket struct {
	DisplayName       string   `json:"displayName"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

// fetchQuotaWindows calls retrieveUserQuotaSummary and converts its grouped
// buckets into quota windows. Buckets without a remaining fraction carry no
// usage data and are skipped.
func (e *Engine) fetchQuotaWindows(ctx context.Context, a *auth.Auth) ([]auth.QuotaWindow, error) {
	body, err := json.Marshal(map[string]any{"project": a.ProjectID})
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		token, err := e.token(ctx, a)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1internal:retrieveUserQuotaSummary", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", agyUserAgent)
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			e.invalidate(a)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, backend.APIHTTPError(resp)
		}
		var data quotaSummary
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		return quotaWindowsFromGroups(data.Groups), nil
	}
}

func quotaWindowsFromGroups(groups []quotaGroup) []auth.QuotaWindow {
	windows := []auth.QuotaWindow{}
	for _, g := range groups {
		for _, b := range g.Buckets {
			if b.RemainingFraction == nil {
				continue
			}
			remaining := math.Round(*b.RemainingFraction*10000) / 100
			remaining = math.Max(0, math.Min(100, remaining))
			w := auth.QuotaWindow{
				Group:            g.DisplayName,
				Name:             b.DisplayName,
				WindowMinutes:    quotaWindowMinutes(b.Window),
				RemainingPercent: &remaining,
			}
			if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil && !t.IsZero() {
				w.ResetsAt = &t
			}
			windows = append(windows, w)
		}
	}
	return windows
}

func quotaWindowMinutes(window string) int {
	switch window {
	case "5h":
		return 300
	case "weekly":
		return 10080
	}
	return 0
}
