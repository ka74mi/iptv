package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
	"unicode/utf16"
)

// ---- テスト用のエンコーダ（実装のwriterとは独立に、EDCBのバイナリ形式を組み立てる）----

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func cat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

// 文字列: [size:4][UTF-16LE...][NUL:2]（sizeは自身の4バイトとNULを含む）
func encString(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := le32(uint32(4 + len(u)*2 + 2))
	for _, v := range u {
		b = append(b, le16(v)...)
	}
	return append(b, 0, 0)
}

// 構造体: [size:4（自身を含む）][body...]
func encStruct(body ...[]byte) []byte {
	b := cat(body...)
	return append(le32(uint32(4+len(b))), b...)
}

// ベクタ: [size:4（自身とcountを含む）][count:4][elements...]
func encVector(elems ...[]byte) []byte {
	b := cat(elems...)
	return cat(le32(uint32(8+len(b))), le32(uint32(len(elems))), b)
}

func encSystemTime(y, mo, d, h, mi, s int) []byte {
	// year, month, dayOfWeek, day, hour, minute, second, milliseconds
	return cat(le16(uint16(y)), le16(uint16(mo)), le16(0), le16(uint16(d)),
		le16(uint16(h)), le16(uint16(mi)), le16(uint16(s)), le16(0))
}

func encServiceInfo(onid, tsid, sid uint16, typ uint8, provider, name, network string, trailing ...byte) []byte {
	return encStruct(le16(onid), le16(tsid), le16(sid), []byte{typ, 0},
		encString(provider), encString(name), encString(network), trailing)
}

type testEvent struct {
	eid        uint16
	hasStart   bool
	hasDur     bool
	dur        int32
	name, text string
	emptyShort bool
	trailing   []byte
}

func encEventInfo(onid, tsid, sid uint16, e testEvent) []byte {
	flag := func(b bool) []byte {
		if b {
			return []byte{1}
		}
		return []byte{0}
	}
	short := le32(4) // 空のshort_info
	if !e.emptyShort {
		short = encStruct(encString(e.name), encString(e.text))
	}
	return encStruct(le16(onid), le16(tsid), le16(sid), le16(e.eid),
		flag(e.hasStart), encSystemTime(2026, 10, 28, 21, 30, 0),
		flag(e.hasDur), le32(uint32(e.dur)),
		short, e.trailing)
}

func ctx() context.Context { return context.Background() }

// ---- 偽のEDCB ----

// newFakeEDCB は、どのコマンドにも reply を成功ペイロードとして返すサーバーを立てる。
func newFakeEDCB(t *testing.T, reply []byte) *Client {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				h := make([]byte, 8)
				if _, err := io.ReadFull(c, h); err != nil {
					return
				}
				io.CopyN(io.Discard, c, int64(binary.LittleEndian.Uint32(h[4:])))
				c.Write(cat(le32(1), le32(uint32(len(reply))), reply))
			}()
		}
	}()
	return NewClient("127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
}

// ---- 正常系 ----

func TestEnumService(t *testing.T) {
	reply := encVector(
		encServiceInfo(4, 0x10, 101, 0x01, "プロバイダ", "ＮＨＫ総合１", "地上デジタル"),
		// 将来の拡張フィールドが末尾にあっても読み飛ばせる
		encServiceInfo(4, 0x10, 102, 0xC0, "", "データ", "", 1, 2, 3, 4),
	)
	got, err := newFakeEDCB(t, reply).EnumService(ctx())
	if err != nil {
		t.Fatal(err)
	}
	want := []ServiceInfo{
		{Onid: 4, Tsid: 0x10, Sid: 101, ServiceType: 0x01, ServiceName: "ＮＨＫ総合１", NetworkName: "地上デジタル"},
		{Onid: 4, Tsid: 0x10, Sid: 102, ServiceType: 0xC0, ServiceName: "データ", NetworkName: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d services: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestEnumPgInfoEx(t *testing.T) {
	events := []testEvent{
		{eid: 1, hasStart: true, hasDur: true, dur: 1800, name: "ニュース", text: "概要"},
		{eid: 2, emptyShort: true}, // 時刻なし・番組情報なし
		{eid: 3, hasStart: true, hasDur: true, dur: 3600, name: "ドラマ", text: "", trailing: []byte{9, 9, 9}},
	}
	var evs [][]byte
	for _, e := range events {
		evs = append(evs, encEventInfo(4, 0x10, 101, e))
	}
	reply := encVector(encStruct(
		encServiceInfo(4, 0x10, 101, 0x01, "", "ＮＨＫ総合", "地上"),
		encVector(evs...),
	))

	got, err := newFakeEDCB(t, reply).EnumPgInfoEx(ctx(), time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ServiceInfo.ServiceName != "ＮＨＫ総合" || len(got[0].EventList) != 3 {
		t.Fatalf("got %+v", got)
	}
	e := got[0].EventList
	wantStart := time.Date(2026, 10, 28, 21, 30, 0, 0, jst)
	if !e[0].HasTime || !e[0].StartTime.Equal(wantStart) || !e[0].HasDuration || e[0].DurationSec != 1800 ||
		e[0].EventName != "ニュース" || e[0].TextChar != "概要" || e[0].Eid != 1 || e[0].Sid != 101 {
		t.Errorf("event0 = %+v", e[0])
	}
	if e[1].HasTime || e[1].HasDuration || e[1].EventName != "" || e[1].Eid != 2 {
		t.Errorf("event1 = %+v", e[1])
	}
	if e[2].EventName != "ドラマ" || e[2].DurationSec != 3600 {
		t.Errorf("event2 = %+v", e[2])
	}
}

func TestSendFileCopy2(t *testing.T) {
	file := func(name string, data []byte) []byte {
		return encStruct(encString(name), le32(uint32(len(data))), le32(0), data)
	}
	reply := cat(le16(5), encVector(file("a.png", []byte{1, 2, 3}), file("missing", nil)))
	got, err := newFakeEDCB(t, reply).SendFileCopy2(ctx(), []string{"a.png", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a.png" || !bytes.Equal(got[0].Data, []byte{1, 2, 3}) ||
		got[1].Name != "missing" || len(got[1].Data) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestSendFileCopy2RejectsOldVersion(t *testing.T) {
	reply := cat(le16(4), encVector())
	if _, err := newFakeEDCB(t, reply).SendFileCopy2(ctx(), []string{"x"}); err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

// ---- 異常系: 壊れた応答でpanicせず、エラーを返す ----

// 正しい応答の「あらゆる途中切れ」は、必ずエラーになる。
func TestTruncatedResponsesAreErrors(t *testing.T) {
	svc := encVector(encServiceInfo(4, 1, 101, 1, "p", "名前", "net"))
	pg := encVector(encStruct(
		encServiceInfo(4, 1, 101, 1, "", "名前", ""),
		encVector(encEventInfo(4, 1, 101, testEvent{eid: 1, hasStart: true, hasDur: true, dur: 60, name: "a", text: "b"})),
	))
	file := cat(le16(5), encVector(encStruct(encString("f"), le32(2), le32(0), []byte{1, 2})))

	cases := []struct {
		name  string
		full  []byte
		parse func(*Client) error
	}{
		{"EnumService", svc, func(c *Client) error { _, err := c.EnumService(ctx()); return err }},
		{"EnumPgInfoEx", pg, func(c *Client) error { _, err := c.EnumPgInfoEx(ctx(), time.Now(), time.Now()); return err }},
		{"SendFileCopy2", file, func(c *Client) error { _, err := c.SendFileCopy2(ctx(), []string{"f"}); return err }},
	}
	for _, tc := range cases {
		if err := tc.parse(newFakeEDCB(t, tc.full)); err != nil {
			t.Fatalf("%s: full response failed: %v", tc.name, err)
		}
		for n := 0; n < len(tc.full); n++ {
			if err := tc.parse(newFakeEDCB(t, tc.full[:n])); err == nil {
				t.Errorf("%s: truncated to %d/%d bytes: expected error", tc.name, n, len(tc.full))
			}
		}
	}
}

func TestMalformedResponsesAreErrors(t *testing.T) {
	huge := func(count uint32) []byte { return cat(le32(8), le32(count)) }
	file := func(dataSize int32) []byte {
		return cat(le16(5), encVector(encStruct(encString("f"), le32(uint32(dataSize)), le32(0), []byte{1, 2})))
	}
	cases := []struct {
		name  string
		reply []byte
		call  func(*Client) error
	}{
		{"vector count too large", huge(1 << 30), func(c *Client) error { _, err := c.EnumService(ctx()); return err }},
		{"vector count exceeds data", huge(50000), func(c *Client) error { _, err := c.EnumService(ctx()); return err }},
		{"vector negative count", huge(0xFFFFFFFF), func(c *Client) error { _, err := c.EnumService(ctx()); return err }},
		{"struct size too small", cat(le32(12), le32(1), le32(2)), func(c *Client) error { _, err := c.EnumService(ctx()); return err }},
		{"file negative size", file(-1), func(c *Client) error { _, err := c.SendFileCopy2(ctx(), []string{"f"}); return err }},
		{"file size beyond data", file(1 << 20), func(c *Client) error { _, err := c.SendFileCopy2(ctx(), []string{"f"}); return err }},
	}
	for _, tc := range cases {
		if err := tc.call(newFakeEDCB(t, tc.reply)); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

// 放送時間が負数の番組は、巨大な時間として扱わず「時間なし」にする。
func TestNegativeDurationIsIgnored(t *testing.T) {
	reply := encVector(encStruct(
		encServiceInfo(4, 1, 101, 1, "", "名前", ""),
		encVector(encEventInfo(4, 1, 101, testEvent{eid: 1, hasStart: true, hasDur: true, dur: -1, name: "a"})),
	))
	got, err := newFakeEDCB(t, reply).EnumPgInfoEx(ctx(), time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if e := got[0].EventList[0]; e.HasDuration || e.DurationSec != 0 {
		t.Fatalf("event = %+v", e)
	}
}

// どんなバイト列でもpanicせず、エラーか結果のどちらかを返す。
func FuzzParsers(f *testing.F) {
	f.Add(encVector(encServiceInfo(4, 1, 101, 1, "p", "名前", "net")))
	f.Add(encVector(encStruct(
		encServiceInfo(4, 1, 101, 1, "", "名前", ""),
		encVector(encEventInfo(4, 1, 101, testEvent{eid: 1, hasStart: true, hasDur: true, dur: 60, name: "a", text: "b"})),
	)))
	f.Add(cat(le16(5), encVector(encStruct(encString("f"), le32(2), le32(0), []byte{1, 2}))))
	f.Fuzz(func(t *testing.T, b []byte) {
		parseServiceList(b)
		parseProgramList(b)
		parseFileCopy2(b)
		newReader(b).str()
	})
}
