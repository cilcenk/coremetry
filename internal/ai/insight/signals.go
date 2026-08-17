package insight

import (
	"fmt"
	"math"
	"strings"
)

// signals.go — kanıt → Signal projeksiyonu. TAMAMI SAF: girdi düz
// veri, çıktı düz veri, hiçbir store/HTTP/LLM dokunuşu yok. Sebep, Faz
// 2.1'in kabul kriteri: kartın deterministik yarısı LLM olmadan da
// doğru olmak zorunda, dolayısıyla o yarı table-testlenebilir olmalı.
//
// Kanıt yapıları (ExceptionEvidence / ProblemEvidence) chstore tiplerinin
// KOPYASI DEĞİL, projeksiyonu: yalnız karta giren alanlar. Böylece bu
// paket chstore'a bağlanmıyor ve testler dev fixture kurmadan yazılıyor;
// chstore → evidence dönüşümü internal/api'de (kompozisyon kökü) yaşıyor.

// maxListedNames — liste-şekilli bir sinyalde gösterilen ad sayısı
// (komşu adayları, çağıranlar). Tavana takılan projeksiyon
// Response.Truncated'ı kaldırır — "hepsi bu mu" sorusu sorulmadan
// cevaplanır.
const maxListedNames = 3

// ── exception kanıtı ────────────────────────────────────────────────

// DeployCandidate — grubun başlangıcı çevresinde seçilmiş deploy.
//
// OffsetSec MUTLAK uzaklık (≥0) ve yön AYRI bir bayrak. İşaretle
// kodlamak cazip ama yanlış: sub-saniye bir "sonra" deploy'u
// (ns→sn kırpması) 0'a düşer ve işaret 0'da yön TAŞIMAZ. Yön seçimi
// yapan yer (anomaly.PickDeploysAroundStartRefs) biliyor, tahmine
// bırakmıyoruz — After=true olan aday KÖK NEDEN OLAMAZ.
type DeployCandidate struct {
	Version   string
	OffsetSec int64
	After     bool
}

// ExceptionEvidence — exception grubu kartının deterministik girdisi.
// Alanların hepsi anomaly.BuildExceptionExplainInput'un ZATEN okuduğu
// veriden gelir; kart için ikinci bir sorgu YOK (yeniden okumak, modelin
// gördüğü deploy/stack ile kartın gösterdiğinin ayrışması demekti —
// exception_context.go'nun kendi gerekçesi).
type ExceptionEvidence struct {
	Fingerprint string
	Type        string
	Service     string
	State       string
	Occurrences uint64
	Last24      uint64
	PeakCount   uint64
	FirstSeenNs int64
	LastSeenNs  int64
	Deploys     []DeployCandidate
	TraceID     string
	// NowNs — bağıl süreleri hesaplayan referans an. Parametre olması
	// ŞART: time.Now() okuyan bir projeksiyon test edilemez ve
	// "şimdi"yi iki kez okuyup iki farklı yaş basma riski taşır.
	NowNs int64
}

// ExceptionSignals — exception grubunun deterministik satırları.
// Sıra SABİT (kart her açılışta aynı satırları aynı yerde gösterir).
// truncated her zaman false: bu projeksiyonda liste-şekilli sinyal yok
// (deploy adaylarından yalnız en yakın ÖNCE olanı bir sinyal olur ve bu
// kırpma değil, seçim — bkz. aşağıdaki gerekçe).
func ExceptionSignals(ev ExceptionEvidence) (out []Signal, truncated bool) {
	add := func(kind, label, value, sev string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		out = append(out, Signal{Kind: kind, Label: label, Value: value, Severity: sev})
	}

	// Oluşum hacmi — kartın ilk satırı, çünkü triyajın ilk sorusu
	// "hâlâ oluyor mu, ne kadar".
	occ := fmtNum(ev.Occurrences)
	if ev.Last24 > 0 || ev.Occurrences > 0 {
		occ += " · son 24s " + fmtNum(ev.Last24)
	}
	if ev.PeakCount > 0 {
		occ += " · tepe " + fmtNum(ev.PeakCount)
	}
	occSev := SevOK
	if ev.Last24 > 0 {
		occSev = SevErr
	}
	add(SignalException, "Oluşum", occ, occSev)

	add(SignalException, "Tip", ev.Type, "")
	add(SignalGeneric, "Servis", ev.Service, "")

	// Yaş — BAĞIL (mutlak saat yasağı, contract.go). Son oluşum
	// tazeliği şiddeti belirler: 15dk içinde canlı, 6sa içinde sıcak.
	if ev.LastSeenNs > 0 && ev.NowNs >= ev.LastSeenNs {
		age := (ev.NowNs - ev.LastSeenNs) / 1e9
		sev := ""
		switch {
		case age <= 15*60:
			sev = SevErr
		case age <= 6*3600:
			sev = SevWarn
		}
		add(SignalGeneric, "Son oluşum", FmtDurTR(age)+" önce", sev)
	}
	if ev.FirstSeenNs > 0 && ev.NowNs >= ev.FirstSeenNs {
		add(SignalGeneric, "İlk görülme", FmtDurTR((ev.NowNs-ev.FirstSeenNs)/1e9)+" önce", "")
	}

	// Deploy — YALNIZ başlangıçtan ÖNCEKİ en yakın aday. Sonrasındaki
	// deploy'lar kök neden olamaz (prompt metni bunu açıkça söylüyor);
	// karta "Yakın deploy" diye basmak operatöre yanlış aday gösterirdi.
	if d, ok := NearestDeployBefore(ev.Deploys); ok {
		add(SignalDeploy, "Yakın deploy",
			d.Version+" · grup başlangıcından "+FmtDurTR(d.OffsetSec)+" önce", SevWarn)
	}
	return out, false
}

// NearestDeployBefore — başlangıçtan ÖNCEKİ (After=false) en YAKIN
// deploy. Saf; sonrası-adayları eler.
func NearestDeployBefore(cands []DeployCandidate) (DeployCandidate, bool) {
	best, found := DeployCandidate{}, false
	for _, c := range cands {
		if c.After || strings.TrimSpace(c.Version) == "" {
			continue
		}
		if !found || c.OffsetSec < best.OffsetSec {
			best, found = c, true
		}
	}
	return best, found
}

// ── problem kanıtı ──────────────────────────────────────────────────

// DeployRef — problemin yakın deploy'u + (varsa) ölçülmüş etkisi.
// Impact YALNIZ hipotez yolunda dolar (RecentDeploy.Impact, v0.9.1059);
// problems listesi enrichment'ı doldurmaz — HasImpact bu ayrımı taşır.
type DeployRef struct {
	Version     string
	AgeSec      int64
	HasImpact   bool
	P99DeltaPct float64
	ErrDeltaPP  float64
}

// HypothesisRef — kalıcı kök-neden hipotezinin kart yüzü.
//
// Others = top suspect DIŞINDAKİ toplam aday sayısı. Candidates o
// kümenin (belki kırpılmış) hâli; ikisi ayrı çünkü "+N" ancak GERÇEK
// toplamdan türetilirse dürüst olur. Top suspect'i saymayan bir sayı
// bilerek seçildi: "Diğer adaylar" satırı zaten onu listelemiyor,
// dahil eden bir toplam satırın kendi "+N"ini bir fazla gösterirdi.
type HypothesisRef struct {
	TopSuspect string
	Confidence float64
	Candidates []string
	Others     int
}

// BlastRef — etki alanının kart yüzü.
type BlastRef struct {
	TotalCallers     int
	CascadingCallers int
	TopCallers       []string
}

// OpRef — DeepEvidence'ın en yavaş operasyonu. Bugün HİÇBİR yüzeyde
// gösterilmiyordu (tasarım §1.6: "hesaplanıp hiç anlatılmayan"), kart
// onu deterministik satır olarak getiriyor.
type OpRef struct {
	Name      string
	P95Ms     float64
	ErrorRate float64
}

// ProblemEvidence — problem kartının deterministik girdisi.
type ProblemEvidence struct {
	ID             string
	Service        string
	Metric         string
	Severity       string
	Priority       string
	PriorityReason string
	Comparator     string
	Status         string
	Value          float64
	Threshold      float64
	StartedNs      int64
	ResolvedNs     int64 // 0 = açık
	FromNs         int64 // analiz penceresi (boundAnalysisWindow)
	ToNs           int64
	NowNs          int64
	Deploy         *DeployRef
	Hyp            *HypothesisRef
	Blast          *BlastRef
	SlowOp         *OpRef
}

// severityTR / priority şiddet eşlemeleri — kartın rengi problemin
// kendi şiddetinden ve triyaj basamağından türer, ikisi AYRI bilgi
// (v0.9.828 kapısının aynı ayrımı).
func severityTR(s string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return "kritik", SevErr
	case "warning", "warn":
		return "uyarı", SevWarn
	case "info":
		return "bilgi", ""
	case "":
		return "", ""
	default:
		return s, ""
	}
}

func prioritySev(p string) string {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "P1":
		return SevErr
	case "P2":
		return SevWarn
	default:
		return ""
	}
}

// ProblemSignals — problemin deterministik satırları, sıra sabit.
// truncated = liste-şekilli sinyallerden biri tavana takıldı.
func ProblemSignals(ev ProblemEvidence) (out []Signal, truncated bool) {
	add := func(kind, label, value, sev string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		out = append(out, Signal{Kind: kind, Label: label, Value: value, Severity: sev})
	}

	if v, sev := severityTR(ev.Severity); v != "" {
		add(SignalProblem, "Şiddet", v, sev)
	}
	if p := strings.TrimSpace(ev.Priority); p != "" {
		v := p
		if ev.PriorityReason != "" {
			v += " — " + ev.PriorityReason
		}
		add(SignalProblem, "Öncelik", v, prioritySev(p))
	}

	// İhlal — karşılaştırıcı VARSA yönüyle yazılır. Yön olmadan
	// "3.92 (eşik 15.00)" okuyan operatör hangi tarafın kötü olduğunu
	// tahmin etmek zorunda kalıyor (v0.9.976'nın ters-çevirme dersi).
	if ev.Metric != "" || ev.Threshold != 0 || ev.Value != 0 {
		val := fmtF(ev.Value)
		if cmp := strings.TrimSpace(ev.Comparator); cmp != "" {
			val += " " + cmp + " eşik " + fmtF(ev.Threshold)
		} else {
			val += " (eşik " + fmtF(ev.Threshold) + ")"
		}
		if ev.Metric != "" {
			val = ev.Metric + " " + val
		}
		add(SignalProblem, "İhlal", val, SevErr)
	}

	// Süre — açık problem için yaş, çözülmüş için açık kalma süresi.
	// 4 saat eşiği problem_priority'nin açık-saat vidasıyla aynı
	// varsayılan (CLAUDE.md triyaj satırı); vidayı burada OKUMUYORUZ
	// (kart ayar okumaz), yalnız aynı büyüklükte bir görsel uyarı.
	if ev.StartedNs > 0 {
		if ev.ResolvedNs > 0 {
			add(SignalProblem, "Süre", "çözüldü · "+FmtDurTR((ev.ResolvedNs-ev.StartedNs)/1e9)+" açık kaldı", SevOK)
		} else if ev.NowNs >= ev.StartedNs {
			age := (ev.NowNs - ev.StartedNs) / 1e9
			sev := ""
			if age >= 4*3600 {
				sev = SevWarn
			}
			add(SignalProblem, "Süre", "açık · "+FmtDurTR(age), sev)
		}
	}

	if d := ev.Deploy; d != nil && strings.TrimSpace(d.Version) != "" {
		v := d.Version + " · " + FmtDurTR(d.AgeSec) + " önce"
		sev := SevWarn
		if d.HasImpact && finite(d.P99DeltaPct) && finite(d.ErrDeltaPP) {
			v += fmt.Sprintf(" · p99 %+.0f%% · hata %+.1fpp", d.P99DeltaPct, d.ErrDeltaPP)
			if d.P99DeltaPct >= 20 || d.ErrDeltaPP >= 1 {
				sev = SevErr
			}
		}
		add(SignalDeploy, "Deploy", v, sev)
	}

	if h := ev.Hyp; h != nil && strings.TrimSpace(h.TopSuspect) != "" {
		v := h.TopSuspect
		if finite(h.Confidence) {
			v += fmt.Sprintf(" · güven %%%.0f", h.Confidence*100)
		}
		sev := ""
		if finite(h.Confidence) && h.Confidence >= 0.5 {
			sev = SevWarn
		}
		add(SignalGeneric, "Kök-neden adayı", v, sev)

		names, cut := capNames(h.Candidates, h.Others)
		if len(names) > 0 {
			v := strings.Join(names, ", ")
			if cut > 0 {
				v += fmt.Sprintf(" +%d", cut)
				truncated = true
			}
			add(SignalGeneric, "Diğer adaylar", v, "")
		}
	}

	if b := ev.Blast; b != nil && b.TotalCallers > 0 {
		v := fmt.Sprintf("%d çağıran", b.TotalCallers)
		sev := ""
		if b.CascadingCallers > 0 {
			v += fmt.Sprintf(" · %d kaskad", b.CascadingCallers)
			sev = SevErr
		}
		if names, cut := capNames(b.TopCallers, len(b.TopCallers)); len(names) > 0 {
			v += " · " + strings.Join(names, ", ")
			if cut > 0 {
				v += fmt.Sprintf(" +%d", cut)
				truncated = true
			}
		}
		add(SignalBlast, "Etki alanı", v, sev)
	}

	if op := ev.SlowOp; op != nil && strings.TrimSpace(op.Name) != "" {
		v := op.Name + fmt.Sprintf(" · p95 %.0fms", op.P95Ms)
		if op.ErrorRate > 0 {
			v += fmt.Sprintf(" · hata %%%.1f", op.ErrorRate)
		}
		add(SignalOpDelta, "En yavaş operasyon", v, "")
	}
	return out, truncated
}

// capNames — ad listesini maxListedNames'e kırpar ve KAÇ tane
// gizlendiğini döndürür. total, kırpmadan ÖNCEKİ gerçek sayı (çağıran
// zaten tavanlı bir liste tutuyorsa total onu aşabilir).
func capNames(names []string, total int) (kept []string, cut int) {
	clean := make([]string, 0, len(names))
	for _, n := range names {
		if strings.TrimSpace(n) != "" {
			clean = append(clean, n)
		}
	}
	if total < len(clean) {
		total = len(clean)
	}
	if len(clean) > maxListedNames {
		clean = clean[:maxListedNames]
	}
	return clean, total - len(clean)
}

// finite — NaN/±Inf kalkanı.
//
// writeJSON'un sanitizeFloats'ı (v0.5.303) JSON SAYILARINI temizler ama
// bizim sayılarımız DİZEYE gömülü: `fmt.Sprintf("%+.0f%%", +Inf)`
// operatöre "p99 +Inf%" yazar ve modele de öyle gider. Sıfır tabanlı
// yüzde deltaları bunu gerçekten üretebiliyor (tasarım §1.6'nın
// sıfır-guard'sız heap yüzdesi şüphesi aynı sınıf). Sonsuz bir değer
// GÖSTERİLMEZ: satırın o parçası düşer, geri kalanı durur.
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// fmtF — kart sayıları: 2 hane, gereksiz sıfırlar atılır (15.00 → "15").
func fmtF(v float64) string {
	if !finite(v) {
		return "—"
	}
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
