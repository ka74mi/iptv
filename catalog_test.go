package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ka74mi/iptv/api"
)

func cat(n int) *api.Catalog {
	return api.NewCatalog(make([]api.ServiceInfo, n), nil)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRunCatalogRetriesThenRefreshes(t *testing.T) {
	catalogRetryMin, catalogRetryMax = time.Millisecond, 4*time.Millisecond
	defer func() { catalogRetryMin, catalogRetryMax = time.Second, 30*time.Second }()

	var calls atomic.Int32
	load := func(_ context.Context, prev *api.Catalog) (*api.Catalog, error) {
		n := calls.Add(1)
		switch {
		case n <= 3: // 初回取得: EDCB未起動を模して3回失敗
			if prev != nil {
				t.Errorf("初回取得に prev が渡された")
			}
			return nil, errors.New("connection refused")
		case n == 4: // 初回成功
			return cat(1), nil
		case n == 5: // 再取得: 失敗しても直前の内容を維持する
			if prev == nil || len(prev.Services) != 1 {
				t.Errorf("再取得に直前の内容が渡されていない: %v", prev)
			}
			return nil, errors.New("temporary failure")
		default: // 以降は成功（内容が変わる）
			return cat(2), nil
		}
	}

	store := &catalogStore{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runCatalog(ctx, store, load, 5*time.Millisecond); close(done) }()

	waitFor(t, "initial load", func() bool { return store.get() != nil })
	if n := len(store.get().Services); n != 1 && n != 2 {
		t.Fatalf("services = %d", n)
	}
	waitFor(t, "refresh", func() bool { c := store.get(); return c != nil && len(c.Services) == 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCatalog did not return after cancel")
	}
}

// 初回取得のリトライ中でも、ctxキャンセルで止まる。
func TestRunCatalogStopsDuringInitialRetry(t *testing.T) {
	catalogRetryMin, catalogRetryMax = 10*time.Second, 10*time.Second // 待機中にキャンセルさせる
	defer func() { catalogRetryMin, catalogRetryMax = time.Second, 30*time.Second }()

	store := &catalogStore{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runCatalog(ctx, store, func(context.Context, *api.Catalog) (*api.Catalog, error) { return nil, errors.New("down") }, 0)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCatalog did not stop")
	}
	if store.get() != nil {
		t.Fatal("store should be empty")
	}
}
