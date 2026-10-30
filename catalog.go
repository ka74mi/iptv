package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ka74mi/iptv/api"
)

// 初回取得に失敗したときのリトライ間隔（指数バックオフ）。テストで差し替える。
var (
	catalogRetryMin = time.Second
	catalogRetryMax = 30 * time.Second
)

// catalogStore は現在のサービス一覧・ロゴのスナップショットを保持する。
// 差し替えは Store で丸ごと行い、読み取り側はロック不要。
type catalogStore struct {
	p atomic.Pointer[api.Catalog]
}

func (s *catalogStore) get() *api.Catalog { return s.p.Load() }

// runCatalog はサービス一覧とロゴを取得して store に格納し、以降 interval ごとに再取得する。
// ctx がキャンセルされると戻る。
//
//   - 初回: 取得できるまで指数バックオフでリトライする。HTTPサーバーは先に起動しており、
//     それまで一覧が必要なエンドポイントは503を返す（EDCBより先に起動しても落ちない）。
//   - 再取得: 失敗した場合は直前のスナップショットを維持する（次回に再挑戦）。
//     interval <= 0 の場合は再取得しない。
func runCatalog(ctx context.Context, store *catalogStore, load func(ctx context.Context, prev *api.Catalog) (*api.Catalog, error), interval time.Duration) {
	wait := catalogRetryMin
	for store.get() == nil {
		c, err := load(ctx, nil)
		if err == nil {
			store.p.Store(c)
			slog.Info("catalog loaded", "services", len(c.Services))
			break
		}
		slog.Warn("catalog load failed, will retry", "retry_in", wait.String(), "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, catalogRetryMax)
	}

	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, err := load(ctx, store.get())
		if err != nil {
			slog.Warn("catalog refresh failed, keeping previous", "err", err)
			continue
		}
		store.p.Store(c)
		slog.Info("catalog refreshed", "services", len(c.Services))
	}
}
