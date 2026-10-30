package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// newLogger は LOG_LEVEL / LOG_FORMAT の指定からロガーを作る。
//
//	level:  debug | info | warn | error（大文字小文字は問わない）
//	format: text（key=value 形式、既定）| json（ログ収集基盤向け）
func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid LOG_LEVEL %q (use debug, info, warn or error)", level)
	}
	opts := &slog.HandlerOptions{Level: lv}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("invalid LOG_FORMAT %q (use text or json)", format)
	}
}

// fatal はエラーを記録してプロセスを終了する（設定の誤りなど、起動を続けられない場合に使う）。
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
