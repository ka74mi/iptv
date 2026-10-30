package api

import (
	"fmt"
	"testing"
)

func logoName(onid uint16, logoID, typ int) string {
	// 実ファイル名の形式: {onid:04x}_{logoID:03x}_{3文字}_{type:02d}.png
	return fmt.Sprintf("%04x_%03x_000_%02d.png", onid, logoID, typ)
}

// 同じロゴ(logo_id)を使う複数サービスすべてにロゴが割り当てられること。
func TestPlanLogosSharedFile(t *testing.T) {
	services := []ServiceInfo{
		{Onid: 4, Sid: 101},
		{Onid: 4, Sid: 102}, // 101 と同じロゴ
		{Onid: 4, Sid: 103}, // 別ロゴ
		{Onid: 4, Sid: 104}, // iniに無い
	}
	iniMap := map[string]int{"00040065": 0x10, "00040066": 0x10, "00040067": 0x11}
	names := []string{logoName(4, 0x10, 5), logoName(4, 0x11, 2)}

	p := planLogos(services, iniMap, names)
	if len(p.files) != 2 {
		t.Fatalf("files = %v, want 2 unique files", p.files)
	}
	logos := p.build([]FileData{{Data: []byte("A")}, {Data: []byte("B")}})
	want := map[uint32]string{
		4<<16 | 101: "A",
		4<<16 | 102: "A",
		4<<16 | 103: "B",
	}
	if len(logos) != len(want) {
		t.Fatalf("logos = %v", logos)
	}
	for k, v := range want {
		if string(logos[k]) != v {
			t.Errorf("logos[%#x] = %q, want %q", k, logos[k], v)
		}
	}
}

// 取得結果が空（存在しないファイル）のサービスは登録されないこと。
func TestLogoPlanBuildSkipsEmpty(t *testing.T) {
	services := []ServiceInfo{{Onid: 4, Sid: 101}}
	p := planLogos(services, map[string]int{"00040065": 0x10}, []string{logoName(4, 0x10, 5)})
	if got := p.build([]FileData{{}}); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}
