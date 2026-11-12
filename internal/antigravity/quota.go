package antigravity

import (
	"time"

	"seavxrouter/internal/auth"
)

// Quota reports per-account availability for GET /v1/quota.
func (e *Engine) Quota() auth.ProviderQuota {
	e.mu.Lock()
	auths := append([]*auth.Auth(nil), e.auths...)
	e.mu.Unlock()
	e.quotaMu.Lock()
	limited := make(map[*auth.Auth]time.Time, len(e.quotaErr))
	for a, t := range e.quotaErr {
		limited[a] = t
	}
	e.quotaMu.Unlock()
	q := auth.ProviderQuota{Provider: "antigravity", DefaultModel: e.DefaultModel, Accounts: make([]auth.AccountQuota, 0, len(auths))}
	for _, a := range auths {
		q.Accounts = append(q.Accounts, auth.AccountStatus("antigravity", a, limited))
	}
	return q
}
