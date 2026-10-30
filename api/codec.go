package api

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
	"unicode/utf16"
)

// ---- 時刻 ----

var jst = time.FixedZone("JST", 9*60*60)

// time.Time → FILETIME
//
// EDCBは日時をJSTの壁時計時刻のままFILETIME化した値（JST を UTC とみなした値）で
// 比較する。真のUTCで送ると範囲が9時間ずれるため、JSTの壁時計時刻に直してから変換する。
func timeToFileTime(t time.Time) uint64 {
	const epochDiff = 116444736000000000 // 1601-01-01 から 1970-01-01 までの100ナノ秒数
	_, off := t.In(jst).Zone()
	return uint64((t.Unix()+int64(off))*10_000_000) + epochDiff
}

// ---- 読み取り ----

var errOutOfRange = errors.New("out of range")

// reader はEDCBのバイナリ形式（リトルエンディアン）を読む。
//
// 最初に発生したエラーを err に保持し、以降の読み取りは何もせずゼロ値を返す。
// 呼び出し側は読み取りのたびにエラーを確認せず、最後に err を1回見ればよい。
//
// 構造体・ベクタは先頭に「自身のサイズ」を持つので、sub() / readVector で
// 範囲を切り出したサブリーダーを使う。これにより、未知の拡張フィールドが
// 末尾に追加されても読み飛ばせる。サブリーダーのエラーは absorb で親に伝える。
type reader struct {
	buf []byte
	pos int
	err error
}

func newReader(buf []byte) *reader {
	return &reader{buf: buf}
}

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// absorb はサブリーダーで発生したエラーを r に引き継ぐ。
func (r *reader) absorb(sub *reader) {
	if sub.err != nil {
		r.fail(sub.err)
	}
}

// take は n バイトを読み進めて返す。範囲外・負数の場合は err を設定して nil を返す。
func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.buf)-r.pos {
		r.err = fmt.Errorf("read %d bytes at offset %d (len %d): %w", n, r.pos, len(r.buf), errOutOfRange)
		return nil
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *reader) u8() uint8 {
	if b := r.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (r *reader) u16() uint16 {
	if b := r.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (r *reader) i32() int32 {
	if b := r.take(4); b != nil {
		return int32(binary.LittleEndian.Uint32(b))
	}
	return 0
}

// systemTime はWindowsのSYSTEMTIME（16バイト）を読み、JSTの時刻として返す。
// 構成: year, month, dayOfWeek, day, hour, minute, second, milliseconds（各2バイト）。
func (r *reader) systemTime() time.Time {
	b := r.take(16)
	if b == nil {
		return time.Time{}
	}
	f := func(i int) int { return int(binary.LittleEndian.Uint16(b[i*2:])) }
	return time.Date(f(0), time.Month(f(1)), f(3), f(4), f(5), f(6), 0, jst)
}

// str は文字列を読む。形式: [size:4（自身とNULを含む）][UTF-16LE...][NUL:2]
func (r *reader) str() string {
	size := r.i32()
	if r.err != nil {
		return ""
	}
	if size < 6 {
		r.fail(fmt.Errorf("invalid string size %d", size))
		return ""
	}
	body := r.take(int(size) - 4) // UTF-16の本体 + NUL終端
	if body == nil {
		return ""
	}
	u16 := make([]uint16, (len(body)-2)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(body[i*2:])
	}
	return string(utf16.Decode(u16))
}

// sub は構造体を読み、その本体だけを範囲とするサブリーダーを返す。
// EDCBのシリアライズは各構造体の先頭4バイトが「自身のサイズ（自身を含む）」になっている。
// エラー時は、エラーを持った空のサブリーダーを返す（呼び出し側は続けて読んでよい）。
func (r *reader) sub() *reader {
	size := r.i32()
	if r.err == nil && size < 4 {
		r.fail(fmt.Errorf("invalid struct size %d", size))
	}
	if r.err != nil {
		return &reader{err: r.err}
	}
	body := r.take(int(size) - 4)
	return &reader{buf: body, err: r.err}
}

// readVector はベクタ（[size:4][count:4][要素...]）を読み、各要素を read で読む。
// 先頭の size は自身（4バイト）と count（4バイト）を含む。
func readVector[T any](r *reader, read func(*reader) T) []T {
	size, count := r.i32(), r.i32()
	if r.err != nil {
		return nil
	}
	if size < 8 || count < 0 || count > maxVectorCount {
		r.fail(fmt.Errorf("invalid vector size=%d count=%d", size, count))
		return nil
	}
	body := r.take(int(size) - 8)
	if body == nil && r.err != nil {
		return nil
	}
	// 各要素は少なくとも自身のサイズ（4バイト）を持つ。実データに対して件数が
	// 多すぎる場合は壊れた応答とみなす（巨大なスライスの事前確保も防ぐ）。
	if int(count) > len(body)/4 {
		r.fail(fmt.Errorf("invalid vector count=%d for %d bytes", count, len(body)))
		return nil
	}
	sub := &reader{buf: body}
	out := make([]T, 0, count)
	for i := 0; i < int(count) && sub.err == nil; i++ {
		out = append(out, read(sub))
	}
	r.absorb(sub)
	if r.err != nil {
		return nil
	}
	return out
}

// ---- 書き込み ----

type writer struct {
	buf []byte
}

func newWriter() *writer {
	return &writer{}
}

func (w *writer) writeUshort(v uint16) {
	w.buf = binary.LittleEndian.AppendUint16(w.buf, v)
}

func (w *writer) writeInt(v int32) {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, uint32(v))
}

// writeString は文字列を [size:4][UTF-16LE...][NUL:2] で書く。
func (w *writer) writeString(s string) {
	u16 := utf16.Encode([]rune(s))
	w.writeInt(int32(6 + len(u16)*2))
	for _, v := range u16 {
		w.writeUshort(v)
	}
	w.writeUshort(0) // null終端
}
