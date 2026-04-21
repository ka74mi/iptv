package handler

import (
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ka74mi/iptv/api"
)

// StreamHandler はHTTPリクエストを受けてEDCBからTSストリームを転送するハンドラ。
//
// URLパス形式: /stream/{onid}/{tsid}/{sid}
//
// 【EDCBストリーム配信の全体シーケンス】
//  1. SendNwTVMode(2)               - TCP送信モードをグローバル設定
//  2. SendNwTVIDSetCh()             - チューナー起動、EpgDataCap_BonのプロセスIDを取得
//  3. OpenViewStreamWithRetry()     - SrvPipeストリームのTCP接続確立（リトライ込み）
//  4. tsreadex | w                  - TSデータをtsreadex経由でHTTPクライアントに転送
//  5. conn.Close()                  - SrvPipe接続を先に切断
//  6. SendNwTVIDClose()             - チューナー終了（必ずconn.Closeの後）
func StreamHandler(client *api.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("stream request: %s", r.URL.Path)

		onid, tsid, sid, ok := parseStreamPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		// EDCBのグローバルTCP送信モードを設定する。
		// use_bon_ch=0（デフォルト）運用ではこの設定がないと
		// EpgDataCap_Bonが -nonw フラグ付きで起動されTCP出力が無効になる。
		if err := client.SendNwTVMode(2); err != nil {
			log.Printf("SendNwTVMode error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// チューナーを起動してプロセスIDを取得する。
		// 戻り値はEpgDataCap_BonのプロセスID（NetworkTV IDではない）。
		processID, err := client.SendNwTVIDSetCh(onid, tsid, sid)
		log.Printf("SendNwTVIDSetCh: processID=%d err=%v", processID, err)
		if err != nil {
			log.Printf("SendNwTVIDSetCh error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// SrvPipeストリームに接続する（最大10秒リトライ）。
		// リトライロジックの詳細は OpenViewStreamWithRetry のコメントを参照。
		conn, err := client.OpenViewStreamWithRetry(processID, 10*time.Second)
		if err != nil {
			log.Printf("OpenViewStreamWithRetry error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// クリーンアップのdefer順序は重要。
		// deferは後入れ先出しなので、先にSendNwTVIDCloseをdeferし、
		// 後にconn.Closeをdeferすることで:
		//   実行順: conn.Close → SendNwTVIDClose
		// SrvPipeを切断してからチューナー終了コマンドを送ることで
		// EDCBが正常にチューナーを解放できる。
		//
		// 【SendNwTVIDCloseに渡す値】
		// use_bon_ch=0, space_or_id=0 の運用では 0 を渡す。
		// EpgDataCap_BonのプロセスIDを渡してはいけない。
		defer func() {
			if err := client.SendNwTVIDClose(0); err != nil {
				log.Printf("SendNwTVIDClose error: %v", err)
			} else {
				log.Printf("SendNwTVIDClose success")
			}
		}()
		defer conn.Close()

		ctx := r.Context()
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		done := make(chan struct{})
		go func() {
			defer close(done)

			// tsreadexでTSストリームを整形してクライアントに転送する。
			//
			// オプションの意図:
			//   -n {sid}  サービスID指定でサービスを選択しPIDを固定する。
			//             -a/-b/-c/-u はこのフィルタが有効なときのみ機能する。
			//   -a 13     1+4+8: 第1音声補完 + モノラル→ステレオ変換 + デュアルモノを第1/第2に分離
			//   -b 3      第2音声がなければ第1音声をコピー（デュアルモノ分離後の副音声保持）
			//   -c 1      字幕ストリームを補完（存在すれば通す）
			//   -u 2      文字スーパーは削除
			//   -         stdinから読む（パイプ入力）
			cmd := exec.CommandContext(ctx, "tsreadex",
				"-n", strconv.Itoa(int(sid)),
				"-a", "13",
				"-b", "0",
				"-c", "1",
				"-u", "2",
				"-",
			)
			cmd.Stdin = conn
			cmd.Stdout = w
			cmd.Stderr = nil

			if err := cmd.Run(); err != nil {
				log.Printf("tsreadex error: %v", err)
			}
		}()

		select {
		case <-ctx.Done():
			// クライアントが切断した場合。
			// conn.Closeでtsreadexのstdinを閉じてプロセスを終了させる。
			// deferのconn.Closeと二重になるが問題ない。
			log.Printf("client disconnected: %s", r.URL.Path)
			conn.Close()
		case <-done:
			log.Printf("stream ended: %s", r.URL.Path)
		}
	}
}

// parseStreamPath は "/stream/{onid}/{tsid}/{sid}" 形式のパスを解析する。
// strings.FieldsFunc を使いループ内アロケーションを排除している。
func parseStreamPath(path string) (onid, tsid, sid uint16, ok bool) {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	// parts[0]="stream", parts[1]=onid, parts[2]=tsid, parts[3]=sid
	if len(parts) < 4 {
		return 0, 0, 0, false
	}
	o, err1 := strconv.ParseUint(parts[1], 10, 16)
	t, err2 := strconv.ParseUint(parts[2], 10, 16)
	s, err3 := strconv.ParseUint(parts[3], 10, 16)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return uint16(o), uint16(t), uint16(s), true
}
