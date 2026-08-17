// Package insight — gömülü "insight kartı"nın SUNUCU sözleşmesi
// (AI Assistant Faz 2.1, v0.9.1129;
// docs/plans/ai-assistant-design-2026-08-16.md §2.3).
//
// Kartın tek fikri: kanıt DÖRT dik yoldan gelir ve hiçbiri diğerini
// bekletmez.
//
//	SIGNALS  deterministik projeksiyon — LLM'siz de doğru, ANINDA
//	LINKS    sunucu-üretimi derin linkler — pencereyi TAŞIRLAR
//	CHARTS   doğrulanmış chart spec'leri (opsiyonel)
//	PROSE    modelin anlatısı — akar, gecikir, HİÇ gelmeyebilir
//
// Ayrım TİPTE yaşıyor çünkü ürün kararı şu: AI kapalı, yapılandırılmamış
// ya da kotası dolmuş bir kurulumda kart YİNE tam değerli olmalı. Emsal
// ServiceChartsExplainBody (v0.9.477): anlatım ile yapısal kanıt ayrı
// yollardan gelir, model metni bozsa bile tablo doğru kalır. Burada aynı
// ilke tipe kaldırıldı — bir yüzey Signals'ı prose'un İÇİNDEN türetmeye
// kalkarsa derleyici izin vermiyor.
//
// SUNUCU-ÜRETİMİ LİNKLER — bilinçli sapma. ChartProblemSignal ailesi
// (explain_service_charts.go:181) linkleri FRONTEND'e bıraktı: "rotalar
// frontend'in bilgisi". Kart için ters karar verildi, iki nedenle:
// (1) kart DÖRT yuvada aynı bileşenle çizilecek (exception satırı,
// problem satırı, log paneli, slow-span), yani rota bilgisi FE'de dörde
// kopyalanırdı; (2) pencere-taşıma disiplini (pivotHref, serviceHref,
// logsRangeParam) tam olarak "kopyalandığı için dört kez kırıldı" diye
// var. Kanıtı üreten yer linki de üretir; FE yalnız Label+Href çizer.
package insight

import (
	"fmt"
	"strings"
)

// ── kart türleri ────────────────────────────────────────────────────

// Faz 2.1'in yuvaları: exception grubu (id = fingerprint) ve problem
// (id = problem id). Log deseni + yavaş sorgu 2.4'te aynı sözleşmeye
// bindi — o yüzden tür bir ENUM, handler başına ayrı uç değil.
//
// KİMLİK KARARLARI (v0.9.1137, Faz 2.4) — her ikisi de "satırın KENDİ
// taşıdığı kimlik", türetilmiş bir şey değil:
//
//	KindLogPattern — id = küratörlü desenin ADI ("Out of memory").
//	  LogPatternAnomaly'nin id alanı YOK; adı ise detektörün
//	  patterns[] listesinin anahtarı (internal/anomaly/log_patterns.go)
//	  ve susturma parmak izinin orta alanı (FingerprintAnomaly
//	  "log_pattern|<pattern>|<service>"). Yani kimlik olarak ZATEN
//	  kullanılıyor. Drain şablonlarının (log_templates.ID) hash'i AYRI
//	  bir aile: "Desenleri anlat" paneli ikisini birden anlatıyor ama
//	  operatörün TIKLADIĞI satır küratörlü desen — şablon hash'ini
//	  kimlik yapmak, olmayan bir satırı adreslemek olurdu.
//	  Servis kimliğe GİRMEZ: kartın servisi "bu pencerede en çok basan"
//	  servistir, pencereyle DEĞİŞİR — kimliğe koymak aynı deseni iki
//	  pencerede iki farklı kart yapardı.
//
//	KindSlowQuery — id = `?stmt=` kodeğinin ta kendisi:
//	  "<stmtHash>[|<dbSystem>]". stmtHash v0.8.375'in ingest-zamanı
//	  parmak izi (pencereler arası KALICI), dbSystem ise katalog
//	  sayfasının kapsamı; ikisi birlikte /slow-queries çekmecesinin
//	  URL kimliği (stmtParam.ts encodeStmtParam) ve detay ucunun
//	  (hash, system) argüman çifti. Üçüncü bir yazılış üretmedik.
const (
	KindException  = "exception"
	KindProblem    = "problem"
	KindLogPattern = "log-pattern"
	KindSlowQuery  = "slow-query"
)

// Kinds — route'un tanıdığı tür listesi. Sıra sabit: /ai yüzey
// kırılımı ve testler bu listeden türer.
func Kinds() []string {
	return []string{KindException, KindProblem, KindLogPattern, KindSlowQuery}
}

// KnownKind — bilinmeyen tür 404 olur ve LLM'e HİÇ ulaşmaz. Bu, ai_calls
// yüzey etiketinin sonlu kalmasının da kapısı (aiSurfaceFromPath aynı
// whitelist'i ikinci kez uygular — ucuz ikinci kapı).
func KnownKind(k string) bool {
	for _, v := range Kinds() {
		if v == k {
			return true
		}
	}
	return false
}

// ── sinyal türleri ve şiddet ────────────────────────────────────────

// Sinyal türü, FE'nin ikon/renk seçimi için. Serbest metin DEĞİL:
// kart dört yuvada aynı görsel dili konuşsun diye kapalı küme.
const (
	SignalDeploy    = "deploy"
	SignalProblem   = "problem"
	SignalOpDelta   = "opdelta"
	SignalBlast     = "blast"
	SignalException = "exception"
	SignalGeneric   = "generic"
	// v0.9.1137 (Faz 2.4) — iki yeni yuvanın kendi aileleri. Kapalı küme
	// GENİŞLEDİ, esnetilmedi: FE'nin InsightSignalKind birleşimi bu
	// listeyle 1:1 (types.ts) ve bugün hiçbiri BOYANMIYOR — küme
	// gelecekteki ikon dili için var (InsightCard.tsx gerekçesi).
	SignalLog = "log"
	SignalDB  = "db"
)

// Şiddet — boş DEĞER GEÇERLİ ve anlamlı: "nötr bilgi" (servis adı,
// operasyon sayısı). ok/warn/err olmayan bir üçüncü renk yok.
const (
	SevOK   = "ok"
	SevWarn = "warn"
	SevErr  = "err"
)

// Signal — kartın deterministik satırı. Value bir DİZE çünkü kart onu
// olduğu gibi çizer; birim/biçim kararı burada, sunucuda verilir.
//
// MUTLAK SAAT YOK. Value içine "16.08 09:11" basmak yasak: sunucu UTC
// basar, sayfanın kalanı tarayıcı saatini basar (fmtClock/fmtDateTime,
// 24 saat konvansiyonu v0.9.879) ve iki damga yan yana ÇELİŞİR. Zaman
// bilgisi ya bağıl gösterilir ("38dk önce") ya da Link'in penceresine
// biner (custom:<ms>-<ms> — birim belirsizliği olmayan tek yol).
type Signal struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Severity string `json:"severity,omitempty"`
}

// Link — kartın derin-link çipi. Şekil guidedAnswerLink'in ikizi
// (copilot_followup.go:185): FE'nin çip şeridi bu şekli çiziyor.
type Link struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// ChartSpec — render_chart'ın DOĞRULANMIŞ çıktısıyla aynı şekil
// (chatChartBlock, copilot_chat.go:337 → guidedChartSpec). Aynı şekil
// olması şart: FE tarafında ```chart``` fence'ini çizen bileşen
// (CosreChart) bu alan kümesini okuyor, kart ikinci bir çizici
// istemiyor.
type ChartSpec struct {
	Title     string `json:"title"`
	Service   string `json:"service"`
	Operation string `json:"operation,omitempty"`
	Agg       string `json:"agg"`
	RangeS    int64  `json:"rangeS"`
}

// chartAggs — agg whitelist. CosreChart'ın AGG_META anahtarlarıyla
// 1:1 olmak ZORUNDA; bilinmeyen agg frontend'de sessizce 'rate'e düşer.
// mcptools.renderChartMetrics ikinci yazılış — chart_pin_test.go iki
// yazılışın ayrışmasını kırmızı yakar.
var chartAggs = map[string]bool{
	"rate": true, "error_rate": true, "p50": true, "p95": true, "p99": true,
}

// Chart penceresi bandı — normalizeRenderChart ile AYNI (varsayılan
// 30dk, tavan 7g). Kart spec'i modelden gelmiyor ama bandın tek olması
// FE'nin "hangi pencere gelebilir" kabulünü tek tutuyor.
const (
	ChartRangeDefaultS int64 = 1800
	ChartRangeMaxS     int64 = 7 * 86400
)

// NewChartSpec — spec kurucusu + doğrulayıcı. ok=false ⇒ spec ATILIR
// (kart chart'sız çizilir); yarım/uydurma bir spec basıp FE'de boş bir
// panel çizdirmek, hiç chart olmamasından kötü.
//
// Saf — table-testli.
func NewChartSpec(title, service, operation, agg string, rangeS int64) (ChartSpec, bool) {
	service = strings.TrimSpace(service)
	agg = strings.TrimSpace(agg)
	if service == "" || !chartAggs[agg] {
		return ChartSpec{}, false
	}
	if rangeS <= 0 {
		rangeS = ChartRangeDefaultS
	}
	if rangeS > ChartRangeMaxS {
		rangeS = ChartRangeMaxS
	}
	if strings.TrimSpace(title) == "" {
		base := service
		if operation != "" {
			base = operation
		}
		title = base + " · " + agg
	}
	return ChartSpec{
		Title: title, Service: service,
		Operation: strings.TrimSpace(operation), Agg: agg, RangeS: rangeS,
	}, true
}

// ── cevap ───────────────────────────────────────────────────────────

// Response — kartın TAM cevabı. Akan kipte `signals` çerçevesi bunun
// prose'suz hâlidir, `answer` çerçevesi prose'u getirir; buffered kipte
// tek gövde olarak döner. TEK tip olması bilinçli: FE iki çerçeveyi
// aynı okuyucuyla çözer.
type Response struct {
	// Prose — modelin anlatısı. Boş olabilir ve bu bir HATA DEĞİL
	// (AIOff, kota, model hatası). Kart boş prose'u "anlatım yok"
	// diye söyler, sinyalleri yine çizer.
	Prose string `json:"prose"`

	// Signals / Links — DAİMA dizi, asla null. nil bırakmak JSON'da
	// `null` üretir ve FE'de `.map` çöker; v0.9.836'nın (rootcause
	// correlations) tam olarak bu sınıfı. Normalize() garanti eder.
	Signals []Signal    `json:"signals"`
	Links   []Link      `json:"links"`
	Charts  []ChartSpec `json:"charts,omitempty"`

	// ExchangeID — bu CEVABIN kimliği; 👍/👎 bununla
	// POST /api/ai/feedback'e gider (v0.8.399 rayı). AI kapalıyken
	// BOŞ: oylanacak bir model cevabı yok, ai_calls satırı da yok.
	ExchangeID string `json:"exchangeId,omitempty"`

	// AIOff — "AI yapılandırılmamış/kapalı" işareti. Kartın prose
	// alanını neden boş gördüğünü FE'nin ARIZA sanmaması için AÇIK
	// bir bayrak: 503 yerine 200 + bayrak, çünkü cevabın geri kalanı
	// (sinyaller, linkler) tamamen geçerli.
	AIOff bool `json:"aiOff,omitempty"`

	// Truncated — deterministik yarının KIRPILDIĞI işareti (çipler
	// tavana takıldı). Dürüstlük zarfının kart-içi hâli: operatör
	// "hepsi bu mu" sorusunu sormak zorunda kalmaz.
	Truncated bool `json:"truncated,omitempty"`

	// Model — anlatıyı üreten model kimliği (copilot.ActiveModel).
	// Yalnız Active() iken dolu; baseURL/apiKey ASLA (v0.9.1037 çipi
	// ile aynı dar sözleşme).
	Model string `json:"model,omitempty"`
}

// Normalize — nil dilimleri boş dilime çevirir. Çerçeve/gövde yazan
// TEK yer bunu çağırır, çağrı noktalarına dağıtılmaz.
func (r *Response) Normalize() {
	if r.Signals == nil {
		r.Signals = []Signal{}
	}
	if r.Links == nil {
		r.Links = []Link{}
	}
}

// WithoutProse — `signals` çerçevesinin gövdesi: aynı tip, prose boş.
// Değer kopyası döner (çağıran Response'u sonra prose ile doldurmaya
// devam eder).
func (r Response) WithoutProse() Response {
	r.Prose = ""
	r.Normalize()
	return r
}

// ── biçimleyiciler (saf) ────────────────────────────────────────────

// FmtDurTR — bağıl süre. fmtAgoTR (copilot_guided.go:830) ile BAYT
// BAYT aynı yazılış: aynı operatör aynı gün sohbet cevabında "38dk",
// kartta "38 min" görmemeli. İki yazılışın ayrışmasını
// signals_test.go pinler.
//
// Her birim ayrı dalda (sn/dk/sa/gün) — v0.6.36 birim-karıştırma
// sınıfının kuralı: değer+birim üreten şablon HER birimi testler.
func FmtDurTR(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%dsn", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%ddk", seconds/60)
	case seconds < 86400:
		h, m := seconds/3600, (seconds%3600)/60
		if m == 0 {
			return fmt.Sprintf("%dsa", h)
		}
		return fmt.Sprintf("%dsa %ddk", h, m)
	default:
		d, h := seconds/86400, (seconds%86400)/3600
		if h == 0 {
			return fmt.Sprintf("%dgün", d)
		}
		return fmt.Sprintf("%dgün %dsa", d, h)
	}
}

// fmtNum — tam sayıyı binlik ayraçlı yazar (1240 → "1.240"). Kart
// sayıları operatörün okuduğu yerde; ham 7 haneli bir sayı satırı
// okunmaz kılıyor.
func fmtNum(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
