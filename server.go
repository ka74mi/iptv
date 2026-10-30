package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	// リクエストヘッダーの読み込み期限（Slowloris対策）。
	readHeaderTimeout = 5 * time.Second
	// keep-alive 接続の待機上限。
	idleTimeout = 60 * time.Second
	// 停止時に、配信中のストリームの後始末（チューナーの終了）を待つ上限。
	// podman stop の既定の待機時間（10秒）に収まるようにしている。
	shutdownTimeout = 8 * time.Second
)

// serve は ln で h を配信し、ctx がキャンセルされたらグレースフルに停止する。
//
// 【タイムアウト】
// /stream は長時間のレスポンスなので WriteTimeout / ReadTimeout は設定しない
// （書き込みの停滞は StreamHandler 側で検知する）。ヘッダー読み込みと
// アイドル接続のみ制限する。
//
// 【停止の流れ】
// Server.Shutdown は進行中のハンドラの終了を待つが、ストリームは自然には終わらない。
// そこで全リクエストの親コンテキスト(BaseContext)をキャンセルして、
// tsreadex を止め、defer でSrvPipeを切断しチューナーを閉じさせてから Shutdown する。
func serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	reqCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
		// net/http 内部のエラー（不正なリクエストなど）も slog の出力形式にそろえる。
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		return err // Serveが自力で失敗した場合（ErrServerClosedにはならない）
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	cancelRequests()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown failed, closing remaining connections", "err", err)
		srv.Close()
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
