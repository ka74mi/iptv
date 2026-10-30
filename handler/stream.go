package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ka74mi/iptv/api"
)

const (
	// ストリームごとに割り当てるNetworkTV IDの範囲 [nwtvIDBase, nwtvIDBase+nwtvIDRange)。
	// 同時視聴中のストリーム同士でIDが衝突しないようにする（衝突するとEDCBが
	// 同じチューナーを共有してチャンネルを切り替え合う）。
	nwtvIDBase  = 100
	nwtvIDRange = 1 << 20

	// 書き込みが進まない（クライアントが詰まった）状態を検知するまでの時間。
	writeStallTimeout = 15 * time.Second
)

// StreamPattern はストリームのルーティングパターン。
// "GET" パターンは HEAD にもマッチし、それ以外のメソッドはServeMuxが405を返す。
const StreamPattern = "GET /stream/{onid}/{tsid}/{sid}"

// deadlineWriter は書き込みのたびにWrite deadlineを更新するラッパー。
// クライアントが受信を止めた場合（TCPウィンドウ0）にWriteが永久にブロックして
// チューナーを握り続けるのを防ぐ。
type deadlineWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	// 非対応のResponseWriterではエラーになるが、その場合はdeadlineなしで続行する。
	_ = d.rc.SetWriteDeadline(time.Now().Add(writeStallTimeout))
	return d.w.Write(p)
}

// StreamHandler はHTTPリクエストを受けてEDCBからTSストリームを転送するハンドラ。
//
// URLパス形式: /stream/{onid}/{tsid}/{sid}
//
// 【EDCBストリーム配信の全体シーケンス】
//  1. SendNwTVIDSetCh()             - ストリーム専用のNetworkTV IDでチューナー起動（TCP送信）、
//     EpgDataCap_BonのプロセスIDを取得
//  2. OpenViewStreamWithRetry()     - SrvPipeストリームのTCP接続確立（リトライ込み）
//  3. syncAndSkipTS()               - 先頭50パケットを読み捨て過渡期をスキップ
//  4. tsreadex | w                  - TSデータをtsreadex経由でHTTPクライアントに転送
//  5. conn.Close()                  - SrvPipe接続を先に切断
//  6. SendNwTVIDClose()             - チューナー終了（必ずconn.Closeの後）
//
// 【受け付けるリクエスト】
//   - 配信対象は M3U に載せるサービス（デジタルTV）のみ。未知のサービスは404。
//     サービス一覧の初回取得前は503。一覧は定期的に再取得され、その都度反映される。
//   - HEAD はチューナーを起動せずヘッダーだけ返す（プローブ用）。
//   - 同時ストリーム数は maxStreams まで。超過した場合は503（Retry-After付き）。
func StreamHandler(client *api.Client, catalog CatalogFunc, maxStreams int) http.HandlerFunc {
	var seq atomic.Uint32

	// 同時ストリーム数の上限（チューナー占有・EDCB負荷への対策）。
	sem := make(chan struct{}, maxStreams)

	return func(w http.ResponseWriter, r *http.Request) {
		onid, ok1 := parseUint16(r.PathValue("onid"))
		tsid, ok2 := parseUint16(r.PathValue("tsid"))
		sid, ok3 := parseUint16(r.PathValue("sid"))
		if !ok1 || !ok2 || !ok3 {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		cat := catalog()
		if cat == nil {
			NotReady(w)
			return
		}
		if !cat.IsTVService(onid, tsid, sid) {
			http.NotFound(w, r)
			return
		}

		// HEAD はチューナーを起動しない。起動するとボディが捨てられるだけで
		// ハンドラが走り続け、チューナーを占有してしまう。
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "video/mp2t")
			w.WriteHeader(http.StatusOK)
			return
		}

		select {
		case sem <- struct{}{}:
			// 解放は最後に実行されるよう、他のdeferより先に登録する
			// （チューナーを閉じてから枠を空ける）。
			defer func() { <-sem }()
		default:
			slog.Warn("stream rejected: too many streams", "limit", maxStreams, "onid", onid, "tsid", tsid, "sid", sid, "remote", r.RemoteAddr)
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many streams", http.StatusServiceUnavailable)
			return
		}

		// ストリームごとにユニークなNetworkTV IDを割り当てる。
		nwtvID := int32(nwtvIDBase + seq.Add(1)%nwtvIDRange)

		// このストリームのログには共通の属性を付ける。パスはデコード済みの生文字列ではなく、
		// パース後の数値で記録する（ログインジェクション対策）。
		lg := slog.With("nwtv_id", nwtvID, "onid", onid, "tsid", tsid, "sid", sid, "remote", r.RemoteAddr)
		lg.Info("stream request")

		// クライアントの切断やサーバー停止（ctxのキャンセル）で、EDCBへの問い合わせと
		// SrvPipeへの接続リトライを直ちに打ち切る。
		ctx := r.Context()

		// チューナーを起動してプロセスIDを取得する。
		// 戻り値はEpgDataCap_BonのプロセスID（NetworkTV IDではない）。
		// チューナーに空きがない場合などもここで失敗するため503を返す。
		processID, err := client.SendNwTVIDSetCh(ctx, nwtvID, onid, tsid, sid)
		if err != nil {
			lg.Warn("SendNwTVIDSetCh failed", "err", err)
			if ctx.Err() != nil {
				// 応答を待つ間に中断されたため、EDCB側ではチューナーが起動済みかもしれない。
				// 念のため閉じる（未起動ならEDCBがエラーを返すだけなので無視する）。
				_ = client.SendNwTVIDClose(context.WithoutCancel(ctx), nwtvID)
				return
			}
			http.Error(w, "tuner unavailable", http.StatusServiceUnavailable)
			return
		}
		lg = lg.With("process_id", processID)
		lg.Debug("tuner started")

		// クリーンアップのdefer順序は重要。
		// deferは後入れ先出しなので、先にSendNwTVIDCloseをdeferし、
		// 後にconn.Closeをdeferすることで:
		//   実行順: conn.Close → SendNwTVIDClose
		// SrvPipeを切断してからチューナー終了コマンドを送ることで
		// EDCBが正常にチューナーを解放できる。
		// OpenViewStreamに失敗した場合もこのdeferでチューナーを解放する
		// （IDはストリーム専用なので他の視聴には影響しない）。
		//
		// 【重要】チューナーは切断・停止で ctx がキャンセルされた後こそ閉じる必要があるため、
		// キャンセルを引き継がない WithoutCancel の ctx を使う（タイムアウトはClient側にある）。
		defer func() {
			if err := client.SendNwTVIDClose(context.WithoutCancel(ctx), nwtvID); err != nil {
				lg.Warn("SendNwTVIDClose failed", "err", err)
			} else {
				lg.Debug("tuner closed")
			}
		}()

		// SrvPipeストリームに接続する（最大10秒リトライ）。
		// リトライロジックの詳細は OpenViewStreamWithRetry のコメントを参照。
		conn, err := client.OpenViewStreamWithRetry(ctx, processID, 10*time.Second)
		if err != nil {
			lg.Warn("OpenViewStream failed", "err", err)
			if ctx.Err() == nil {
				http.Error(w, "stream unavailable", http.StatusBadGateway)
			}
			return
		}
		defer conn.Close()

		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			// 切り捨てるパケットの数(50)
			// 後始末（接続やパイプのクローズ）による終了は異常ではないので記録しない。
			if err := syncAndSkipTS(conn, pw, 50); err != nil &&
				!errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
				lg.Warn("TS stream from EDCB ended", "err", err)
			}
		}()

		// tsreadexでTSストリームを整形してクライアントに転送する。
		//
		// オプションの意図:
		//   -n {sid}  サービスID指定でサービスを選択しPIDを固定する。
		//             -a/-b/-c/-u はこのフィルタが有効なときのみ機能する。
		//   -a 13     1+4+8: 第1音声補完 + モノラル→ステレオ変換 + デュアルモノを第1/第2に分離
		//   -b 0      第2音声はそのまま（補完なし）
		//   -c 1      字幕ストリームを補完（存在すれば通す）
		//   -u 2      文字スーパーは削除
		//   -         stdinから読む（パイプ入力）
		//
		// 【終了処理】
		// Runはハンドラ内で同期的に実行する。クライアント切断時はCommandContextが
		// tsreadexを終了させるため、ハンドラ復帰後にResponseWriterへ書き込まれることはない。
		cmd := exec.CommandContext(ctx, "tsreadex",
			"-n", strconv.Itoa(int(sid)),
			"-a", "13",
			"-b", "0",
			"-c", "1",
			"-u", "2",
			"-",
		)
		cmd.Stdin = pr
		cmd.Stdout = &deadlineWriter{w: w, rc: http.NewResponseController(w)}
		cmd.Stderr = os.Stderr
		cmd.WaitDelay = 5 * time.Second

		err = cmd.Run()
		// tsreadexが先に終了しても、読み手側を閉じて上流goroutineのpw.Writeを必ず解除する。
		// （閉じないと pw.Write で永久にブロックしてgoroutineがリークする）
		pr.CloseWithError(io.ErrClosedPipe)

		switch {
		case ctx.Err() != nil:
			lg.Info("stream stopped", "reason", "client disconnected or server shutdown")
		case err != nil:
			lg.Warn("stream ended unexpectedly", "err", err)
		default:
			lg.Info("stream ended")
		}
	}
}

// syncAndSkipTS はTSストリームの先頭を0x47同期バイトで揃え、
// skipPackets枚のパケットを読み捨ててからwに転送する。
// チューナー起動直後の過渡期（PMT未確定・PCR未揃い）をスキップする目的。
//
// 【読み捨て量】
// 呼び出し側で指定する（現在は50パケット ≒ 9.4KB）。チューナーコールドスタート時の
// 不安定期をカバーする保守的な値。症状が解消しない場合は増やして調整する。
func syncAndSkipTS(r io.Reader, w io.Writer, skipPackets int) error {
	const packetSize = 188
	const syncByte = 0x47

	buf := make([]byte, 1)
	// 0x47を探して188バイト境界に同期する
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		if buf[0] == syncByte {
			break
		}
	}

	// 0x47の残り187バイト + 残り(skipPackets-1)パケット分を読み捨て
	skip := make([]byte, (packetSize-1)+(packetSize*(skipPackets-1)))
	if _, err := io.ReadFull(r, skip); err != nil {
		return err
	}

	// 以降はそのままwに転送
	_, err := io.Copy(w, r)
	return err
}

func parseUint16(s string) (uint16, bool) {
	v, err := strconv.ParseUint(s, 10, 16)
	return uint16(v), err == nil
}
