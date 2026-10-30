package api

import (
	"bytes"
	"fmt"
	"time"
)

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

type FileData struct {
	Name string
	Data []byte
}

// 各 read* は、構造体1個を r から読んで返す。エラーは r.err に記録される。

func readServiceInfo(r *reader) ServiceInfo {
	sub := r.sub()
	s := ServiceInfo{Onid: sub.u16(), Tsid: sub.u16(), Sid: sub.u16(), ServiceType: sub.u8()}
	sub.u8()  // partial_reception_flag（使用しない）
	sub.str() // service_provider_name（使用しない）
	s.ServiceName = sub.str()
	s.NetworkName = sub.str()
	r.absorb(sub)
	return s
}

func readEventInfo(r *reader) EventInfo {
	sub := r.sub()
	e := EventInfo{Onid: sub.u16(), Tsid: sub.u16(), Sid: sub.u16(), Eid: sub.u16()}

	startFlag := sub.u8()
	start := sub.systemTime()
	if startFlag != 0 {
		e.StartTime, e.HasTime = start, true
	}

	durFlag := sub.u8()
	dur := sub.i32()
	if durFlag != 0 && dur >= 0 {
		e.DurationSec, e.HasDuration = uint32(dur), true
	}

	// short_info: 内容がない場合は空の構造体（サイズ4）が入っている。
	// それ以降（拡張情報など）は使用しないので、構造体末尾までまとめて読み飛ばされる。
	short := sub.sub()
	if len(short.buf) > 0 {
		e.EventName = short.str()
		e.TextChar = short.str()
	}
	sub.absorb(short)
	r.absorb(sub)
	return e
}

func readServiceEventInfo(r *reader) ServiceEventInfo {
	sub := r.sub()
	sei := ServiceEventInfo{ServiceInfo: readServiceInfo(sub)}
	sei.EventList = readVector(sub, readEventInfo)
	r.absorb(sub)
	return sei
}

func readFileData(r *reader) FileData {
	sub := r.sub()
	f := FileData{Name: sub.str()}
	dataSize := sub.i32()
	sub.i32() // パディング
	// 負数や残りより大きいサイズは take がエラーにする。
	if data := sub.take(int(dataSize)); sub.err == nil {
		f.Data = bytes.Clone(data) // 応答バッファ全体を保持し続けないようコピーする
	}
	r.absorb(sub)
	return f
}

// ---- 応答全体のパース ----

// parseServiceList は EnumService の応答を読む。
func parseServiceList(buf []byte) ([]ServiceInfo, error) {
	r := newReader(buf)
	v := readVector(r, readServiceInfo)
	return v, r.err
}

// parseProgramList は EnumPgInfoEx の応答を読む。
func parseProgramList(buf []byte) ([]ServiceEventInfo, error) {
	r := newReader(buf)
	v := readVector(r, readServiceEventInfo)
	return v, r.err
}

// parseFileCopy2 は SendFileCopy2 の応答を読む。形式: [CMD_VER:2][vector<FILE_DATA>]
func parseFileCopy2(buf []byte) ([]FileData, error) {
	r := newReader(buf)
	ver := r.u16()
	if r.err != nil {
		return nil, r.err
	}
	if ver < 5 {
		return nil, fmt.Errorf("SendFileCopy2: unsupported version %d", ver)
	}
	v := readVector(r, readFileData)
	return v, r.err
}
