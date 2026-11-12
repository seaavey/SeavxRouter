package antigravity

import (
	"errors"
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
