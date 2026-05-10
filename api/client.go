package api

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"
	"unicode/utf16"
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
func (c *Client) sendCmd(cmd uint32, payload []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.host, strconv.Itoa(c.port)), c.timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, err
	}

	size := uint32(len(payload))
	buf := make([]byte, 8+size)
	binary.LittleEndian.PutUint32(buf[0:], cmd)
	binary.LittleEndian.PutUint32(buf[4:], size)
	copy(buf[8:], payload)

	if _, err := conn.Write(buf); err != nil {
		return nil, err
	}

	header := make([]byte, 8)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	ret := binary.LittleEndian.Uint32(header[0:])
	rsize := binary.LittleEndian.Uint32(header[4:])
	if ret != cmdSuccess {
		return nil, fmt.Errorf("EDCB returned error: %d", ret)
	}
	// 異常に大きいレスポンスサイズでのOOMを防ぐ
	if rsize > maxResponseSize {
		return nil, fmt.Errorf("response too large: %d bytes", rsize)
	}

	rbuf := make([]byte, rsize)
	if _, err := io.ReadFull(conn, rbuf); err != nil {
		return nil, err
	}
	return rbuf, nil
}

const (
	cmdSuccess      = 1
	maxResponseSize = 64 * 1024 * 1024 // 64MB
	maxVectorCount  = 100_000
)

const (
	cmdEnumService     = 1021
	cmdEnumPgInfoEx    = 1029
	cmdNwTVMode        = 1072 // NetworkTVモード送信設定（1:UDP 2:TCP 3:UDP+TCP）
	cmdNwTVIDSetCh     = 1073 // NetworkTVチャンネル切り替え（ID指定）
	cmdNwTVIDClose     = 1074 // NetworkTV終了（ID指定）
	cmdRelayViewStream = 301  // ViewアプリのSrvPipeストリームをTCP転送
)

const (
	cmdFileCopy  = 1060
	cmdFileCopy2 = 2060
)

// FILETIME (1601年からの100ナノ秒) → time.Time
func fileTimeToTime(ft uint64) time.Time {
	const epochDiff = 116444736000000000
	if ft < epochDiff {
		return time.Unix(0, 0)
	}
	nsec := int64((ft - epochDiff) * 100)
	return time.Unix(0, nsec).In(jst)
}

// time.Time → FILETIME
func timeToFileTime(t time.Time) uint64 {
	const epochDiff = 116444736000000000
	return uint64(t.UnixNano()/100) + epochDiff
}

var jst = time.FixedZone("JST", 9*60*60)

type reader struct {
	buf []byte
	pos int
}

func newReader(buf []byte) *reader {
	return &reader{buf: buf}
}

func (r *reader) readByte() (uint8, error) {
	if r.pos+1 > len(r.buf) {
		return 0, fmt.Errorf("readByte: out of range")
	}
	v := r.buf[r.pos]
	r.pos++
	return v, nil
}

func (r *reader) readUshort() (uint16, error) {
	if r.pos+2 > len(r.buf) {
		return 0, fmt.Errorf("readUshort: out of range")
	}
	v := binary.LittleEndian.Uint16(r.buf[r.pos:])
	r.pos += 2
	return v, nil
}

func (r *reader) readInt() (int32, error) {
	if r.pos+4 > len(r.buf) {
		return 0, fmt.Errorf("readInt: out of range")
	}
	v := int32(binary.LittleEndian.Uint32(r.buf[r.pos:]))
	r.pos += 4
	return v, nil
}

func (r *reader) readUint() (uint32, error) {
	if r.pos+4 > len(r.buf) {
		return 0, fmt.Errorf("readUint: out of range")
	}
	v := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *reader) readUlong() (uint64, error) {
	if r.pos+8 > len(r.buf) {
		return 0, fmt.Errorf("readUlong: out of range")
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return v, nil
}

func (r *reader) readSystemTime() (time.Time, error) {
	if r.pos+16 > len(r.buf) {
		return time.Time{}, fmt.Errorf("readSystemTime: out of range")
	}
	year := int(binary.LittleEndian.Uint16(r.buf[r.pos:]))
	month := int(binary.LittleEndian.Uint16(r.buf[r.pos+2:]))
	day := int(binary.LittleEndian.Uint16(r.buf[r.pos+6:]))
	hour := int(binary.LittleEndian.Uint16(r.buf[r.pos+8:]))
	min := int(binary.LittleEndian.Uint16(r.buf[r.pos+10:]))
	sec := int(binary.LittleEndian.Uint16(r.buf[r.pos+12:]))
	r.pos += 16
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0, jst)
	return t, nil
}

func (r *reader) readString() (string, error) {
	size, err := r.readInt()
	if err != nil {
		return "", err
	}
	if size < 6 {
		return "", fmt.Errorf("readString: invalid size %d", size)
	}
	strLen := int(size) - 6
	if r.pos+strLen > len(r.buf) {
		return "", fmt.Errorf("readString: out of range")
	}
	u16 := make([]uint16, strLen/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(r.buf[r.pos+i*2:])
	}
	r.pos += strLen + 2 // null終端分
	// utf16.Decode は []rune を返すので string() で直接変換する（中間 []rune 不要）
	return string(utf16.Decode(u16)), nil
}

// readStruct は構造体の先頭サイズを読んでサブリーダーを返す。
// EDCBのシリアライズは各構造体の先頭4バイトが「自身のサイズ（自身を含む）」になっている。
func (r *reader) readStruct() (*reader, error) {
	size, err := r.readInt()
	if err != nil {
		return nil, err
	}
	end := r.pos + int(size) - 4
	if size < 4 || end < r.pos || end > len(r.buf) {
		return nil, fmt.Errorf("readStruct: invalid size %d", size)
	}
	sub := &reader{buf: r.buf[r.pos:end]}
	r.pos = end
	return sub, nil
}

type ServiceInfo struct {
	Onid        uint16
	Tsid        uint16
	Sid         uint16
	ServiceType uint8
	ServiceName string
	NetworkName string
}

type EventInfo struct {
	Onid        uint16
	Tsid        uint16
	Sid         uint16
	Eid         uint16
	StartTime   time.Time
	HasTime     bool
	DurationSec uint32
	HasDuration bool
	EventName   string
	TextChar    string
}

type ServiceEventInfo struct {
	ServiceInfo ServiceInfo
	EventList   []EventInfo
}

func (r *reader) readVector(readFunc func(*reader) error) error {
	size, err := r.readInt()
	if err != nil {
		return err
	}
	count, err := r.readInt()
	if err != nil {
		return err
	}
	end := r.pos + int(size) - 8
	if size < 8 || count < 0 || count > maxVectorCount || end < r.pos || end > len(r.buf) {
		return fmt.Errorf("readVector: invalid size=%d count=%d", size, count)
	}
	sub := &reader{buf: r.buf[r.pos:end]}
	r.pos = end
	for i := 0; i < int(count); i++ {
		if err := readFunc(sub); err != nil {
			return err
		}
	}
	return nil
}

func readServiceInfo(r *reader) (ServiceInfo, error) {
	sub, err := r.readStruct()
	if err != nil {
		return ServiceInfo{}, err
	}
	var s ServiceInfo
	if s.Onid, err = sub.readUshort(); err != nil {
		return s, err
	}
	if s.Tsid, err = sub.readUshort(); err != nil {
		return s, err
	}
	if s.Sid, err = sub.readUshort(); err != nil {
		return s, err
	}
	if s.ServiceType, err = sub.readByte(); err != nil {
		return s, err
	}
	if _, err = sub.readByte(); err != nil { // partial_reception_flag（使用しないが読み飛ばしエラーは伝播）
		return s, err
	}
	if _, err = sub.readString(); err != nil { // service_provider_name（使用しないが読み飛ばしエラーは伝播）
		return s, err
	}
	if s.ServiceName, err = sub.readString(); err != nil {
		return s, err
	}
	if s.NetworkName, err = sub.readString(); err != nil {
		return s, err
	}
	return s, nil
}

func readEventInfo(r *reader) (EventInfo, error) {
	sub, err := r.readStruct()
	if err != nil {
		return EventInfo{}, err
	}
	var e EventInfo
	if e.Onid, err = sub.readUshort(); err != nil {
		return e, err
	}
	if e.Tsid, err = sub.readUshort(); err != nil {
		return e, err
	}
	if e.Sid, err = sub.readUshort(); err != nil {
		return e, err
	}
	if e.Eid, err = sub.readUshort(); err != nil {
		return e, err
	}

	startFlag, err := sub.readByte()
	if err != nil {
		return e, err
	}
	t, err := sub.readSystemTime()
	if err != nil {
		return e, err
	}
	if startFlag != 0 {
		e.StartTime = t
		e.HasTime = true
	}

	durFlag, err := sub.readByte()
	if err != nil {
		return e, err
	}
	dur, err := sub.readInt()
	if err != nil {
		return e, err
	}
	if durFlag != 0 {
		e.DurationSec = uint32(dur)
		e.HasDuration = true
	}

	// short_info: サイズが4（空構造体）の場合はスキップ
	marker, err := sub.readInt()
	if err != nil {
		return e, err
	}
	if marker != 4 {
		sub.pos -= 4
		shortSub, err := sub.readStruct()
		if err != nil {
			return e, err
		}
		if e.EventName, err = shortSub.readString(); err != nil {
			return e, err
		}
		if e.TextChar, err = shortSub.readString(); err != nil {
			return e, err
		}
	}

	return e, nil
}

func readServiceEventInfo(r *reader) (ServiceEventInfo, error) {
	sub, err := r.readStruct()
	if err != nil {
		return ServiceEventInfo{}, err
	}
	var sei ServiceEventInfo
	if sei.ServiceInfo, err = readServiceInfo(sub); err != nil {
		return sei, err
	}
	err = sub.readVector(func(inner *reader) error {
		e, err := readEventInfo(inner)
		if err != nil {
			return err
		}
		sei.EventList = append(sei.EventList, e)
		return nil
	})
	return sei, err
}

func (c *Client) EnumService() ([]ServiceInfo, error) {
	rbuf, err := c.sendCmd(cmdEnumService, nil)
	if err != nil {
		return nil, err
	}
	r := newReader(rbuf)
	var services []ServiceInfo
	err = r.readVector(func(inner *reader) error {
		s, err := readServiceInfo(inner)
		if err != nil {
			return err
		}
		services = append(services, s)
		return nil
	})
	return services, err
}

type FileData struct {
	Name string
	Data []byte
}

func readFileData(r *reader) (FileData, error) {
	sub, err := r.readStruct()
	if err != nil {
		return FileData{}, err
	}
	var f FileData
	if f.Name, err = sub.readString(); err != nil {
		return f, err
	}
	dataSize, err := sub.readInt()
	if err != nil {
		return f, err
	}
	if _, err = sub.readInt(); err != nil { // パディング
		return f, err
	}
	if sub.pos+int(dataSize) > len(sub.buf) {
		return f, fmt.Errorf("readFileData: out of range")
	}
	f.Data = make([]byte, dataSize)
	copy(f.Data, sub.buf[sub.pos:sub.pos+int(dataSize)])
	return f, nil
}

func (c *Client) EnumPgInfoEx(start, end time.Time) ([]ServiceEventInfo, error) {
	payload := make([]byte, 32)
	// 全サービス対象フィルタ（onid/tsid/sid をすべて 0xFFFF で埋める）
	binary.LittleEndian.PutUint64(payload[0:], 0xffffffffffff)
	binary.LittleEndian.PutUint64(payload[8:], 0xffffffffffff)
	binary.LittleEndian.PutUint64(payload[16:], timeToFileTime(start))
	binary.LittleEndian.PutUint64(payload[24:], timeToFileTime(end))

	buf := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(buf[0:], uint32(8+len(payload)))
	binary.LittleEndian.PutUint32(buf[4:], 4) // 要素数4
	copy(buf[8:], payload)

	rbuf, err := c.sendCmd(cmdEnumPgInfoEx, buf)
	if err != nil {
		return nil, err
	}
	r := newReader(rbuf)
	var result []ServiceEventInfo
	err = r.readVector(func(inner *reader) error {
		sei, err := readServiceEventInfo(inner)
		if err != nil {
			return err
		}
		result = append(result, sei)
		return nil
	})
	return result, err
}

type writer struct {
	buf []byte
}

func newWriter() *writer {
	return &writer{}
}

func (w *writer) writeByte(v uint8) {
	w.buf = append(w.buf, v)
}

// binary.LittleEndian.AppendUint* (Go 1.23〜) を使い中間スライスのアロケーションを排除
func (w *writer) writeUshort(v uint16) {
	w.buf = binary.LittleEndian.AppendUint16(w.buf, v)
}

func (w *writer) writeInt(v int32) {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, uint32(v))
}

func (w *writer) writeUint(v uint32) {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, v)
}

func (w *writer) writeString(s string) {
	u16 := utf16.Encode([]rune(s))
	size := int32(6 + len(u16)*2)
	w.writeInt(size)
	for _, v := range u16 {
		w.writeUshort(v)
	}
	w.writeUshort(0) // null終端
}

// SendNwTVMode はEDCBのグローバルなNetworkTV送信モードを設定する。
//
// mode: 1=UDP, 2=TCP, 3=UDP+TCP
//
// 【重要】SendNwTVIDSetCh で use_bon_ch=0（デフォルト）を使う場合、
// EDCBはこのグローバル設定を参照してEpgDataCap_Bonを起動する。
// この設定をしないと -nonw フラグ付きで起動されTCP出力が無効になる。
// use_bon_ch=1 で ch_or_mode を個別指定する場合はこのコマンドは不要
// だが、use_bon_ch=0 運用では毎リクエスト前に必ず送ること。
func (c *Client) SendNwTVMode(mode uint32) error {
	w := newWriter()
	w.writeUint(mode)
	_, err := c.sendCmd(cmdNwTVMode, w.buf)
	return err
}

// SendNwTVIDSetCh はNetworkTVモードでチューナーを起動しチャンネルを設定する。
// 戻り値はEpgDataCap_BonのプロセスID（OpenViewStreamに使用する）。
//
// 【重要】use_bon_ch=0, space_or_id=0, ch_or_mode=0 の構成では、
// 送信モードはSendNwTVModeのグローバル設定に依存する。
// use_bon_ch=1 にすると space_or_id がNetworkTV IDとして扱われ、
// ch_or_mode で送信モードを個別指定できる（KonomiTVはこちらの方式）。
//
// 【注意】SetChが成功してプロセスIDが返っても、EpgDataCap_Bonが
// BonDriverを初期化してSrvPipeにデータを流し始めるまでタイムラグがある。
// OpenViewStreamはリトライが必須。
//
// 【NetworkTV IDとプロセスIDの混同に注意】
//   - SetChの戻り値 = EpgDataCap_BonのプロセスID → OpenViewStreamに使う
//   - CloseNWTVに渡す値 = space_or_id で指定したNetworkTV ID（use_bon_ch=0なら0）
//
// この2つを混同するとCloseが失敗してEpgDataCap_Bonが残り続ける。
func (c *Client) SendNwTVIDSetCh(onid, tsid, sid uint16) (int32, error) {
	w := newWriter()
	pos := len(w.buf)
	w.writeInt(0) // サイズ仮置き（後で書き換え）
	w.writeInt(1) // use_sid=1（onid/tsid/sidで指定）
	w.writeUshort(onid)
	w.writeUshort(tsid)
	w.writeUshort(sid)
	w.writeInt(0) // use_bon_ch=0（グローバル設定を使用）
	w.writeInt(0) // space_or_id=0（NetworkTV ID）
	w.writeInt(0) // ch_or_mode=0（グローバル設定を使用）
	binary.LittleEndian.PutUint32(w.buf[pos:], uint32(len(w.buf)-pos))

	rbuf, err := c.sendCmd(cmdNwTVIDSetCh, w.buf)
	if err != nil {
		return 0, err
	}
	r := newReader(rbuf)
	return r.readInt() // EpgDataCap_BonのプロセスID
}

// SendNwTVIDClose はNetworkTV IDを指定してチューナーを終了する。
//
// 【重要】nwtvID には SendNwTVIDSetCh の space_or_id に渡した値を使う。
// use_bon_ch=0, space_or_id=0 の運用では 0 を渡す。
// EpgDataCap_BonのプロセスIDを渡しても対象が見つからずエラーになる。
//
// 【正しいクリーンアップ順序】
//  1. conn.Close()（SrvPipeのTCPコネクションを先に切断）
//  2. SendNwTVIDClose（その後でチューナー終了コマンドを送る）
//
// SrvPipeを開いたままCloseNWTVを送ると正常終了できない場合がある。
// deferの後入れ先出し特性を利用して、conn.Closeより後にdeferすること。
func (c *Client) SendNwTVIDClose(nwtvID int32) error {
	w := newWriter()
	w.writeInt(nwtvID)
	_, err := c.sendCmd(cmdNwTVIDClose, w.buf)
	return err
}

// OpenViewStreamWithRetry はSrvPipeストリームへの接続をリトライ付きで試みる。
//
// SetCh成功直後はEpgDataCap_BonがFIFOを準備中のため即座には成功しない。
// 0.1秒から徐々に伸ばしながら最大 timeout まで待つ（KonomiTV方式）。
//
// 【注意】失敗時にSendNwTVIDCloseを呼んではいけない。
// Closeしてしまうと次のSetChで別プロセスが起動してしまい、
// プロセスIDが毎回変わって永遠に成功しなくなる。
func (c *Client) OpenViewStreamWithRetry(processID int32, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	wait := 100 * time.Millisecond
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := c.openViewStream(processID)
		log.Printf("OpenViewStream: processID=%d err=%v", processID, err)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(wait)
		wait += 100 * time.Millisecond
		if wait > time.Second {
			wait = time.Second
		}
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
func (c *Client) openViewStream(processID int32) (net.Conn, error) {
	w := newWriter()
	w.writeInt(int32(cmdRelayViewStream))
	w.writeInt(int32(4)) // payloadSize
	w.writeInt(processID)

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.host, strconv.Itoa(c.port)), c.timeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Write(w.buf); err != nil {
		conn.Close()
		return nil, err
	}

	header := make([]byte, 8)
	if _, err := io.ReadFull(conn, header); err != nil {
		conn.Close()
		return nil, err
	}
	ret := binary.LittleEndian.Uint32(header[0:])
	size := binary.LittleEndian.Uint32(header[4:])
	if ret != cmdSuccess {
		conn.Close()
		return nil, fmt.Errorf("OpenViewStream failed: ret=%d", ret)
	}
	// レスポンスペイロードを読み捨て、以降はストリームデータ
	if size > 0 {
		discard := make([]byte, size)
		if _, err := io.ReadFull(conn, discard); err != nil {
			conn.Close()
			return nil, err
		}
	}

	// Deadlineをリセットしてストリーミング用に開放
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// SendFileCopy はEDCBサーバー上のファイルを転送する。
// name にはSettingディレクトリからの相対パスを渡す。
func (c *Client) SendFileCopy(name string) ([]byte, error) {
	w := newWriter()
	w.writeString(name)
	return c.sendCmd(cmdFileCopy, w.buf)
}

// SendFileCopy2 は複数ファイルをまとめて転送する。
// name に "LogoData\\*.*" を指定するとディレクトリ内ファイルリストが取得できる。
func (c *Client) SendFileCopy2(names []string) ([]FileData, error) {
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

	rbuf, err := c.sendCmd(cmdFileCopy2, w.buf)
	if err != nil {
		return nil, err
	}
	r := newReader(rbuf)
	ver, err := r.readUshort()
	if err != nil {
		return nil, err
	}
	if ver < 5 {
		return nil, fmt.Errorf("SendFileCopy2: unsupported version %d", ver)
	}
	var result []FileData
	err = r.readVector(func(inner *reader) error {
		f, err := readFileData(inner)
		if err != nil {
			return err
		}
		result = append(result, f)
		return nil
	})
	return result, err
}
