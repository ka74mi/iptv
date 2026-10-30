package api

import (
	"testing"
	"time"
)

// EDCBはJSTの壁時計時刻をUTCとみなしたFILETIMEで範囲比較する。
func TestTimeToFileTimeUsesJSTWallClock(t *testing.T) {
	const epochDiff = 116444736000000000
	// 2026-10-28 12:00:00 JST（= 03:00:00 UTC）は、12:00:00 UTC のFILETIMEと一致するべき
	in := time.Date(2026, 10, 28, 12, 0, 0, 0, jst)
	want := uint64(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).Unix())*10_000_000 + epochDiff
	if got := timeToFileTime(in); got != want {
		t.Fatalf("timeToFileTime(%v) = %d, want %d", in, got, want)
	}
	// 同じ瞬間をUTCで渡しても同じ値になる
	if got := timeToFileTime(in.UTC()); got != want {
		t.Fatalf("timeToFileTime(UTC) = %d, want %d", got, want)
	}
}
