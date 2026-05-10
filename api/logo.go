package api

import (
	"fmt"
	"log"
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
		var logoID int
		if _, err := fmt.Sscanf(kv[1], "%d", &logoID); err != nil {
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

func NewLogoCache(client *Client, services []ServiceInfo) (*LogoCache, error) {
	// Step1: LogoData.ini とディレクトリリストを取得
	files, err := client.SendFileCopy2([]string{"LogoData.ini", "LogoData\\*.*"})
	if err != nil {
		return nil, fmt.Errorf("SendFileCopy2: %w", err)
	}
	if len(files) < 2 {
		return nil, fmt.Errorf("SendFileCopy2: unexpected file count %d", len(files))
	}

	iniMap := parseLogoIni(files[0].Data)
	indexNames := parseLogoIndex(files[1].Data)

	// Step2: サービス一覧からファイル名を解決
	type entry struct {
		key      uint32
		filename string
	}
	var entries []entry
	seen := make(map[string]bool)
	for _, s := range services {
		key := fmt.Sprintf("%04X%04X", s.Onid, s.Sid)
		logoID, ok := iniMap[key]
		if !ok {
			continue
		}
		filename := resolveLogoFileName(indexNames, s.Onid, logoID)
		if filename == "" || seen[filename] {
			continue
		}
		seen[filename] = true
		entries = append(entries, entry{
			key:      uint32(s.Onid)<<16 | uint32(s.Sid),
			filename: filename,
		})
	}

	// Step3: PNGを一括取得
	filenames := make([]string, len(entries))
	for i, e := range entries {
		filenames[i] = "LogoData\\" + e.filename
	}
	pngFiles, err := client.SendFileCopy2(filenames)
	if err != nil {
		return nil, fmt.Errorf("SendFileCopy2 logos: %w", err)
	}

	// Step4: キャッシュに格納
	cache := &LogoCache{logos: make(map[uint32][]byte)}
	for i, f := range pngFiles {
		if i < len(entries) && len(f.Data) > 0 {
			cache.logos[entries[i].key] = f.Data
		}
	}
	log.Printf("LogoCache: %d logos loaded", len(cache.logos))
	return cache, nil
}

func (lc *LogoCache) Get(onid, sid uint16) []byte {
	return lc.logos[uint32(onid)<<16|uint32(sid)]
}
