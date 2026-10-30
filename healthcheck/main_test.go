package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer hang.Close()

	c := &http.Client{Timeout: 300 * time.Millisecond}
	if err := check(c, ok.URL); err != nil {
		t.Errorf("healthy server: %v", err)
	}
	if err := check(c, bad.URL); err == nil {
		t.Error("500 must be unhealthy")
	}
	start := time.Now()
	if err := check(c, hang.URL); err == nil {
		t.Error("hanging server must be unhealthy")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("did not time out promptly: %v", d)
	}
	if err := check(c, "http://127.0.0.1:1/health"); err == nil {
		t.Error("refused connection must be unhealthy")
	}
}
