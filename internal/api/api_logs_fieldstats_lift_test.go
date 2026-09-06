package api

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.10.509 (C5) — lift bayrağı anahtara girer; seçim eşiği ERROR (17) ama
// sayfada daha yüksek seviye seçiliyse korunur; handler iki fieldstats
// koşturur ve tabanı degraded ile zarflar.
func TestLogsFieldStatsLiftKeyAndSelection(t *testing.T) {
	f := logstore.Filter{Service: "svc"}
	if logsFieldStatsKeyLift("pod", f, "1", "2", 5, false) == logsFieldStatsKeyLift("pod", f, "1", "2", 5, true) {
		t.Fatal("errorLift anahtara girmeli")
	}
	if logsFieldStatsKey("pod", f, "1", "2", 5) != logsFieldStatsKeyLift("pod", f, "1", "2", 5, false) {
		t.Fatal("eski anahtar = lift=false anahtarı")
	}
	if got := logsErrorLiftSelection(f).SeverityMin; got != 17 {
		t.Fatalf("seçim eşiği 17 olmalı, %d", got)
	}
	if got := logsErrorLiftSelection(logstore.Filter{SeverityMin: 21}).SeverityMin; got != 21 {
		t.Fatalf("daha yüksek seçili seviye korunmalı, %d", got)
	}
	src, err := os.ReadFile("api_logs.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"s.logs.FieldStats(tctx, logsErrorLiftSelection(f), field, size)",
		"s.logs.FieldStats(tctx, f, field, 20)",
		"logstore.LiftFieldStats(sel, base)",
		"out.ErrorLift.Degraded, out.ErrorLift.Reason = true",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("handler missing %q", want)
		}
	}
}
