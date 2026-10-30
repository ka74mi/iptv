package api

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// parseLogoIni は LogoData.ini をパースして onid+sid → logo_id のマップを返す。
// キーは "ONIDSID" の8桁大文字16進数（例: "00070126"）、値は logo_id。
func parseLogoIni(data []byte) map[string]int {
	m := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(kv[0]))
		if len(key) != 8 {
			continue
		}
		// 値の後ろにコメント等が続いても先頭の数値だけを使う。
		fields := strings.Fields(kv[1])
		if len(fields) == 0 {
			continue
		}
		logoID, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		m[key] = logoID
	}
	return m
}

// parseLogoIndex はディレクトリリストをパースしてファイル名一覧を返す。
func parseLogoIndex(data []byte) []string {
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		a := strings.Fields(strings.TrimSpace(line))
		if len(a) < 4 {
			continue
		}
		name := a[3]
		if name == "." || name == ".." {
			continue
		}
		names = append(names, name)
	}
	return names
}

// resolveLogoFileName はonid・logo_idからファイル名を解決する。
// タイプ優先順位: 5 > 2 > 4 > 1 > 3 > 0
func resolveLogoFileName(names []string, onid uint16, logoID int) string {
	prefix := fmt.Sprintf("%04x_%03x_", onid, logoID)
	logoTypes := []int{5, 2, 4, 1, 3, 0}
	for _, t := range logoTypes {
		suffix := fmt.Sprintf("_%02d.png", t)
		for _, name := range names {
			if len(name) < 16 {
				continue
			}
			if strings.EqualFold(name[:9], prefix) && strings.EqualFold(name[12:], suffix) {
				return name
			}
		}
	}
	return ""
}

type LogoCache struct {
	logos map[uint32][]byte // key: onid<<16 | sid
}

// logoPlan は取得すべきロゴファイルと、各ファイルを使うサービスキーの対応。
type logoPlan struct {
	files []string            // 取得するファイル名（重複なし・サービス順）
	keys  map[string][]uint32 // ファイル名 → そのロゴを使うサービスキー（onid<<16 | sid）
}

// planLogos はサービス一覧からロゴファイルを解決する。
//
// 複数のサービスが同じロゴファイルを参照することがあるため（サブチャンネル等）、
// ファイルは重複なく1回だけ取得し、使うサービスすべてにキーを紐づける。
func planLogos(services []ServiceInfo, iniMap map[string]int, indexNames []string) logoPlan {
	p := logoPlan{keys: make(map[string][]uint32)}
	for _, s := range services {
		logoID, ok := iniMap[fmt.Sprintf("%04X%04X", s.Onid, s.Sid)]
		if !ok {
			continue
		}
		filename := resolveLogoFileName(indexNames, s.Onid, logoID)
		if filename == "" {
			continue
		}
		if _, seen := p.keys[filename]; !seen {
			p.files = append(p.files, filename)
		}
		p.keys[filename] = append(p.keys[filename], uint32(s.Onid)<<16|uint32(s.Sid))
	}
	return p
}

// build は取得結果（files と同じ並び）からサービスキー → PNG のマップを作る。
// 同じファイルを使うサービスは同じ []byte を共有する（読み取り専用）。
func (p logoPlan) build(pngs []FileData) map[uint32][]byte {
	logos := make(map[uint32][]byte)
	for i, f := range pngs {
		if i >= len(p.files) || len(f.Data) == 0 {
			continue
		}
		for _, key := range p.keys[p.files[i]] {
			logos[key] = f.Data
		}
	}
	return logos
}

func NewLogoCache(ctx context.Context, client *Client, services []ServiceInfo) (*LogoCache, error) {
	// Step1: LogoData.ini とディレクトリリストを取得
	files, err := client.SendFileCopy2(ctx, []string{"LogoData.ini", "LogoData\\*.*"})
	if err != nil {
		return nil, fmt.Errorf("SendFileCopy2: %w", err)
	}
	if len(files) < 2 {
		return nil, fmt.Errorf("SendFileCopy2: unexpected file count %d", len(files))
	}

	// Step2: サービス一覧からファイル名を解決（同じロゴを使うサービスはまとめる）
	plan := planLogos(services, parseLogoIni(files[0].Data), parseLogoIndex(files[1].Data))

	// Step3: PNGを一括取得（EDCBは要求した一覧と同じ並びで返し、存在しないファイルはData空）
	names := make([]string, len(plan.files))
	for i, f := range plan.files {
		names[i] = "LogoData\\" + f
	}
	pngFiles, err := client.SendFileCopy2(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("SendFileCopy2 logos: %w", err)
	}

	// Step4: キャッシュに格納
	cache := &LogoCache{logos: plan.build(pngFiles)}
	slog.Info("logos loaded", "services", len(cache.logos), "files", len(plan.files))
	return cache, nil
}

// NewLogoCacheWith は、固定のロゴ（キーは {onid, sid}）を持つキャッシュを作る。
// EDCBから取得せずに使いたい場合（テストなど）向け。
func NewLogoCacheWith(logos map[[2]uint16][]byte) *LogoCache {
	lc := &LogoCache{logos: make(map[uint32][]byte, len(logos))}
	for k, v := range logos {
		lc.logos[uint32(k[0])<<16|uint32(k[1])] = v
	}
	return lc
}

// Get はロゴのPNGを返す。無ければ（lc が nil の場合も）nil。
func (lc *LogoCache) Get(onid, sid uint16) []byte {
	if lc == nil {
		return nil
	}
	return lc.logos[uint32(onid)<<16|uint32(sid)]
}
