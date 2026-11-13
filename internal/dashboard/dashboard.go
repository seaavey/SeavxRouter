// Package dashboard renders a live terminal dashboard over the router's
// SQLite state, with an optional SSE feed of finished requests.
package dashboard

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"seavxrouter/internal/httpapi"
)

const (
	// recentLimit is how many recent requests Load fetches; the renderer
	// shows as many rows as the terminal height allows.
	recentLimit = 100
	groupLimit  = 12
	dayLimit    = 14
)

var groupBys = []string{"day", "provider", "model", "user", "endpoint"}

// Options configures a Dashboard.
type Options struct {
	UsageDB string
	Refresh time.Duration
	GroupBy string
	Once    bool
	Server  string
	APIKey  string
}

// Snapshot is everything one refresh renders.
type Snapshot struct {
	Generated        time.Time
	DBPath           string
	GroupBy          string
	Today            httpapi.UsageTotals
	Week             httpapi.UsageTotals
	Total            httpapi.UsageTotals
	Groups           []httpapi.UsageGroup
	Compression      httpapi.CompressionTotals
	CompressionToday httpapi.CompressionTotals
	Recent           []httpapi.LogSummary
	Live             bool
	LiveNote         string
	Err              string
}

// Dashboard loads usage data from the router's SQLite database.
type Dashboard struct {
	opts    Options
	store   *httpapi.UsageStore
	groupBy string
}

// Open validates options and opens the usage database. The database must
// already exist: the dashboard never creates state on its own.
func Open(opts Options) (*Dashboard, error) {
	if opts.GroupBy == "" {
		opts.GroupBy = "day"
	}
	if !ValidGroupBy(opts.GroupBy) {
		return nil, fmt.Errorf("invalid group-by %q (want day, provider, model, user or endpoint)", opts.GroupBy)
	}
	if opts.Refresh <= 0 {
		opts.Refresh = 2 * time.Second
	}
	opts.Server = normalizeServer(opts.Server)
	if _, err := os.Stat(opts.UsageDB); err != nil {
		return nil, fmt.Errorf("usage db %s is not available: %w (start seavxrouter with usage tracking enabled or pass -usage-db)", opts.UsageDB, err)
	}
	store, err := httpapi.OpenUsageStore(opts.UsageDB)
	if err != nil {
		return nil, err
	}
	return &Dashboard{opts: opts, store: store, groupBy: opts.GroupBy}, nil
}

func (d *Dashboard) Close() error { return d.store.Close() }

// ValidGroupBy reports whether g is a supported usage grouping.
func ValidGroupBy(g string) bool {
	return slices.Contains(groupBys, g)
}

// SetGroupBy switches the usage grouping and reports whether g is valid.
func (d *Dashboard) SetGroupBy(g string) bool {
	if !ValidGroupBy(g) {
		return false
	}
	d.groupBy = g
	return true
}

func (d *Dashboard) GroupBy() string { return d.groupBy }

// Load reads a consistent snapshot from the usage database.
func (d *Dashboard) Load() (Snapshot, error) {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekStart := dayStart.AddDate(0, 0, -6)

	snap := Snapshot{
		Generated: now,
		DBPath:    d.opts.UsageDB,
		GroupBy:   d.groupBy,
		LiveNote:  "db poll",
	}
	var err error
	if snap.Today, _, err = d.store.Query(httpapi.UsageFilter{Start: &dayStart, GroupBy: "day"}); err != nil {
		return Snapshot{}, fmt.Errorf("querying today's usage: %w", err)
	}
	if snap.Week, _, err = d.store.Query(httpapi.UsageFilter{Start: &weekStart, GroupBy: "day"}); err != nil {
		return Snapshot{}, fmt.Errorf("querying weekly usage: %w", err)
	}
	var groups []httpapi.UsageGroup
	if snap.Total, groups, err = d.store.Query(httpapi.UsageFilter{GroupBy: d.groupBy}); err != nil {
		return Snapshot{}, fmt.Errorf("querying usage: %w", err)
	}
	snap.Groups = trimGroups(d.groupBy, groups)
	if snap.Compression, err = d.store.QueryCompressionTotals(nil); err != nil {
		return Snapshot{}, err
	}
	if snap.CompressionToday, err = d.store.QueryCompressionTotals(&dayStart); err != nil {
		return Snapshot{}, err
	}
	if snap.Recent, err = d.store.QueryLogSummaries(recentLimit, 0); err != nil {
		return Snapshot{}, fmt.Errorf("querying recent requests: %w", err)
	}
	return snap, nil
}

func trimGroups(groupBy string, groups []httpapi.UsageGroup) []httpapi.UsageGroup {
	out := slices.Clone(groups)
	if groupBy == "day" {
		if len(out) > dayLimit {
			out = out[len(out)-dayLimit:]
		}
		return out
	}
	slices.SortStableFunc(out, func(a, b httpapi.UsageGroup) int {
		if c := cmp.Compare(b.TotalTokens, a.TotalTokens); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	if len(out) > groupLimit {
		out = out[:groupLimit]
	}
	return out
}

func normalizeServer(raw string) string {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "", "off", "none", "disabled":
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/")
}

// prependRecent adds a live log to the recent list, dropping a duplicate row
// left behind by a poll that already saw the same request.
func prependRecent(list []httpapi.LogSummary, e httpapi.LogSummary) []httpapi.LogSummary {
	out := make([]httpapi.LogSummary, 0, len(list)+1)
	out = append(out, e)
	for _, x := range list {
		if x.ID != e.ID {
			out = append(out, x)
		}
	}
	if len(out) > recentLimit {
		out = out[:recentLimit]
	}
	return out
}
