package evalrubric

// run.go — koşum artefaktı + iki koşumun diff'i (Faz A: skor geçmişi).
//
// Artefakt JSON'dur ve REPO DIŞI bir dizine yazılır (COREMETRY_EVAL_OUT;
// varsayılan ./evalset-runs, gitignore'lu): replay CH'ye ya da ai_calls'a
// bağımlı olmamalı — "evalset bir geliştiricinin gerçek anahtarına asla
// ateşlenmez, kayıt bellek içi" sözleşmesi (evalset_test.go başlığı) korunur.
// Dosya adı prompt_version + model taşır: farklı prompt/model koşumları
// yan yana durur, diff kıyaslanabilir olanı seçer.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CaseResult — bir vakanın koşum sonucu.
type CaseResult struct {
	ID        string   `json:"id"`
	Surface   string   `json:"surface"`
	OK        bool     `json:"ok"`        // ikili kapı (kalkan/beklenti) geçti mi
	LatencyMs int64    `json:"latencyMs"` // metrik, kapı değil
	Fails     []string `json:"fails,omitempty"`
	Rubric    Result   `json:"rubric"`
}

// Summary — koşum özeti.
type Summary struct {
	Cases      int     `json:"cases"`
	Pass       int     `json:"pass"`
	Fail       int     `json:"fail"`
	Skipped    int     `json:"skipped"`
	RubricMean float64 `json:"rubricMean"` // uygulanan vakaların Total ortalaması
	BelowThr   int     `json:"belowThreshold"`
}

// Run — koşum artefaktı.
type Run struct {
	Schema        string       `json:"schema"` // coremetry.evalset-run/1
	At            time.Time    `json:"at"`
	PromptVersion string       `json:"promptVersion"`
	Model         string       `json:"model"`
	Provider      string       `json:"provider,omitempty"`
	Cases         []CaseResult `json:"cases"`
	Summary       Summary      `json:"summary"`
}

const RunSchema = "coremetry.evalset-run/1"

// Summarize — Cases'ten Summary üretir (skipped çağırandan gelir).
func Summarize(cases []CaseResult, skipped int) Summary {
	s := Summary{Cases: len(cases) + skipped, Skipped: skipped}
	var acc float64
	n := 0
	for _, c := range cases {
		if c.OK {
			s.Pass++
		} else {
			s.Fail++
		}
		if c.Rubric.Applicable > 0 {
			acc += c.Rubric.Total
			n++
			if c.Rubric.Total < Threshold {
				s.BelowThr++
			}
		}
	}
	if n > 0 {
		s.RubricMean = round3(acc / float64(n))
	}
	return s
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// RunFileName — evalset-<unixsec>-<promptVersion>-<model>.json (güvenli karakterler).
func RunFileName(r Run) string {
	pv := unsafeName.ReplaceAllString(r.PromptVersion, "_")
	m := unsafeName.ReplaceAllString(r.Model, "_")
	if pv == "" {
		pv = "nopv"
	}
	if m == "" {
		m = "nomodel"
	}
	return fmt.Sprintf("evalset-%d-%s-%s.json", r.At.Unix(), pv, m)
}

// WriteRun — dizini yaratır, artefaktı yazar, yolu döner.
func WriteRun(dir string, r Run) (string, error) {
	if r.Schema == "" {
		r.Schema = RunSchema
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, RunFileName(r))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// ReadRun — artefaktı okur; şema uyuşmazlığı hata.
func ReadRun(path string) (Run, error) {
	var r Run
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if r.Schema != RunSchema {
		return r, fmt.Errorf("beklenmeyen şema %q (istenen %s)", r.Schema, RunSchema)
	}
	return r, nil
}

// CaseDelta — iki koşum arasında bir vaka.
type CaseDelta struct {
	ID       string  `json:"id"`
	Before   float64 `json:"before"`
	After    float64 `json:"after"`
	Delta    float64 `json:"delta"`
	OKBefore bool    `json:"okBefore"`
	OKAfter  bool    `json:"okAfter"`
}

// DiffReport — B (sonra) − A (önce).
type DiffReport struct {
	Comparable   bool        `json:"comparable"` // aynı model; prompt farkı beklenen kıyas
	Note         string      `json:"note,omitempty"`
	MeanBefore   float64     `json:"meanBefore"`
	MeanAfter    float64     `json:"meanAfter"`
	MeanDelta    float64     `json:"meanDelta"`
	Regressed    []CaseDelta `json:"regressed"`    // delta < 0
	Improved     []CaseDelta `json:"improved"`     // delta > 0
	NewlyFailing []string    `json:"newlyFailing"` // ok→FAIL
	NewlyPassing []string    `json:"newlyPassing"` // FAIL→ok
	OnlyInBefore []string    `json:"onlyInBefore"`
	OnlyInAfter  []string    `json:"onlyInAfter"`
}

// Diff — vaka kimliğiyle eşleştirir. Model farklıysa Comparable=false (yine
// raporlar: model A/B'si de meşru bir soru, ama prompt kıyası DEĞİL).
func Diff(before, after Run) DiffReport {
	rep := DiffReport{Comparable: before.Model == after.Model, MeanBefore: before.Summary.RubricMean, MeanAfter: after.Summary.RubricMean}
	rep.MeanDelta = round3(after.Summary.RubricMean - before.Summary.RubricMean)
	if !rep.Comparable {
		rep.Note = "model farklı (" + before.Model + " → " + after.Model + "): prompt kıyası değil, model A/B'si"
	} else if before.PromptVersion == after.PromptVersion {
		rep.Note = "aynı prompt_version: fark = gürültü tabanı (≤ 0,05 beklenir)"
	}
	bm := map[string]CaseResult{}
	for _, c := range before.Cases {
		bm[c.ID] = c
	}
	am := map[string]CaseResult{}
	for _, c := range after.Cases {
		am[c.ID] = c
	}
	for id, b := range bm {
		a, ok := am[id]
		if !ok {
			rep.OnlyInBefore = append(rep.OnlyInBefore, id)
			continue
		}
		d := CaseDelta{ID: id, Before: b.Rubric.Total, After: a.Rubric.Total, Delta: round3(a.Rubric.Total - b.Rubric.Total), OKBefore: b.OK, OKAfter: a.OK}
		if d.Delta < 0 {
			rep.Regressed = append(rep.Regressed, d)
		} else if d.Delta > 0 {
			rep.Improved = append(rep.Improved, d)
		}
		if b.OK && !a.OK {
			rep.NewlyFailing = append(rep.NewlyFailing, id)
		}
		if !b.OK && a.OK {
			rep.NewlyPassing = append(rep.NewlyPassing, id)
		}
	}
	for id := range am {
		if _, ok := bm[id]; !ok {
			rep.OnlyInAfter = append(rep.OnlyInAfter, id)
		}
	}
	sort.Slice(rep.Regressed, func(i, j int) bool { return rep.Regressed[i].Delta < rep.Regressed[j].Delta })
	sort.Slice(rep.Improved, func(i, j int) bool { return rep.Improved[i].Delta > rep.Improved[j].Delta })
	sort.Strings(rep.NewlyFailing)
	sort.Strings(rep.NewlyPassing)
	sort.Strings(rep.OnlyInBefore)
	sort.Strings(rep.OnlyInAfter)
	return rep
}

// FormatDiff — insan okur metin (evalsetdiff CLI + test log).
func FormatDiff(before, after Run, rep DiffReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "evalset diff: %s (%s, %s) → %s (%s, %s)\n",
		before.At.Format(time.RFC3339), before.PromptVersion, before.Model,
		after.At.Format(time.RFC3339), after.PromptVersion, after.Model)
	if rep.Note != "" {
		fmt.Fprintf(&sb, "  not: %s\n", rep.Note)
	}
	fmt.Fprintf(&sb, "  rubrik ortalaması: %.3f → %.3f (Δ %+.3f)  pass %d→%d  fail %d→%d\n",
		rep.MeanBefore, rep.MeanAfter, rep.MeanDelta, before.Summary.Pass, after.Summary.Pass, before.Summary.Fail, after.Summary.Fail)
	for _, d := range rep.Regressed {
		fmt.Fprintf(&sb, "  ▼ %-40s %.3f → %.3f (%+.3f)\n", d.ID, d.Before, d.After, d.Delta)
	}
	for _, d := range rep.Improved {
		fmt.Fprintf(&sb, "  ▲ %-40s %.3f → %.3f (%+.3f)\n", d.ID, d.Before, d.After, d.Delta)
	}
	if len(rep.NewlyFailing) > 0 {
		fmt.Fprintf(&sb, "  yeni FAIL: %s\n", strings.Join(rep.NewlyFailing, ", "))
	}
	if len(rep.NewlyPassing) > 0 {
		fmt.Fprintf(&sb, "  yeni ok:   %s\n", strings.Join(rep.NewlyPassing, ", "))
	}
	if len(rep.OnlyInBefore) > 0 || len(rep.OnlyInAfter) > 0 {
		fmt.Fprintf(&sb, "  yalnız öncede: %v · yalnız sonrada: %v\n", rep.OnlyInBefore, rep.OnlyInAfter)
	}
	return sb.String()
}

func round3(f float64) float64 { return float64(int64(f*1000+copysign(0.5, f))) / 1000 }

func copysign(x, y float64) float64 {
	if y < 0 {
		return -x
	}
	return x
}
