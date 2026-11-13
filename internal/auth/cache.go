package auth

import (
	"os"
	"time"
)

// AccountCache follows one provider's accounts in the credentials file so
// accounts added or removed while the server runs (for example through
// "seavxrouter login") take effect without a restart. Refresh must be called
// with the owning engine's lock held: the returned list may reuse *Auth
// pointers that engine quota state is keyed by.
type AccountCache struct {
	store    AuthStore
	provider string

	modTime time.Time
	size    int64
}

// NewAccountCache prepares a cache for one provider. The first Refresh always
// reloads, so accounts written between the initial store.Load and engine
// construction are not lost.
func NewAccountCache(store AuthStore, provider string) AccountCache {
	return AccountCache{store: store, provider: provider}
}

// Refresh returns the provider's accounts as currently stored on disk. Entries
// whose credentials match a current entry keep its *Auth pointer; entries with
// rotated or replaced credentials come from disk; added accounts are appended
// and deleted ones dropped. Unreadable or unchanged files leave current
// untouched.
func (c *AccountCache) Refresh(current []*Auth) []*Auth {
	st, err := os.Stat(c.store.Path)
	if err != nil || (st.ModTime().Equal(c.modTime) && st.Size() == c.size) {
		return current
	}
	all, err := c.store.Load()
	if err != nil {
		return current
	}
	c.modTime, c.size = st.ModTime(), st.Size()
	fresh := make([]*Auth, 0, len(current))
	for _, a := range all {
		if a.Provider == c.provider {
			fresh = append(fresh, a)
		}
	}
	if len(fresh) == 0 {
		return current
	}
	next := make([]*Auth, 0, len(fresh))
	for _, f := range fresh {
		merged := f
		for _, cur := range current {
			if sameAccount(cur, f) && sameCredentials(cur, f) {
				merged = cur
				break
			}
		}
		next = append(next, merged)
	}
	return next
}

// sameCredentials reports whether two entries for the same account carry
// identical tokens, so in-memory state keyed by the current pointer stays
// valid. Expiry uses Equal so a monotonic reading does not count as a change.
func sameCredentials(a, b *Auth) bool {
	return a.AccessToken == b.AccessToken &&
		a.RefreshToken == b.RefreshToken &&
		a.AccountID == b.AccountID &&
		a.ProjectID == b.ProjectID &&
		a.Expiry.Equal(b.Expiry)
}
