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

// ── log-pattern kanıtı (v0.9.1137, Faz 2.4) ─────────────────────────

// PatternServiceRef — desenin bir servisteki payı (logstore'un
// PatternServiceHit projeksiyonu).
type PatternServiceRef struct {
	Service string
	Count   uint64
}

// LogPatternEvidence — küratörlü log deseni kartının deterministik
// girdisi. TAMAMI anomaly.DetectLogPatterns'ın ZATEN döndürdüğü
// alanlardan gelir: kart için ikinci bir ES sorgusu YOK (operatörün
// duran kısıtı "elastice çok sorgu yükü oluşturma sakın").
//
// ŞİDDET KARIŞIMI (severity mix) BİLİNÇLİ YOK: detektörün okuması
// (logstore.CountPatterns) severity boyutu döndürmüyor, dolayısıyla
// karışımı göstermek YENİ bir sorgu şekli isterdi. Olmayan bir kırılımı
// tahminle doldurmak yerine hiç göstermiyoruz.
type LogPatternEvidence struct {
	Pattern string
	// Kind — "new" | "spike". Kartın rengi buradan: FE'nin desen
	// kartındaki rozet dili (streams.tsx: NEW=warning, SPIKE=danger)
	// ile AYNI eşleme, yoksa aynı olay iki yüzeyde iki renk olur.
	Kind          string
	CurrentCount  uint64
	BaselineCount uint64 // pencere-eşdeğerine normalize edilmiş taban
	Ratio         float64
	Service       string // pencerede en çok basan servis
	Sample        string
	TopServices   []PatternServiceRef
	// Tokens — desenin gövde alt dizeleri (küratörlü patterns[] listesi).
	// SİNYAL değil, LİNK malzemesi: /logs sorgusu bunlardan kuruluyor
	// (PatternKQL). Kanıt yapısında durması bilinçli — linki üreten yer
	// kanıtı üreten yerle aynı olsun (v0.9.831'in "iki yerde seçim" tuzağı).
	Tokens     []string
	LastSeenNs int64
	// WindowSec — sayımların penceresi. Sinyal olarak BASILIYOR çünkü
	// "1.240" tek başına birimsiz bir sayı: hangi pencerede 1.240?
	WindowSec int64
	NowNs     int64
}

// LogPatternSignals — desenin deterministik satırları, sıra sabit.
func LogPatternSignals(ev LogPatternEvidence) (out []Signal, truncated bool) {
	add := func(kind, label, value, sev string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		out = append(out, Signal{Kind: kind, Label: label, Value: value, Severity: sev})
	}

	// Durum — triyajın ilk sorusu: bu desen YENİ mi, yoksa bilinen bir
	// desenin patlaması mı? İkisi FARKLI kanıt (detektörün kendi ayrımı:
	// tabansız "new" yalnız sayıya, "spike" orana dayanır).
	switch strings.ToLower(strings.TrimSpace(ev.Kind)) {
	case "new":
		add(SignalLog, "Durum", "YENİ — bu pencerede ilk kez göründü", SevWarn)
	case "spike":
		v := "PATLAMA"
		if finite(ev.Ratio) && ev.Ratio > 0 {
			v += " ×" + fmtF(ev.Ratio)
		}
		add(SignalLog, "Durum", v, SevErr)
	}

	if ev.WindowSec > 0 {
		add(SignalGeneric, "Pencere", "son "+FmtDurTR(ev.WindowSec), "")
	}

	// Hacim — şiddet YOK: rengi Durum satırı taşıyor. İki satırın da
	// kırmızı olması operatöre iki ayrı kötü haber gibi okunur.
	vol := "şimdi " + fmtNum(ev.CurrentCount)
	if ev.BaselineCount > 0 {
		vol += " · taban " + fmtNum(ev.BaselineCount)
	} else if ev.CurrentCount > 0 {
		vol += " · taban yok (yeni)"
	}
	add(SignalLog, "Hacim", vol, "")

	add(SignalGeneric, "Baskın servis", ev.Service, "")

	// Etkilenen servisler — liste-şekilli, tavana takılırsa Truncated.
	entries := make([]string, 0, len(ev.TopServices))
	for _, ts := range ev.TopServices {
		if strings.TrimSpace(ts.Service) == "" {
			continue
		}
		entries = append(entries, ts.Service+" ("+fmtNum(ts.Count)+")")
	}
	if len(entries) > 1 {
		names, cut := capNames(entries, len(entries))
		v := strings.Join(names, ", ")
		if cut > 0 {
			v += fmt.Sprintf(" +%d", cut)
			truncated = true
		}
		add(SignalLog, "Etkilenen servisler", v, "")
	}

	// Son görülme — BAĞIL (mutlak saat yasağı); tazelik eşikleri
	// exception kartıyla AYNI, iki kart aynı sayfada yan yana açılabiliyor.
	if ev.LastSeenNs > 0 && ev.NowNs >= ev.LastSeenNs {
		age := (ev.NowNs - ev.LastSeenNs) / 1e9
		sev := ""
		switch {
		case age <= 15*60:
			sev = SevErr
		case age <= 6*3600:
			sev = SevWarn
		}
		add(SignalGeneric, "Son görülme", FmtDurTR(age)+" önce", sev)
	}

	add(SignalLog, "Örnek satır", oneLine(ev.Sample, 160), "")
	return out, truncated
}

// ── slow-query kanıtı (v0.9.1137, Faz 2.4) ──────────────────────────

// CallerRef — ifade sınıfını çağıran bir servis (db_statement_summary_5m
// MV'sinin servis kırılımı).
type CallerRef struct {
	Service string
	Calls   uint64
	P95Ms   float64
	TotalMs float64
}

// SlowQueryEvidence — yavaş sorgu sınıfı kartının deterministik girdisi.
// Kaynak: db_statement_summary_5m MV'si (özet + çağıran kırılımı) +
// MV-gömülü exemplar (v0.9.1097). HAM spans taraması YOK.
type SlowQueryEvidence struct {
	// StmtParam — `?stmt=` kodeği ("<hash>[|<system>]"); linkler bunu
	// AYNEN taşır ki kart ile çekmece AYNI sınıfı açsın.
	StmtParam string
	Statement string // '?'-normalize görünüm formu
	Sample    string // literalli gerçek örnek (yalnız prompt'a girer)
	DBSystem  string
	DBName    string
	Calls     uint64
	Errors    uint64
	TotalMs   float64
	AvgMs     float64
	P95Ms     float64
	P99Ms     float64
	MaxMs     float64
	Callers   []CallerRef
	// CallersCapped — çağıran okuması TAVANA dayandı (okuma limiti).
	// Ayrı bir bayrak çünkü kırpma İKİ yerde oluyor: okuma limitinde ve
	// gösterim tavanında; ikisini tek sayıya karıştırmak "+N"i yalan yapar.
	CallersCapped bool
	SlowTraceID   string
	ErrorTraceID  string
	FromNs, ToNs  int64
	NowNs         int64
}

// SlowQuerySignals — yavaş sorgunun deterministik satırları, sıra sabit.
func SlowQuerySignals(ev SlowQueryEvidence) (out []Signal, truncated bool) {
	add := func(kind, label, value, sev string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		out = append(out, Signal{Kind: kind, Label: label, Value: value, Severity: sev})
	}

	// Gecikme — şiddet p99'dan, katalog tablosunun p99Color eşikleriyle
	// AYNI (>1000ms kırmızı, >200ms sarı). Aynı satır iki yüzeyde farklı
	// renk olmasın.
	if ev.P95Ms > 0 || ev.P99Ms > 0 || ev.MaxMs > 0 {
		v := "p95 " + fmtMs(ev.P95Ms) + " · p99 " + fmtMs(ev.P99Ms)
		if ev.MaxMs > 0 {
			v += " · maks " + fmtMs(ev.MaxMs)
		}
		sev := ""
		switch {
		case ev.P99Ms > 1000:
			sev = SevErr
		case ev.P99Ms > 200:
			sev = SevWarn
		}
		add(SignalDB, "Gecikme", v, sev)
	}

	// Hacim — katalogun sıralama ölçütü toplam duvar saati; kart da onu
	// öne alıyor ("düzeltmeye değen" sorgu bu).
	if ev.Calls > 0 {
		v := fmtNum(ev.Calls) + " çağrı · toplam " + fmtMs(ev.TotalMs)
		if ev.AvgMs > 0 {
			v += " · ort " + fmtMs(ev.AvgMs)
		}
		add(SignalDB, "Hacim", v, "")
	}

	if ev.Errors > 0 {
		v := fmtNum(ev.Errors) + " hata"
		if ev.Calls > 0 {
			v += fmt.Sprintf(" (%%%.1f)", float64(ev.Errors)*100/float64(ev.Calls))
		}
		add(SignalDB, "Hata", v, SevErr)
	}

	// Motor — db_name gruplamada KATLANIYOR (katalog satırının "+N"i tam
	// bunu söylüyor), yani buradaki ad sınıfın TEK veritabanı olmak
	// zorunda değil. 'default' MV'nin "db.name yoktu" nöbetçisi: bir
	// veritabanı adı gibi basmak yanlış olur.
	if sys := strings.TrimSpace(ev.DBSystem); sys != "" {
		v := sys
		if n := strings.TrimSpace(ev.DBName); n != "" && n != "default" {
			v += " · " + n
		}
		add(SignalDB, "Motor", v, "")
	}

	if len(ev.Callers) > 0 {
		c := ev.Callers[0]
		v := c.Service
		if c.Calls > 0 {
			v += " · " + fmtNum(c.Calls) + " çağrı"
		}
		if c.P95Ms > 0 {
			v += " · p95 " + fmtMs(c.P95Ms)
		}
		add(SignalDB, "En çok çağıran", v, "")
	}
	if len(ev.Callers) > 1 {
		names := make([]string, 0, len(ev.Callers))
		for _, c := range ev.Callers {
			if strings.TrimSpace(c.Service) != "" {
				names = append(names, c.Service)
			}
		}
		kept, cut := capNames(names, len(names))
		v := fmt.Sprintf("%d servis: %s", len(names), strings.Join(kept, ", "))
		if cut > 0 {
			v += fmt.Sprintf(" +%d", cut)
			truncated = true
		}
		add(SignalDB, "Çağıranlar", v, "")
	}
	if ev.CallersCapped {
		truncated = true
	}

	add(SignalDB, "İfade", oneLine(ev.Statement, 200), "")
	return out, truncated
}

// ── biçim yardımcıları ──────────────────────────────────────────────

// fmtMs — milisaniye → okunur süre. Her BİRİM ayrı dalda ve hepsi
// tablo-testli (v0.6.36 birim-karıştırma kuralı: değer+birim üreten
// şablon HER birimi testler). 1000ms altı ms, 60sn altı sn, üstü dk.
func fmtMs(ms float64) string {
	if !finite(ms) || ms < 0 {
		return "—"
	}
	switch {
	case ms < 1000:
		return fmt.Sprintf("%.0fms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fsn", ms/1000)
	default:
		return fmt.Sprintf("%.1fdk", ms/60_000)
	}
}

// oneLine — çok satırlı örneği tek satıra indirip RUNE sınırında keser.
// Bayt kesmesi geçersiz UTF-8 üretir ve o dize hem panele hem PROMPT'a
// gider (anomaly.truncateSample bugün bayt kesiyor — kart onu tekrar
// etmiyor).
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 {
		return s
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
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
