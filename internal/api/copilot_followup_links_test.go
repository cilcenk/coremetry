package api

import (
	"os"
	"strings"
	"testing"
)

// v0.9.1130 regresyon — guided çiplerinin /logs linki `minSev=17`
// basıyordu; /logs okuyucusu `severity` (logsUrl.ts). Ölü param =
// çip "error logları" vaat edip TÜM seviyeleri açıyordu (K4 sınıfı,
// v0.9.855'in öldürdüğü hatanın kardeşi). İki yönlü pin: yasak ad
// hiçbir yerde, /logs linklerinde doğru ad var.
func TestFollowupLogLinksUseSeverityParam(t *testing.T) {
	b, err := os.ReadFile("copilot_followup.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "minSev") {
		t.Error("copilot_followup.go hâlâ `minSev` içeriyor — /logs bu adı okumaz; `severity` kullan")
	}
	if !strings.Contains(src, "/logs?service=\" + svcQ + \"&severity=17") {
		t.Error("servisli error-log çipi `severity=17` taşımalı")
	}
}
