package api

import (
	"reflect"
	"testing"
)

func TestParseLogoIni(t *testing.T) {
	data := []byte("[LogoIDMap]\r\n00040065=16\r\n0004 0066 = 17\n00040067=18 ; comment\n00040068=abc\n0004006=5\nbroken\n")
	got := parseLogoIni(data)
	want := map[string]int{"00040065": 16, "00040067": 18}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseLogoIndex(t *testing.T) {
	data := []byte("\xEF\xBB\xBF0 0 0 .\n0 0 0 ..\n1 100 2000 0004_010_000_05.png\nshort line\n")
	got := parseLogoIndex(data)
	want := []string{"0004_010_000_05.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveLogoFileNamePrefersHigherType(t *testing.T) {
	names := []string{"0004_010_000_02.png", "0004_010_000_05.png", "0004_011_000_05.png", "0005_010_000_05.png"}
	if got := resolveLogoFileName(names, 4, 0x10); got != "0004_010_000_05.png" {
		t.Fatalf("got %q", got)
	}
	if got := resolveLogoFileName(names, 4, 0x99); got != "" {
		t.Fatalf("unknown logo = %q", got)
	}
}

func TestNilLogoCacheIsSafe(t *testing.T) {
	var lc *LogoCache
	if lc.Get(1, 2) != nil {
		t.Fatal("nil cache should return nil")
	}
}
