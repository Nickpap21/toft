package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mcpCall is one POST /mcp from addr; auth "" sends no Authorization header.
func mcpCall(h http.Handler, addr, auth string) int {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.RemoteAddr = addr + ":1234"
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

// refused sends n calls and reports whether any got a 429.
func refused(h http.Handler, addr string, n int, auth func(i int) string) bool {
	for i := 0; i < n; i++ {
		if mcpCall(h, addr, auth(i)) == http.StatusTooManyRequests {
			return true
		}
	}
	return false
}

func TestMCPLimitedPerAddress(t *testing.T) {
	r := testRouter()

	// A new Authorization header on every request gets a new per-token
	// bucket each time; the per-address limit still holds.
	if !refused(r, "203.0.113.1", 200, func(i int) string { return fmt.Sprintf("Bearer forged-%d", i) }) {
		t.Fatal("200 requests from one address with a new token each were never limited")
	}

	// A flood with no header is limited per address, and does not spend
	// another address's budget (a client's first discovery request).
	if !refused(r, "203.0.113.2", 200, func(int) string { return "" }) {
		t.Fatal("200 requests from one address with no header were never limited")
	}
	if code := mcpCall(r, "203.0.113.3", ""); code == http.StatusTooManyRequests {
		t.Fatal("another address's first request was refused")
	}
}
