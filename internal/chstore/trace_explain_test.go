package chstore

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// v0.10.326 — explain kaydı: nil alıcı sessiz; adım SQL/arg/süre/satır/hata
// taşır; GetTraces'in ham yolu adımları kaydeder (kaynak pini).
func TestTraceExplainStepAndNil(t *testing.T) {
	var nilX *TraceExplain
	nilX.note("x")
	nilX.step("a", "SELECT 1", nil, time.Now(), 0, nil) // panic yok
	x := &TraceExplain{}
	x.note("path=%s", "raw-list")
	from := time.Date(2026, 9, 3, 11, 20, 0, 0, time.UTC)
	x.step("list", "SELECT trace_id\n\t\tFROM spans WHERE time >= ? AND service_name = ?", []any{from, "svc", strings.Repeat("y", 200)}, time.Now().Add(-5*time.Millisecond), 51, errors.New("boom"))
	if len(x.Notes) != 1 || x.Notes[0] != "path=raw-list" {
		t.Errorf("notes: %v", x.Notes)
	}
	if len(x.Steps) != 1 {
		t.Fatalf("steps: %+v", x.Steps)
	}
	s := x.Steps[0]
	if s.SQL != "SELECT trace_id FROM spans WHERE time >= ? AND service_name = ?" || s.Rows != 51 || s.Err != "boom" || s.Ms < 4 {
		t.Errorf("step: %+v", s)
	}
	if s.Args[0] != "2026-09-03T11:20:00Z" || s.Args[1] != "svc" || !strings.HasSuffix(s.Args[2], "…") {
		t.Errorf("args: %v", s.Args)
	}
}

func TestGetTracesRecordsExplainSteps(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, w := range []string{`f.Explain.step("list"`, `f.Explain.step("light-stage1"`, `f.Explain.step("light-stage2"`, `f.Explain.note("path=mv`, `f.Explain.note("path=raw-list`, `f.Explain.note("path=light`, `f.Explain.note("path=probe`} {
		if !strings.Contains(src, w) {
			t.Errorf("repo.go'da %s yok — teşhis kaydı eksik", w)
		}
	}
}
