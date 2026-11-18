package backend

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func waitRefreshed(t *testing.T, cat *Catalog[string]) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cat.mu.Lock()
		busy := cat.refreshing
		cat.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("refresh did not finish")
}

func TestCatalogServesStaleWithoutBlocking(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	cat := NewCatalog("test", time.Nanosecond, func(ctx context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "stale", nil
		}
		select {
		case <-started:
		default:
			close(started)
		}
		select {
		case <-release:
			return "fresh", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	got, err := cat.Get(context.Background())
	if err != nil || got != "stale" {
		t.Fatalf("first Get = %q, %v", got, err)
	}

	time.Sleep(2 * time.Millisecond) // let the entry go stale
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err = cat.Get(ctx)
	if err != nil || got != "stale" {
		t.Fatalf("stale Get = %q, %v (refresh must not block readers)", got, err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh did not start")
	}
	close(release)
	waitRefreshed(t, cat)

	if got, ok := cat.Last(); !ok || got != "fresh" {
		t.Fatalf("Last after refresh = %q, %v", got, ok)
	}
}

func TestCatalogKeepsStaleOnRefreshError(t *testing.T) {
	var calls atomic.Int32
	cat := NewCatalog("test", time.Nanosecond, func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "stale", nil
		}
		return "", errors.New("upstream down")
	})

	if got, err := cat.Get(context.Background()); err != nil || got != "stale" {
		t.Fatalf("first Get = %q, %v", got, err)
	}

	time.Sleep(2 * time.Millisecond)
	if got, err := cat.Get(context.Background()); err != nil || got != "stale" {
		t.Fatalf("stale Get = %q, %v", got, err)
	}
	waitRefreshed(t, cat)
	if calls.Load() != 2 {
		t.Fatalf("fetch calls = %d, want 2", calls.Load())
	}
	if got, ok := cat.Last(); !ok || got != "stale" {
		t.Fatalf("Last after failed refresh = %q, %v", got, ok)
	}
}

func TestCatalogConcurrentFirstFetchRunsOnce(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	cat := NewCatalog("test", time.Hour, func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 42, nil
	})

	const n = 16
	results := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			v, err := cat.Get(context.Background())
			if err != nil {
				results <- -1
				return
			}
			results <- v
		}()
	}
	time.Sleep(20 * time.Millisecond) // let the callers pile up on the first fetch
	close(release)

	for i := 0; i < n; i++ {
		if v := <-results; v != 42 {
			t.Fatalf("Get = %d, want 42", v)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

func TestCatalogLastWithoutFetch(t *testing.T) {
	var calls atomic.Int32
	cat := NewCatalog("test", time.Hour, func(context.Context) (string, error) {
		calls.Add(1)
		return "value", nil
	})
	if _, ok := cat.Last(); ok {
		t.Fatal("Last on an empty catalog must report missing")
	}
	if calls.Load() != 0 {
		t.Fatalf("Last triggered a fetch")
	}
	if _, err := cat.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := cat.Last(); !ok || got != "value" {
		t.Fatalf("Last = %q, %v", got, ok)
	}
}
