package antigravity

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"seavxrouter/internal/auth"
)

func TestEngineQuotaTracking(t *testing.T) {
	a := &auth.Auth{Email: "a@example.com", ProjectID: "proj-1", Tier: "free", Expiry: time.Now().Add(time.Hour)}
	e := &Engine{auths: []*auth.Auth{a}, DefaultModel: "gemini-2.5-flash"}

	q := e.Quota()
	if q.Provider != "antigravity" || q.DefaultModel != "gemini-2.5-flash" || len(q.Accounts) != 1 {
		t.Fatalf("quota = %+v", q)
	}
	ac := q.Accounts[0]
	if ac.Email != "a@example.com" || ac.ProjectID != "proj-1" || ac.Status != "ready" || ac.AccessTokenExpiresAt == nil {
		t.Fatalf("account = %+v", ac)
	}

	e.trackQuota(a, errors.New("429 quota exceeded"))
	if q = e.Quota(); q.Accounts[0].Status != "limited" || q.Accounts[0].LimitedSince == nil {
		t.Fatalf("expected limited account, got %+v", q.Accounts[0])
	}

	e.trackQuota(a, nil)
	if q = e.Quota(); q.Accounts[0].Status != "ready" || q.Accounts[0].LimitedSince != nil {
		t.Fatalf("expected ready account, got %+v", q.Accounts[0])
	}
}

func TestEngineLiveQuotaWindows(t *testing.T) {
	reset := time.Date(2026, 11, 14, 2, 28, 18, 0, time.UTC)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:retrieveUserQuotaSummary" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q", got)
		}
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{
				{
					"displayName": "Gemini Models",
					"buckets": []map[string]any{
						{"bucketId": "gemini-weekly", "displayName": "Weekly Limit Remaining", "window": "weekly", "remainingFraction": 0.0059594098, "resetTime": "2026-11-14T02:28:18Z"},
						{"bucketId": "gemini-5h", "displayName": "Five Hour Limit Remaining", "window": "5h", "remainingFraction": 1, "resetTime": "2026-11-12T10:25:07Z"},
					},
				},
				{
					"displayName": "Claude and GPT models",
					"buckets": []map[string]any{
						{"bucketId": "3p-weekly", "displayName": "Weekly Limit Remaining", "window": "weekly", "remainingFraction": 0.9996896, "resetTime": "2026-11-18T06:43:46Z"},
						{"displayName": "no-fraction", "window": "5h"},
					},
				},
			},
		})
	}))
	defer srv.Close()

	a := &auth.Auth{Email: "a@example.com", ProjectID: "proj-1", AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}
	e, err := NewEngine(auth.AuthStore{}, srv.URL, []*auth.Auth{a})
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = srv.Client()

	windows := e.Quota().Accounts[0].Windows
	if len(windows) != 3 {
		t.Fatalf("windows = %+v", windows)
	}
	w := windows[0]
	if w.Group != "Gemini Models" || w.Name != "Weekly Limit Remaining" || w.WindowMinutes != 10080 {
		t.Fatalf("first window = %+v", w)
	}
	if w.RemainingPercent == nil || *w.RemainingPercent != 0.6 {
		t.Fatalf("first remaining = %v, want 0.6", w.RemainingPercent)
	}
	if w.ResetsAt == nil || !w.ResetsAt.Equal(reset) {
		t.Fatalf("first resets_at = %v, want %v", w.ResetsAt, reset)
	}
	w = windows[1]
	if w.Group != "Gemini Models" || w.Name != "Five Hour Limit Remaining" || w.WindowMinutes != 300 {
		t.Fatalf("second window = %+v", w)
	}
	if w.RemainingPercent == nil || *w.RemainingPercent != 100 {
		t.Fatalf("second remaining = %v, want 100", w.RemainingPercent)
	}
	w = windows[2]
	if w.Group != "Claude and GPT models" || w.WindowMinutes != 10080 {
		t.Fatalf("third window = %+v", w)
	}
	if w.RemainingPercent == nil || *w.RemainingPercent != 99.97 {
		t.Fatalf("third remaining = %v, want 99.97", w.RemainingPercent)
	}

	if calls.Load() != 1 {
		t.Fatalf("quota calls = %d, want 1", calls.Load())
	}
	_ = e.Quota()
	if calls.Load() != 1 {
		t.Fatalf("cached quota should not refetch, calls = %d", calls.Load())
	}
}
