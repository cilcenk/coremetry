// Package perfcheck — performans bütçesinin SAF çekirdeği (v0.10.116,
// docs/perf/perf-budget-2026-08-28.md AŞAMA 3).
//
// cmd/perfcheck ölçer (HTTP + httptrace), bu paket karar verir:
// yüzdelik, bütçe eşiği, önceki koşuyla kıyas, veri-seti sapması. Ağ
// YOK, saat YOK — her karar tablo-testli. Aynı JSON şekli hem çıktı hem
// kıyas girdisidir, yani "önceki koşu" = bir önceki çıktı dosyası.
//
// Karar kuralı (operatör onaylı, 2026-08-28):
//   - karar MEDYAN üzerinden (p95 bilgi amaçlı — 5 koşuda p95 = max,
//     gürültüyü ölçer, bütçeyi değil);
//   - FAIL = medyan ulaşılabilir eşiği aşıyor VE (önceki koşu yok YA DA
//     önceki medyana göre +tolerans% üstünde) — yani hem "yavaş" hem
//     "daha da yavaşladı";
//   - WARN = ikisinden yalnız biri;
//   - veri-seti (24h span sayısı) önceki koşudan %drift'ten çok sapmışsa
//     kıyas UYARIya düşer, fail üretmez — farklı veriyle kıyas yalan söyler.
package perfcheck

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Budget — bir ölçüm noktasının eşikleri (ms; 0 = eşik yok).
type Budget struct {
	AchievableMs   float64 `json:"achievableMs"`
	AspirationalMs float64 `json:"aspirationalMs,omitempty"`
	// MaxBytes — gövde boyutu tavanı (0 = yok); dashboard bundle gibi
	// "hızlı ama 1,7 MB" noktaları için.
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// Point — budget.json'daki bir ölçüm noktası. Name = self-telemetry span
// adı (`GET /api/traces`) — spanmetrics_1m.name ile AYNI yazım, prod
// p95'iyle aynı tabloda kıyaslanabilsin. Scenario = senaryo eki.
type Point struct {
	Name     string `json:"name"`
	Scenario string `json:"scenario"`
	Method   string `json:"method,omitempty"` // boş = GET
	// Path — sorgu dizisi dahil; {from} {to} (ns), {service}, {traceIds}
	// yer tutucuları cmd tarafında doldurulur.
	Path string `json:"path"`
	// Window — {from}/{to} için pencere ("1h", "6h", "24h").
	Window string `json:"window,omitempty"`
	// BodyFromDashboard — POST gövdesi bu dashboard'un panellerinden
	// kurulur (cmd tarafı); boş = gövdesiz.
	BodyFromDashboard string `json:"bodyFromDashboard,omitempty"`
	// Cold — refresh=1 ile soğuk yol (X-Cache BYPASS beklenir).
	Cold   bool   `json:"cold"`
	Budget Budget `json:"budget"`
	Note   string `json:"note,omitempty"`
}

// Sample — bir koşunun ölçümü.
type Sample struct {
	TTFBMs float64 `json:"ttfbMs"`
	Status int     `json:"status"`
	Bytes  int64   `json:"bytes"`
	XCache string  `json:"xcache,omitempty"`
}

// Stats — koşuların özeti.
type Stats struct {
	N     int     `json:"n"`
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	MinMs float64 `json:"minMs"`
	MaxMs float64 `json:"maxMs"`
}

// Result — bir noktanın sonucu (çıktı JSON'unun `points[]` öğesi).
type Result struct {
	Name     string   `json:"name"`
	Scenario string   `json:"scenario"`
	Cold     Stats    `json:"cold"`
	Warm     *Stats   `json:"warm,omitempty"`
	Bytes    int64    `json:"bytes"`
	XCache   []string `json:"xcache,omitempty"`
	Budget   Budget   `json:"budget"`
	Status   string   `json:"status"` // pass | warn | fail | invalid
	Reason   string   `json:"reason,omitempty"`
	DeltaPct *float64 `json:"deltaPct,omitempty"` // önceki koşuya göre soğuk p50 farkı
	Samples  []Sample `json:"samples,omitempty"`
}

// Dataset — ölçüm anındaki veri-seti parmak izi.
type Dataset struct {
	Spans24h int64  `json:"spans24h"`
	Services int    `json:"services"`
	Version  string `json:"version"`
}

// Report — çıktı dosyasının tamamı.
type Report struct {
	Schema    int      `json:"schema"`
	StartedAt string   `json:"startedAt"`
	BaseURL   string   `json:"baseUrl"`
	Runs      int      `json:"runs"`
	Dataset   Dataset  `json:"dataset"`
	Points    []Result `json:"points"`
	Summary   Summary  `json:"summary"`
	Notes     []string `json:"notes,omitempty"`
}

// Summary — sayım + çıkış kodu kararı.
type Summary struct {
	Pass    int  `json:"pass"`
	Warn    int  `json:"warn"`
	Fail    int  `json:"fail"`
	Invalid int  `json:"invalid"`
	OK      bool `json:"ok"`
}

// Rules — karar parametreleri (budget.json başlığı).
type Rules struct {
	TolerancePct        float64 `json:"tolerancePct"`        // önceki koşuya göre izin verilen artış
	DatasetDriftWarnPct float64 `json:"datasetDriftWarnPct"` // veri-seti sapma eşiği
}

// Key — bir noktanın kimliği (kıyas eşlemesi için).
func (p Point) Key() string { return p.Name + " · " + p.Scenario }

// Key — Result için aynı kimlik.
func (r Result) Key() string { return r.Name + " · " + r.Scenario }

// Summarize — TTFB'lerden Stats. Boş → sıfır. Yüzdelik en-yakın-sıra
// (nearest-rank): 5 koşuda p95 = max, 20 koşuda 19. sıra.
func Summarize(ttfbMs []float64) Stats {
	if len(ttfbMs) == 0 {
		return Stats{}
	}
	s := append([]float64(nil), ttfbMs...)
	sort.Float64s(s)
	return Stats{
		N:     len(s),
		P50Ms: Percentile(s, 50),
		P95Ms: Percentile(s, 95),
		MinMs: s[0],
		MaxMs: s[len(s)-1],
	}
}

// Percentile — SIRALI dilimde nearest-rank yüzdelik. p ∈ (0,100].
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// DatasetDriftPct — 24h span sayısının önceki koşuya göre mutlak sapma
// yüzdesi; önceki bilinmiyorsa 0.
func DatasetDriftPct(cur, prev Dataset) float64 {
	if prev.Spans24h <= 0 || cur.Spans24h <= 0 {
		return 0
	}
	return math.Abs(float64(cur.Spans24h)-float64(prev.Spans24h)) / float64(prev.Spans24h) * 100
}

// Evaluate — bir noktanın kararı. prev nil = ilk koşu (kıyas yok).
// driftWarn=true → kıyas güvenilmez: fail yerine warn, gerekçede yazar.
//
// Karar tablosu:
//
//	geçerli değil (soğuk koşuda BYPASS yok / 2xx değil) → invalid
//	p50 ≤ eşik  ve  (prev yok | Δ ≤ tol)                → pass
//	p50 > eşik  ve  (prev yok | Δ > tol)                → fail
//	yalnız biri                                         → warn
//	gövde > MaxBytes                                    → en az warn (eşikle birlikte fail)
//	driftWarn                                           → fail → warn (kıyas güvenilmez)
func Evaluate(r Result, prev *Result, rules Rules, driftWarn bool) Result {
	if r.Status == "invalid" {
		return r
	}
	var reasons []string
	overBudget := r.Budget.AchievableMs > 0 && r.Cold.P50Ms > r.Budget.AchievableMs
	if overBudget {
		reasons = append(reasons, fmt.Sprintf("soğuk p50 %.0f ms > ulaşılabilir %.0f ms", r.Cold.P50Ms, r.Budget.AchievableMs))
	}
	regressed := false
	if prev != nil && prev.Cold.P50Ms > 0 {
		delta := (r.Cold.P50Ms - prev.Cold.P50Ms) / prev.Cold.P50Ms * 100
		d := math.Round(delta*10) / 10
		r.DeltaPct = &d
		if delta > rules.TolerancePct {
			regressed = true
			reasons = append(reasons, fmt.Sprintf("önceki p50 %.0f ms → %+.0f%% (tolerans %.0f%%)", prev.Cold.P50Ms, delta, rules.TolerancePct))
		}
	}
	overBytes := r.Budget.MaxBytes > 0 && r.Bytes > r.Budget.MaxBytes
	if overBytes {
		reasons = append(reasons, fmt.Sprintf("gövde %d B > tavan %d B", r.Bytes, r.Budget.MaxBytes))
	}
	switch {
	case overBudget && (prev == nil || regressed):
		r.Status = "fail"
	case overBudget || regressed || overBytes:
		r.Status = "warn"
	default:
		r.Status = "pass"
	}
	if overBudget && overBytes && r.Status == "warn" {
		r.Status = "fail"
	}
	if r.Status == "fail" && driftWarn {
		r.Status = "warn"
		reasons = append(reasons, "veri-seti önceki koşudan sapmış — kıyas güvenilmez, fail warn'a indirildi")
	}
	if r.Budget.AspirationalMs > 0 && r.Cold.P50Ms <= r.Budget.AspirationalMs {
		reasons = append(reasons, "arzu edilen eşiğin altında")
	}
	r.Reason = strings.Join(reasons, "; ")
	return r
}

// ValidateCold — soğuk koşu sözleşmesi: her örnek 2xx ve X-Cache BYPASS
// (serveCached'siz uçlarda X-Cache yok — boş kabul). Bozuksa (status,
// gerekçe) ile invalid.
func ValidateCold(samples []Sample, expectBypass bool) (string, string) {
	for i, s := range samples {
		if s.Status < 200 || s.Status >= 300 {
			return "invalid", fmt.Sprintf("koşu %d HTTP %d", i+1, s.Status)
		}
		if expectBypass && s.XCache != "" && s.XCache != "BYPASS" {
			return "invalid", fmt.Sprintf("koşu %d X-Cache=%s (soğuk ölçüm BYPASS ister — refresh=1 düşmüş olabilir)", i+1, s.XCache)
		}
	}
	return "", ""
}

// Tally — özet + çıkış kararı: fail varsa OK=false; invalid nokta da
// başarısızlıktır (ölçülemeyen bütçe "geçti" sayılamaz).
func Tally(points []Result) Summary {
	var s Summary
	for _, p := range points {
		switch p.Status {
		case "pass":
			s.Pass++
		case "warn":
			s.Warn++
		case "fail":
			s.Fail++
		default:
			s.Invalid++
		}
	}
	s.OK = s.Fail == 0 && s.Invalid == 0
	return s
}

// IndexPrev — önceki raporun noktalarını kimliğe göre indeksler.
func IndexPrev(prev *Report) map[string]Result {
	out := map[string]Result{}
	if prev == nil {
		return out
	}
	for _, p := range prev.Points {
		out[p.Key()] = p
	}
	return out
}

// Lines — terminal özeti: nokta başına tek satır, insan okur.
func Lines(rep Report) []string {
	var out []string
	for _, p := range rep.Points {
		mark := map[string]string{"pass": "✓", "warn": "⚠", "fail": "✗", "invalid": "?"}[p.Status]
		delta := ""
		if p.DeltaPct != nil {
			delta = fmt.Sprintf(" Δ%+.0f%%", *p.DeltaPct)
		}
		line := fmt.Sprintf("%s %-26s %-28s soğuk p50 %6.0f ms  p95 %6.0f ms%s  bütçe %.0f/%.0f  %d B",
			mark, p.Name, p.Scenario, p.Cold.P50Ms, p.Cold.P95Ms, delta, p.Budget.AchievableMs, p.Budget.AspirationalMs, p.Bytes)
		if p.Warm != nil {
			line += fmt.Sprintf("  sıcak p50 %.0f ms", p.Warm.P50Ms)
		}
		if p.Reason != "" {
			line += "  — " + p.Reason
		}
		out = append(out, line)
	}
	return out
}
