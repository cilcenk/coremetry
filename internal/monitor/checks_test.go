package monitor

// Table-driven tests for the pure decision cores of the tcp / ssl-cert /
// keyword synthetic-monitor types (v0.8.283). The probe I/O in runner.go is
// a thin shell around these — dial/handshake/GET, then hand the raw
// observation (NotAfter, body, target string) to one of these functions.
//
// Cert-expiry in particular is a value+unit shape (NotAfter vs now → whole
// days) so per the v0.6.36 unit-mixing rule every branch (expired / inside
// warn window / healthy / exact-boundary) is exercised here at ship time.

import (
	"strings"
	"testing"
	"time"
)

func TestEvalCertExpiry(t *testing.T) {
	// Fixed "now" so the day math is deterministic.
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	cases := []struct {
		name     string
		notAfter time.Time
		warnDays uint16
		wantUp   bool
		wantDays int64
	}{
		{"healthy far future", now.Add(90 * day), 14, true, 90},
		{"exactly at warn threshold is still up", now.Add(14 * day), 14, true, 14},
		{"one day inside warn window is down", now.Add(13 * day), 14, false, 13},
		{"expires tomorrow, down", now.Add(1 * day), 14, false, 1},
		{"expires in hours (same day) rounds to 0, down", now.Add(6 * time.Hour), 14, false, 0},
		{"already expired yesterday, down, negative days", now.Add(-1 * day), 14, false, -1},
		{"long expired, down", now.Add(-40 * day), 14, false, -40},
		{"custom low threshold healthy", now.Add(10 * day), 7, true, 10},
		{"custom low threshold breached", now.Add(5 * day), 7, false, 5},
		{"threshold 1 healthy", now.Add(2 * day), 1, true, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, days, msg := evalCertExpiry(c.notAfter, now, c.warnDays)
			wantStatus := "down"
			if c.wantUp {
				wantStatus = "up"
			}
			if status != wantStatus {
				t.Errorf("status = %q, want %q (msg=%q)", status, wantStatus, msg)
			}
			if days != c.wantDays {
				t.Errorf("daysRemaining = %d, want %d", days, c.wantDays)
			}
			if msg == "" {
				t.Errorf("expected a non-empty human message")
			}
		})
	}
}

func TestEvalKeyword(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		keyword string
		invert  bool
		wantUp  bool
	}{
		{"present, must-contain, up", "all systems operational", "operational", false, true},
		{"absent, must-contain, down", "everything on fire", "operational", false, false},
		{"present, must-not-contain, down", "FATAL: disk full", "FATAL", true, false},
		{"absent, must-not-contain, up", "healthy", "FATAL", true, true},
		{"case sensitive miss", "OPERATIONAL", "operational", false, false},
		{"substring match", "prefix-ok-suffix", "ok", false, true},
		{"empty body must-contain down", "", "ok", false, false},
		{"empty body must-not-contain up", "", "FATAL", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up, msg := evalKeyword(c.body, c.keyword, c.invert)
			if up != c.wantUp {
				t.Errorf("up = %v, want %v (msg=%q)", up, c.wantUp, msg)
			}
			if msg == "" {
				t.Errorf("expected a non-empty human message")
			}
		})
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		name        string
		target      string
		defaultPort string
		want        string
		wantErr     bool
	}{
		{"host with default port appended", "example.com", "443", "example.com:443", false},
		{"host with explicit port kept", "example.com:8443", "443", "example.com:8443", false},
		{"https url stripped to host:defaultport", "https://api.example.com/health", "443", "api.example.com:443", false},
		{"url with explicit port and path", "https://api.example.com:9443/x", "443", "api.example.com:9443", false},
		{"tcp requires explicit port (no default)", "db.internal", "", "", true},
		{"tcp with explicit port ok", "db.internal:5432", "", "db.internal:5432", false},
		{"whitespace trimmed", "  example.com  ", "443", "example.com:443", false},
		{"empty target errors", "", "443", "", true},
		{"port zero rejected", "example.com:0", "443", "", true},
		{"port too high rejected", "example.com:70000", "443", "", true},
		{"non-numeric port rejected", "example.com:https", "443", "", true},
		{"ipv6 with port kept", "[2606:4700::1111]:443", "443", "[2606:4700::1111]:443", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeAddr(c.target, c.defaultPort)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got %q", c.target, got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != c.want {
				t.Errorf("NormalizeAddr(%q, %q) = %q, want %q", c.target, c.defaultPort, got, c.want)
			}
		})
	}
}

// Guard the invariant the runner relies on: the message strings carry the
// day count so the timeline tooltip is self-describing even without the
// structured detail column.
func TestEvalCertExpiryMessageMentionsDays(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	_, _, msg := evalCertExpiry(now.Add(3*24*time.Hour), now, 14)
	if !strings.Contains(msg, "3") {
		t.Errorf("message %q should mention the day count", msg)
	}
}
