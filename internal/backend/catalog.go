package backend

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultFetchTimeout bounds one catalog fetch. Provider engines and the
// multi-provider aggregator share it so a hung upstream can never hold a
// response open indefinitely. It is deliberately generous: catalogs are large
// (one upstream serves 2900+ entries), and a timeout that fires early drops that
// provider's models from the answer while leaving the cache cold, so every
// later request pays the fetch again.
const DefaultFetchTimeout = 20 * time.Second

// Catalog caches a provider's model list with stale-while-revalidate
// semantics: the first call fetches synchronously, later calls return the last
// known value immediately while one background refresh keeps it current. A
// failed refresh keeps the stale value and retries on a later call.
type Catalog[T any] struct {
	name  string
	ttl   time.Duration
	fetch func(context.Context) (T, error)

	mu         sync.Mutex
	val        T
	has        bool
	at         time.Time
	refreshing bool
	wait       chan struct{}
}

// NewCatalog builds a catalog. name labels refresh warnings.
func NewCatalog[T any](name string, ttl time.Duration, fetch func(context.Context) (T, error)) *Catalog[T] {
	return &Catalog[T]{name: name, ttl: ttl, fetch: fetch}
}

// Get returns the cached value, refreshing it in the background once the TTL
// expires. Concurrent first calls share one fetch.
func (c *Catalog[T]) Get(ctx context.Context) (T, error) {
	for {
		c.mu.Lock()
		if c.has {
			val := c.val
			if time.Since(c.at) >= c.ttl && !c.refreshing {
				c.refreshing = true
				go c.refresh(context.WithoutCancel(ctx))
			}
			c.mu.Unlock()
			return val, nil
		}
		if c.refreshing {
			wait := c.wait
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				var zero T
				return zero, ctx.Err()
			}
		}
		c.refreshing = true
		c.wait = make(chan struct{})
		c.mu.Unlock()

		fctx, cancel := context.WithTimeout(ctx, DefaultFetchTimeout)
		val, err := c.fetch(fctx)
		cancel()

		c.mu.Lock()
		if err == nil {
			c.val = val
			c.has = true
			c.at = time.Now()
		}
		c.refreshing = false
		if c.wait != nil {
			close(c.wait)
			c.wait = nil
		}
		c.mu.Unlock()
		return val, err
	}
}

// Last returns the cached value without triggering a fetch.
func (c *Catalog[T]) Last() (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val, c.has
}

// Invalidate drops the cached value so the next Get fetches synchronously.
func (c *Catalog[T]) Invalidate() {
	c.mu.Lock()
	var zero T
	c.val = zero
	c.has = false
	c.at = time.Time{}
	c.mu.Unlock()
}

func (c *Catalog[T]) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, DefaultFetchTimeout)
	defer cancel()
	val, err := c.fetch(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing = false
	if err != nil {
		slog.Warn("model catalog refresh failed", "provider", c.name, "err", err)
		return
	}
	c.val = val
	c.has = true
	c.at = time.Now()
}
