package api

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"
)

type Client struct {
	host    string
	port    int
	timeout time.Duration
}

func NewClient(host string, port int) *Client {
	return &Client{
		host:    host,
		port:    port,
		timeout: 15 * time.Second,
	}
}

const (
	cmdSuccess      = 1
	maxResponseSize = 64 * 1024 * 1024 // 64MB
	maxVectorCount  = 100_000
)

const (
	cmdEnumService     = 1021
	cmdEnumPgInfoEx    = 1029
	cmdNwTVIDSetCh     = 1073 // NetworkTVチャンネル切り替え（ID指定）
	cmdNwTVIDClose     = 1074 // NetworkTV終了（ID指定）
	cmdFileCopy2       = 2060
	cmdRelayViewStream = 301 // ViewアプリのSrvPipeストリームをTCP転送
)

// connect はEDCBにTCP接続し、c.timeout 後を期限とするdeadlineを設定して返す。
//
// ctx がキャンセルされると、実行中の読み書きはdeadlineを現在時刻に引き上げて直ちに中断される。
// 戻り値の stop は、接続を使い終えたら（ストリームとして返す場合は引き渡す前に）呼ぶ。
// stop が false を返した場合は、すでに ctx のキャンセルで中断されている。
func (c *Client) connect(ctx context.Context) (conn net.Conn, stop func() bool, err error) {
	d := net.Dialer{Timeout: c.timeout}
	conn, err = d.DialContext(ctx, "tcp", net.JoinHostPort(c.host, strconv.Itoa(c.port)))
	if err != nil {
		return nil, nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	stop = context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	return conn, stop, nil
}

// ctxErr は、ctx のキャンセル・期限切れが原因のI/Oエラーを ctx のエラーに置き換える。
// 呼び出し側は errors.Is(err, context.Canceled) などで判別できる。
func ctxErr(ctx context.Context, err error) error {
	if ce := ctx.Err(); ce != nil {
		return ce
	}
	return err
}

// encodeRequest はリクエストを [cmd:4LE][payloadSize:4LE][payload...] に組み立てる。
func encodeRequest(cmd uint32, payload []byte) []byte {
	buf := make([]byte, 0, 8+len(payload))
	buf = binary.LittleEndian.AppendUint32(buf, cmd)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(payload)))
	return append(buf, payload...)
}

// readResponseHeader はレスポンスヘッダー [ret:4LE][payloadSize:4LE] を読み、
// ret が成功でない、またはサイズが異常に大きい場合はエラーを返す。
func readResponseHeader(conn net.Conn) (size uint32, err error) {
	var header [8]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return 0, err
	}
	ret := binary.LittleEndian.Uint32(header[0:])
	size = binary.LittleEndian.Uint32(header[4:])
	if ret != cmdSuccess {
		return 0, fmt.Errorf("EDCB returned error: %d", ret)
	}
	// 異常に大きいレスポンスサイズでのOOMを防ぐ
	if size > maxResponseSize {
		return 0, fmt.Errorf("response too large: %d bytes", size)
	}
	return size, nil
}

// sendCmd はEDCB TCP APIに1コマンドを送信してレスポンスを返す。
//
// EDCBのCtrlCmdプロトコルは「1コマンド = 1TCPコネクション」の使い捨て設計。
// 同一コネクションでコマンドを連続送信することはできない。
//
// リクエスト形式: [cmd:4LE][payloadSize:4LE][payload...]
// レスポンス形式: [ret:4LE][payloadSize:4LE][payload...]
//
//	ret=1(CMD_SUCCESS) のみ成功。それ以外はエラー。
//	ret=0 はパラメータ不正・対象が見つからない等を意味する。
//
// ctx がキャンセルされた場合は、待たずに ctx のエラー（context.Canceled など）で戻る。
func (c *Client) sendCmd(ctx context.Context, cmd uint32, payload []byte) ([]byte, error) {
	conn, stop, err := c.connect(ctx)
	if err != nil {
		return nil, ctxErr(ctx, err)
	}
	defer conn.Close()
	defer stop()

	if _, err := conn.Write(encodeRequest(cmd, payload)); err != nil {
		return nil, ctxErr(ctx, err)
	}
	size, err := readResponseHeader(conn)
	if err != nil {
		return nil, ctxErr(ctx, err)
	}
	rbuf := make([]byte, size)
	if _, err := io.ReadFull(conn, rbuf); err != nil {
		return nil, ctxErr(ctx, err)
	}
	return rbuf, nil
}

// EnumService はEDCBが把握しているサービス（チャンネル）の一覧を返す。
func (c *Client) EnumService(ctx context.Context) ([]ServiceInfo, error) {
	rbuf, err := c.sendCmd(ctx, cmdEnumService, nil)
	if err != nil {
		return nil, err
	}
	return parseServiceList(rbuf)
}

// EnumPgInfoEx は start〜end の間に開始する番組をサービスごとに返す。
//
// EDCBは番組の「開始時刻」で範囲を絞る点に注意（放送中の番組を含めるには
// start を過去に遡らせる）。
func (c *Client) EnumPgInfoEx(ctx context.Context, start, end time.Time) ([]ServiceEventInfo, error) {
	// 検索キー4要素: [onid/tsid/sidのマスク][同]（0xffffffffffff = 全サービス対象）、
	// [開始時刻の始点][終点]（FILETIME）。
	keys := make([]byte, 0, 32)
	keys = binary.LittleEndian.AppendUint64(keys, 0xffffffffffff)
	keys = binary.LittleEndian.AppendUint64(keys, 0xffffffffffff)
	keys = binary.LittleEndian.AppendUint64(keys, timeToFileTime(start))
	keys = binary.LittleEndian.AppendUint64(keys, timeToFileTime(end))

	// vector<LONGLONG>: [size:4（自身とcountを含む）][count:4][要素...]
	payload := make([]byte, 0, 8+len(keys))
	payload = binary.LittleEndian.AppendUint32(payload, uint32(8+len(keys)))
	payload = binary.LittleEndian.AppendUint32(payload, 4)
	payload = append(payload, keys...)

	rbuf, err := c.sendCmd(ctx, cmdEnumPgInfoEx, payload)
	if err != nil {
		return nil, err
	}
	return parseProgramList(rbuf)
}

// SendNwTVIDSetCh はNetworkTVモードでチューナーを起動しチャンネルを設定する。
// 戻り値はEpgDataCap_BonのプロセスID（OpenViewStreamに使用する）。
//
// nwtvID にはストリームごとにユニークなNetworkTV IDを渡す。
// use_bon_ch=1 を指定すると space_or_id がNetworkTV ID、ch_or_mode が送信モード
// （1:UDP 2:TCP 3:UDP+TCP）として扱われるため、EDCBのグローバル設定（SendNwTVMode）に
// 依存せずリクエスト単位でTCP送信を指定できる（KonomiTVと同じ方式）。
//
// 【IDを固定しない理由】
// EDCBは同じNetworkTV IDで起動中のチューナーがあるとそれを再利用してチャンネルだけを
// 切り替える（ReserveManager::OpenNWTV）。IDを共有すると、同時視聴で互いの
// チャンネルを切り替え合い、片方のCloseでもう片方のチューナーも閉じてしまう。
//
// 【注意】SetChが成功してプロセスIDが返っても、EpgDataCap_Bonが
// BonDriverを初期化してSrvPipeにデータを流し始めるまでタイムラグがある。
// OpenViewStreamはリトライが必須。
//
// 【NetworkTV IDとプロセスIDの混同に注意】
//   - SetChの戻り値 = EpgDataCap_BonのプロセスID → OpenViewStreamに使う
//   - CloseNWTVに渡す値 = nwtvID（SetChに渡したNetworkTV ID）
func (c *Client) SendNwTVIDSetCh(ctx context.Context, nwtvID int32, onid, tsid, sid uint16) (int32, error) {
	w := newWriter()
	pos := len(w.buf)
	w.writeInt(0) // サイズ仮置き（後で書き換え）
	w.writeInt(1) // use_sid=1（onid/tsid/sidで指定）
	w.writeUshort(onid)
	w.writeUshort(tsid)
	w.writeUshort(sid)
	w.writeInt(1)      // use_bon_ch=1（ID・送信モードを個別指定）
	w.writeInt(nwtvID) // space_or_id=NetworkTV ID
	w.writeInt(2)      // ch_or_mode=2（TCP）
	binary.LittleEndian.PutUint32(w.buf[pos:], uint32(len(w.buf)-pos))

	rbuf, err := c.sendCmd(ctx, cmdNwTVIDSetCh, w.buf)
	if err != nil {
		return 0, err
	}
	r := newReader(rbuf)
	processID := r.i32() // EpgDataCap_BonのプロセスID
	return processID, r.err
}

// SendNwTVIDClose はNetworkTV IDを指定してチューナーを終了する。
//
// 【重要】nwtvID には SendNwTVIDSetCh に渡したNetworkTV IDを使う。
// EpgDataCap_BonのプロセスIDを渡しても対象が見つからずエラーになる。
//
// 【正しいクリーンアップ順序】
//  1. conn.Close()（SrvPipeのTCPコネクションを先に切断）
//  2. SendNwTVIDClose（その後でチューナー終了コマンドを送る）
//
// SrvPipeを開いたままCloseNWTVを送ると正常終了できない場合がある。
// deferの後入れ先出し特性を利用して、conn.Closeより先にdeferすること。
//
// 【ctx の注意】リクエストのctxがキャンセルされた後（クライアント切断・停止時）でも
// チューナーは必ず閉じる必要があるため、呼び出し側は context.WithoutCancel(ctx) を渡すこと。
func (c *Client) SendNwTVIDClose(ctx context.Context, nwtvID int32) error {
	w := newWriter()
	w.writeInt(nwtvID)
	_, err := c.sendCmd(ctx, cmdNwTVIDClose, w.buf)
	return err
}

// OpenViewStreamWithRetry はSrvPipeストリームへの接続をリトライ付きで試みる。
//
// SetCh成功直後はEpgDataCap_BonがFIFOを準備中のため即座には成功しない。
// 0.1秒から徐々に伸ばしながら最大 timeout まで待つ（KonomiTV方式）。
//
// ctx がキャンセルされた場合は、待たずに ctx のエラーで戻る。
// 失敗した場合、呼び出し側でSendNwTVIDCloseしてチューナーを解放すること
// （NetworkTV IDはストリームごとにユニークなので他の視聴には影響しない）。
func (c *Client) OpenViewStreamWithRetry(ctx context.Context, processID int32, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	wait := 100 * time.Millisecond
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := c.openViewStream(ctx, processID)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err() // クライアントの切断・停止。リトライしない
		}
		slog.Debug("OpenViewStream not ready, retrying", "process_id", processID, "err", err)
		lastErr = err

		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		wait = min(wait+100*time.Millisecond, time.Second)
	}
	return nil, fmt.Errorf("OpenViewStream timed out after %s: %w", timeout, lastErr)
}

// openViewStream はEpgDataCap_BonのSrvPipeストリームをTCP経由で受信するための
// コネクションを開く。成功するとそのコネクションにTSデータが流れ続ける。
//
// 【仕組み】EDCBのCMD2_EPG_SRV_RELAY_VIEW_STREAMコマンドを送ると、
// EDCBサーバー側がSrvPipe（Linux上ではFIFOファイル SendTSTCP_*_{pid}_?.fifo）を
// 開いてそのデータをこのTCPコネクションに転送し続ける（CMD_NO_RES_THREAD方式）。
// GoはFIFOを直接読む必要はなく、このコネクションを読むだけでよい。
//
// 【コマンド送信形式】通常のsendCmdと異なり独自フォーマット:
// [cmd:4LE][payloadSize:4LE][processID:4LE]
// レスポンスヘッダー [ret:4LE][size:4LE] を受信後、
// sizeバイトを読み捨てた後にそのままストリームデータが流れてくる。
func (c *Client) openViewStream(ctx context.Context, processID int32) (net.Conn, error) {
	conn, stop, err := c.connect(ctx)
	if err != nil {
		return nil, ctxErr(ctx, err)
	}
	fail := func(err error) (net.Conn, error) {
		stop()
		conn.Close()
		return nil, ctxErr(ctx, err)
	}

	w := newWriter()
	w.writeInt(processID)
	if _, err := conn.Write(encodeRequest(cmdRelayViewStream, w.buf)); err != nil {
		return fail(err)
	}
	size, err := readResponseHeader(conn)
	if err != nil {
		return fail(fmt.Errorf("OpenViewStream failed: %w", err))
	}
	// レスポンスペイロードを読み捨て、以降はストリームデータ
	if _, err := io.CopyN(io.Discard, conn, int64(size)); err != nil {
		return fail(err)
	}

	// 以降は呼び出し側が接続を所有する。ctx のキャンセルによる中断を解除してから
	// deadlineをリセットし、ストリーミング用に開放する（停止はConnのCloseで行う）。
	if !stop() {
		conn.Close()
		return nil, ctx.Err() // 引き渡す直前にキャンセルされた
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// SendFileCopy2 は複数ファイルをまとめて転送する。
// name に "LogoData\\*.*" を指定するとディレクトリ内ファイルリストが取得できる。
//
// 結果は names と同じ数・同じ順で返り、存在しないファイルは Data が空になる。
func (c *Client) SendFileCopy2(ctx context.Context, names []string) ([]FileData, error) {
	w := newWriter()
	w.writeUshort(5) // CMD_VER
	// vector<string>
	pos := len(w.buf)
	w.writeInt(0) // サイズ仮置き
	w.writeInt(int32(len(names)))
	for _, name := range names {
		w.writeString(name)
	}
	binary.LittleEndian.PutUint32(w.buf[pos:], uint32(len(w.buf)-pos))

	rbuf, err := c.sendCmd(ctx, cmdFileCopy2, w.buf)
	if err != nil {
		return nil, err
	}
	return parseFileCopy2(rbuf)
}
