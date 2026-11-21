package ratelimit

import (
	"net/http"
	"testing"
)

func TestLimiter(t *testing.T) {
	l := New(3)
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d should pass", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("4th request must be limited")
	}
	if !l.Allow("b") {
		t.Fatal("other keys are independent")
	}
}

func TestClientIP(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	if got := ClientIP(r, false); got != "10.0.0.5" {
		t.Errorf("untrusted proxy: got %s", got)
	}
	if got := ClientIP(r, true); got != "1.2.3.4" {
		t.Errorf("trusted proxy must use the last hop, got %s", got)
	}
}
