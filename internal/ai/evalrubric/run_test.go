package evalrubric

// run_test.go — artefakt yazma/okuma gidiş-dönüşü, dosya adı, özet ve diff.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkRun(pv, model string, at time.Time, cases ...CaseResult) Run {
	return Run{Schema: RunSchema, At: at, PromptVersion: pv, Model: model, Cases: cases, Summary: Summarize(cases, 0)}
}

func cr(id string, ok bool, total float64) CaseResult {
	return CaseResult{ID: id, Surface: "Problem", OK: ok, Rubric: Result{Total: total, Applicable: 3}}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]CaseResult{cr("a", true, 1), cr("b", false, 0.5), {ID: "c", OK: true}}, 2)
	if s.Cases != 5 || s.Pass != 2 || s.Fail != 1 || s.Skipped != 2 {
		t.Fatalf("sayım: %+v", s)
	}
	if s.RubricMean != 0.75 || s.BelowThr != 1 {
		t.Fatalf("yalnız uygulanabilir vakalar ortalamaya girer (1+0.5)/2=0.75, eşik altı 1: %+v", s)
	}
}

func TestWriteReadRunRoundtrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	r := mkRun("pv-abc/1", "qwen3:8b", at, cr("problem-x", true, 0.9))
	p, err := WriteRun(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "evalset-1789120800-pv-abc_1-qwen3_8b.json" {
		t.Fatalf("dosya adı güvenli karakterlerle prompt+model taşımalı: %s", filepath.Base(p))
	}
	back, err := ReadRun(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.PromptVersion != r.PromptVersion || back.Model != r.Model || len(back.Cases) != 1 || back.Cases[0].Rubric.Total != 0.9 {
		t.Fatalf("gidiş-dönüş: %+v", back)
	}
}

func TestReadRunRejectsForeignSchema(t *testing.T) {
	dir := t.TempDir()
	r := mkRun("pv", "m", time.Now())
	r.Schema = "başka/1"
	p, _ := WriteRun(dir, r) // WriteRun boş şemayı doldurur ama yabancı şemayı korur
	if _, err := ReadRun(p); err == nil {
		t.Fatal("yabancı şema reddedilmeli")
	}
}

func TestDiffReport(t *testing.T) {
	at := time.Now()
	before := mkRun("pv1", "m", at, cr("a", true, 1.0), cr("b", true, 0.8), cr("c", false, 0.2), cr("old", true, 1))
	after := mkRun("pv2", "m", at.Add(time.Hour), cr("a", true, 0.7), cr("b", true, 0.8), cr("c", true, 0.9), cr("new", true, 1))
	rep := Diff(before, after)
	if !rep.Comparable || rep.Note != "" {
		t.Fatalf("aynı model, farklı prompt → kıyaslanabilir, notsuz: %+v", rep)
	}
	if len(rep.Regressed) != 1 || rep.Regressed[0].ID != "a" || rep.Regressed[0].Delta != -0.3 {
		t.Fatalf("gerileyen a (−0.3): %+v", rep.Regressed)
	}
	if len(rep.Improved) != 1 || rep.Improved[0].ID != "c" {
		t.Fatalf("iyileşen c: %+v", rep.Improved)
	}
	if len(rep.NewlyPassing) != 1 || rep.NewlyPassing[0] != "c" || len(rep.NewlyFailing) != 0 {
		t.Fatalf("yeni ok c: %+v / %+v", rep.NewlyPassing, rep.NewlyFailing)
	}
	if strings.Join(rep.OnlyInBefore, ",") != "old" || strings.Join(rep.OnlyInAfter, ",") != "new" {
		t.Fatalf("yalnız-bir-tarafta: %v / %v", rep.OnlyInBefore, rep.OnlyInAfter)
	}
	out := FormatDiff(before, after, rep)
	for _, want := range []string{"▼ a", "▲ c", "yeni ok:   c", "yalnız öncede: [old]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rapor metni %q içermeli:\n%s", want, out)
		}
	}
	// Model farkı: kıyaslanabilir DEĞİL (model A/B), aynı prompt: gürültü notu.
	other := mkRun("pv1", "m2", at)
	if Diff(before, other).Comparable {
		t.Fatal("farklı model → Comparable=false")
	}
	same := mkRun("pv1", "m", at.Add(time.Minute), cr("a", true, 0.98))
	if n := Diff(before, same).Note; !strings.Contains(n, "gürültü") {
		t.Fatalf("aynı prompt_version → gürültü notu: %q", n)
	}
}
