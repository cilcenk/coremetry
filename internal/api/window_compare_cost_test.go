package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.444 — pencere kıyası yalnız RED okur: buildServiceContext pencere
// başına 8 okuma (ham exception taraması, komşu örnekleme…) yapıyordu.
func TestWindowCompareReadsOnlyRED(t *testing.T) {
	b, err := os.ReadFile("copilot_guided.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Server) guidedWindowCompareBundle(")
	if i < 0 {
		t.Fatal("bundle yok")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}\n"); j > 0 {
		body = body[:j]
	}
	if strings.Contains(body, "buildServiceContext(") || !strings.Contains(body, "GetServiceSummary5m(") {
		t.Fatal("window_compare buildServiceContext yerine GetServiceSummary5m + aggRED kullanmalı")
	}
}
