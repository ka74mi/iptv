package listen

import "testing"

func TestParse(t *testing.T) {
	ok := map[string]string{
		":8080":           ":8080",
		" :8080 ":         ":8080",
		"127.0.0.1:9000":  "127.0.0.1:9000",
		"0.0.0.0:80":      "0.0.0.0:80",
		"[::1]:8080":      "[::1]:8080",
		"[::]:65535":      "[::]:65535",
		"host.local:1234": "host.local:1234",
	}
	for in, want := range ok {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "8080", "localhost", ":", ":0", ":65536", ":-1", ":http", ":80a", "::1:8080", "host:"} {
		if got, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %q, want error", in, got)
		}
	}
}

func TestPort(t *testing.T) {
	if got := Port("127.0.0.1:9000"); got != "9000" {
		t.Fatalf("Port = %q", got)
	}
	if got := Port("[::1]:8080"); got != "8080" {
		t.Fatalf("Port = %q", got)
	}
}

func TestHealthURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://localhost:8080/health",
		"0.0.0.0:8080":   "http://localhost:8080/health",
		"[::]:8080":      "http://localhost:8080/health",
		"127.0.0.1:9000": "http://127.0.0.1:9000/health",
		"[::1]:8080":     "http://[::1]:8080/health",
		"192.168.0.5:80": "http://192.168.0.5:80/health",
	}
	for in, want := range cases {
		if got := HealthURL(in); got != want {
			t.Errorf("HealthURL(%q) = %q, want %q", in, got, want)
		}
	}
}
