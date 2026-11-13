package domain

import "testing"

func TestValidHostname(t *testing.T) {
	for h, want := range map[string]bool{
		"app.example.com": true, "example.com": true, "a-b.c-d.example.co.uk": true,
		"*.example.com": true, "*.studio.example.org": true,
		"localhost": false, "*.com": false, "*": false, "**.example.com": false, "a.*.example.com": false,
		"-a.example.com": false, "a-.example.com": false, "a..example.com": false, "a_b.example.com": false,
		"App.Example.com": false, // Create lowercases first
		"*.example.com.":  false, "example.com/x": false, "": false,
		"203.0.113.5": false, "*.0.113.5": false, "web.203.0.113.5.sslip.io": true, "1password.com": true,
	} {
		if got := ValidHostname(h); got != want {
			t.Errorf("ValidHostname(%q) = %v, want %v", h, got, want)
		}
	}
}
