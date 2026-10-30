package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewLoggerLevelAndFormat(t *testing.T) {
	var buf bytes.Buffer
	l, err := newLogger(&buf, "WARN", "json") // レベル名は大文字小文字を問わない
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hidden")
	l.Warn("shown", "onid", 4, "err", "boom")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %q", lines)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("not JSON: %v: %q", err, lines[0])
	}
	if rec["level"] != "WARN" || rec["msg"] != "shown" || rec["onid"] != float64(4) || rec["err"] != "boom" {
		t.Fatalf("record = %v", rec)
	}

	buf.Reset()
	l, err = newLogger(&buf, "debug", "text")
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("detail", "sid", 101)
	if out := buf.String(); !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "sid=101") {
		t.Fatalf("text output = %q", out)
	}
}

func TestNewLoggerRejectsInvalid(t *testing.T) {
	for _, c := range [][2]string{{"verbose", "text"}, {"info", "xml"}, {"", "text"}} {
		if _, err := newLogger(&bytes.Buffer{}, c[0], c[1]); err == nil {
			t.Errorf("newLogger(%q, %q): expected error", c[0], c[1])
		}
	}
}
