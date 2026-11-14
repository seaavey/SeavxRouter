package kiro

import (
	"time"

	"seavxrouter/internal/auth"
)

// Quota reports per-account availability for GET /v1/quota.
func (e *Engine) Quota() auth.ProviderQuota {
	auths := e.snapshotAuths()
	e.quotaMu.Lock()
	limited := make(map[*auth.Auth]time.Time, len(e.quotaErr))
	for a, t := range e.quotaErr {
		limited[a] = t
	}
	e.quotaMu.Unlock()
	q := auth.ProviderQuota{Provider: "kiro", DefaultModel: e.Default(), Accounts: make([]auth.AccountQuota, 0, len(auths))}
	for _, a := range auths {
		q.Accounts = append(q.Accounts, auth.AccountStatus("kiro", a, limited))
	}
	return q
}
