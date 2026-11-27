package auth

import "time"

// QuotaWindow is one usage window reported by a provider: the Codex 5-hour
// and weekly rate limits, or an Antigravity quota bucket. Antigravity buckets
// are grouped the way its CLI shows them ("Gemini Models", "Claude and GPT
// models"), with Group carrying the group name. Percent fields are 0-100;
// providers that report consumed quota set UsedPercent, providers that report
// leftover quota set RemainingPercent.
type QuotaWindow struct {
	Group            string     `json:"group,omitempty"`
	Name             string     `json:"name,omitempty"`
	WindowMinutes    int        `json:"window_minutes,omitempty"`
	UsedPercent      *float64   `json:"used_percent,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	ResetsAt         *time.Time `json:"resets_at,omitempty"`
}

// AccountQuota is the public status of one backend account.
type AccountQuota struct {
	Provider             string        `json:"provider"`
	Email                string        `json:"email,omitempty"`
	AccountID            string        `json:"account_id,omitempty"`
	ProjectID            string        `json:"project_id,omitempty"`
	Tier                 string        `json:"tier,omitempty"`
	AccessTokenExpiresAt *time.Time    `json:"access_token_expires_at,omitempty"`
	Status               string        `json:"status"`
	LimitedSince         *time.Time    `json:"limited_since,omitempty"`
	Windows              []QuotaWindow `json:"windows,omitempty"`
}

// ProviderQuota groups account status per provider for GET /v1/quota.
type ProviderQuota struct {
	Provider     string         `json:"provider"`
	BaseURL      string         `json:"base_url,omitempty"`
	SDK          string         `json:"sdk,omitempty"`
	DefaultModel string         `json:"default_model,omitempty"`
	Accounts     []AccountQuota `json:"accounts"`
}

// AccountStatus renders one account's quota state, given the set of accounts
// that recently hit a quota error and when.
func AccountStatus(provider string, a *Auth, limited map[*Auth]time.Time) AccountQuota {
	q := AccountQuota{
		Provider:  provider,
		Email:     a.Email,
		AccountID: a.AccountID,
		ProjectID: a.ProjectID,
		Tier:      a.Tier,
		Status:    "ready",
	}
	if !a.Expiry.IsZero() {
		exp := a.Expiry
		q.AccessTokenExpiresAt = &exp
	}
	if t, ok := limited[a]; ok {
		q.Status = "limited"
		q.LimitedSince = &t
	}
	return q
}
