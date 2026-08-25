package insight

import (
	"net/url"
	"strconv"
	"strings"
)

// links.go — kartın derin linkleri. SUNUCUDA kuruluyor (gerekçe:
// contract.go paket yorumu) ve hepsi SAF.
//
// TEK KURAL, tavizsiz: pencere taşınır. Bir kart linki "şu an baktığım
// olayın span'lerini/loglarını göster" sorusunun cevabıdır; hedef sayfa
// pencereyi URL'den çözer ve yoksa operatörün yapışkan aralığına düşer.
// Yani pencereyi düşüren link, SESSİZCE başka bir soruyu cevaplar: hedef
// boş liste çizer ve bu "böyle bir kayıt yok" gibi okunur. Bu hata
// frontend'de DÖRT kez ayrı ayrı gemiye gitti (Explore pivotu v0.9.208,
// anomali çekmecesi / slow-queries / backtrace v0.9.213) ve o yüzden
// tracesPivotHref'te pencere ZORUNLU argüman. Burada aynı disiplin:
// pencere gerektiren her üretici RangeParam'dan geçer, boş token
// dönerse link HİÇ üretilmez (kırık link, link yokluğundan kötü —
// v0.9.655 ilkesi).
//
// Birim disiplini (v0.6.36 sınıfı): olay damgaları unix NANOSANİYE,
// `custom:` token'ı MİLİSANİYE, chart penceresi SANİYE. Üç birim tek
// dosyada dönüşüyor; her dönüşümün kendi tablo testi var.

// severityErrorFloor — /logs'un OTel severity-number tabanı (ERROR).
// Param adı `severity`: /logs okuyucusu (logsUrl.ts readLogsParams)
// TAM olarak bunu okuyor. `minSev` yazan bir link ÖLÜ param taşır ve
// hedef sayfa "tüm seviyeler"de açılır — copilot_followup.go'nun guided
// çipleri bugün bu hatayı taşıyor (ayrı dilimde düzeltilecek).
const severityErrorFloor = "17"

// RangeParam — [fromNs, toNs] → /logs, /traces, /service ve /problems'ın
// okuduğu `custom:<fromMs>-<toMs>` token'ı. logsRangeParam
// (frontend/src/lib/logsUrl.ts) ile AYNI sözleşme:
//
//   - ns → ms; alt kenar AŞAĞI, üst kenar YUKARI yuvarlanır ki yuvarlama
//     pencereyi DARALTMASIN (kesilen üst kenar en yeni kovayı düşürür);
//   - decodeRange'in kabul testini birebir aynala: fromMs > 0 ve
//     toMs > fromMs. Reddedeceği bir token'ı ÜRETMEYİZ, boş döner ve
//     çağıran linki atar.
func RangeParam(fromNs, toNs int64) string {
	if fromNs <= 0 || toNs <= 0 {
		return ""
	}
	fromMs := fromNs / 1e6
	toMs := toNs / 1e6
	if toNs%1e6 != 0 {
		toMs++
	}
	if fromMs <= 0 || toMs <= fromMs {
		return ""
	}
	return "custom:" + strconv.FormatInt(fromMs, 10) + "-" + strconv.FormatInt(toMs, 10)
}

// ExceptionWindow — exception grubunun OLAY penceresi.
//
// Grubun tamamı (FirstSeen→LastSeen) haftalar sürebilir; log/trace
// pivotunu o aralığa açmak hem pahalı hem yanlış soru — operatörün
// baktığı şey SON oluşum. Kural: [LastSeen-30dk, LastSeen+5dk]; grup bu
// pencereden GENÇSE alt kenar FirstSeen-5dk olur (yeni grubun run-up'ı
// pencerede kalsın). Saf; sıfır/ters damgalarda boş (0,0) döner ve
// pencere isteyen linkler üretilmez.
func ExceptionWindow(firstSeenNs, lastSeenNs int64) (fromNs, toNs int64) {
	if lastSeenNs <= 0 {
		return 0, 0
	}
	const min = int64(60 * 1e9)
	to := lastSeenNs + 5*min
	from := lastSeenNs - 30*min
	if firstSeenNs > 0 && firstSeenNs > from {
		from = firstSeenNs - 5*min
	}
	if from <= 0 || to <= from {
		return 0, 0
	}
	return from, to
}

// MetricFamily — problemin ihlal ettiği metrik ailesi. Alt dize listesi
// exemplarKindForMetric (internal/api/rootcause.go:44) ile BİREBİR: aynı
// problemde örnek trace "yavaş" seçilirken kart linki "hatalı" göstermesin.
// Dönüş: "error" | "latency" | "other".
func MetricFamily(metric string) string {
	m := strings.ToLower(metric)
	switch {
	case strings.Contains(m, "error"):
		return "error"
	case strings.Contains(m, "p99") || strings.Contains(m, "p95") ||
		strings.Contains(m, "latency") || strings.Contains(m, "duration") ||
		strings.Contains(m, "ms"):
		return "latency"
	default:
		return "other"
	}
}

// ── üreticiler ──────────────────────────────────────────────────────

// ExceptionLinks — exception kartının çipleri, sıra sabit.
func ExceptionLinks(ev ExceptionEvidence) []Link {
	from, to := ExceptionWindow(ev.FirstSeenNs, ev.LastSeenNs)
	rng := RangeParam(from, to)
	var out []Link

	if fp := strings.TrimSpace(ev.Fingerprint); fp != "" {
		// /problems?exc=<fp> — grubun TAM detay görünümü
		// (withExcParam, features/anomalies/problemLink.ts). Pencere de
		// biner: aynı sayfadaki liste operatörün olayıyla aynı aralığı
		// göstersin.
		out = append(out, Link{Label: "Exception grubu",
			Href: href("/problems", kv{"exc", fp}, kv{"range", rng})})
	}
	// Trace bir NOKTA nesnesi — /trace?id= pencere okumaz, o yüzden
	// buraya range koymamak eksiklik değil (pivotHref disiplini pencere
	// ÇÖZEN hedefler içindir).
	if tid := strings.TrimSpace(ev.TraceID); tid != "" {
		out = append(out, Link{Label: "Örnek trace",
			Href: href("/trace", kv{"id", tid})})
	}
	if svc := strings.TrimSpace(ev.Service); svc != "" && rng != "" {
		// rootOnly=false ŞART: exception span'i neredeyse her zaman
		// trace'in ORTASINDA (v0.8.585) — /traces varsayılanı true ve
		// o varsayılanla liste boş çıkar.
		out = append(out, Link{Label: "Hatalı trace'ler",
			Href: href("/traces", kv{"service", svc}, kv{"hasError", "true"},
				kv{"rootOnly", "false"}, kv{"range", rng})})
		out = append(out, Link{Label: "Loglar (error+)",
			Href: href("/logs", kv{"service", svc},
				kv{"severity", severityErrorFloor}, kv{"range", rng})})
		out = append(out, Link{Label: "Servis",
			Href: href("/service", kv{"name", svc}, kv{"range", rng})})
	}
	return out
}

// ProblemLinks — problem kartının çipleri, sıra sabit.
func ProblemLinks(ev ProblemEvidence) []Link {
	rng := RangeParam(ev.FromNs, ev.ToNs)
	var out []Link

	if id := strings.TrimSpace(ev.ID); id != "" {
		// /problems?problem=<id> — bildirim e-postasının, status
		// sayfasının ve triyaj çekmecesinin ORTAK şekli
		// (notify.go:308, api.go statusPageProblemURL).
		out = append(out, Link{Label: "Problem detayı",
			Href: href("/problems", kv{"problem", id}, kv{"range", rng})})
	}
	svc := strings.TrimSpace(ev.Service)
	if svc != "" && rng != "" {
		out = append(out, Link{Label: "Servis",
			Href: href("/service", kv{"name", svc}, kv{"range", rng})})

		switch MetricFamily(ev.Metric) {
		case "error":
			out = append(out, Link{Label: "Hatalı trace'ler",
				Href: href("/traces", kv{"service", svc}, kv{"hasError", "true"},
					kv{"rootOnly", "false"}, kv{"range", rng})})
		case "latency":
			out = append(out, Link{Label: "En yavaş trace'ler",
				Href: href("/traces", kv{"service", svc}, kv{"sort", "duration"},
					kv{"rootOnly", "false"}, kv{"range", rng})})
		default:
			out = append(out, Link{Label: "Trace'ler",
				Href: href("/traces", kv{"service", svc},
					kv{"rootOnly", "false"}, kv{"range", rng})})
		}
		out = append(out, Link{Label: "Loglar (error+)",
			Href: href("/logs", kv{"service", svc},
				kv{"severity", severityErrorFloor}, kv{"range", rng})})
	}
	// Kök-neden adayı KENDİ servis sayfasına — hipotezin işaret ettiği
	// yere tek tıkla gitmek, kartın "sonra ne yapayım" yarısı.
	if h := ev.Hyp; h != nil && rng != "" {
		if sus := strings.TrimSpace(h.TopSuspect); sus != "" && sus != svc {
			out = append(out, Link{Label: "Aday: " + sus,
				Href: href("/service", kv{"name", sus}, kv{"range", rng})})
		}
	}
	return out
}

// ── log-pattern linkleri (v0.9.1137, Faz 2.4) ───────────────────────

// PatternLogWindow — desenin OLAY penceresi: son görülmenin 30dk öncesi
// → 10dk sonrası. patternLogWindow (streams.tsx, v0.9.862) ile AYNI
// formül: aynı desen için satırın "logs ↗" çipi ile kartın pivotu farklı
// pencere açarsa operatör iki farklı cevap görür.
//
// Saf; damga yoksa (0,0) döner ve pencere isteyen linkler üretilmez.
func PatternLogWindow(lastSeenNs int64) (fromNs, toNs int64) {
	if lastSeenNs <= 0 {
		return 0, 0
	}
	const min = int64(60 * 1e9)
	from, to := lastSeenNs-30*min, lastSeenNs+10*min
	if from <= 0 || to <= from {
		return 0, 0
	}
	return from, to
}

// PatternKQL — desenin /logs sorgusu.
//
// PARAM DOĞRULAMASI (ölü-param disiplini, v0.9.1130 sınıfı): /logs
// okuyucusu readLogsParams (frontend/src/lib/logsUrl.ts) YALNIZ
// service · cluster · q · severity · traceId · spanId · hasTrace ·
// filters · cols okur, pencere de `range`ten. `pattern=` ya da
// `tokens=` diye bir param YOK — yazsak sessizce düşer ve hedef sayfa
// FİLTRESİZ açılır.
//
// Servis de `q`ya giriyor, `service=`ye DEĞİL: v0.5.311'de
// operatör-bildirimli karar ("servis picker'da ön-seçili gelmesin,
// KQL'e girsin") — kardeş üretici logsLinkForPattern bugün aynen bunu
// yapıyor. İki üretici aynı çipi çiziyor; ayrışmaları operatörün
// "aynı link neden farklı" sorusudur.
func PatternKQL(service string, tokens []string) string {
	var clauses []string
	if s := strings.TrimSpace(service); s != "" {
		clauses = append(clauses, `service.name:"`+strings.ReplaceAll(s, `"`, `\"`)+`"`)
	}
	quoted := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if strings.TrimSpace(t) == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `\"`)+`"`)
	}
	switch len(quoted) {
	case 0:
	case 1:
		clauses = append(clauses, quoted[0])
	default:
		clauses = append(clauses, "("+strings.Join(quoted, " OR ")+")")
	}
	return strings.Join(clauses, " AND ")
}

// LogPatternLinks — desen kartının çipleri, sıra sabit.
func LogPatternLinks(ev LogPatternEvidence) []Link {
	from, to := PatternLogWindow(ev.LastSeenNs)
	rng := RangeParam(from, to)
	var out []Link

	// Loglar — desenin GERÇEK satırlarına inen tek link. Pencere ZORUNLU:
	// penceresiz açılan /logs yapışkan aralığa düşer ve boş liste
	// "böyle log yok" diye okunur (v0.9.862'nin düzelttiği tam bu).
	if q := PatternKQL(ev.Service, ev.Tokens); q != "" && rng != "" {
		out = append(out, Link{Label: "Loglar (desen)",
			Href: href("/logs", kv{"q", q}, kv{"range", rng})})
	}
	if svc := strings.TrimSpace(ev.Service); svc != "" {
		if rng != "" {
			out = append(out, Link{Label: "Servis",
				Href: href("/service", kv{"name", svc}, kv{"range", rng})})
			out = append(out, Link{Label: "Hatalı trace'ler",
				Href: href("/traces", kv{"service", svc}, kv{"hasError", "true"},
					kv{"rootOnly", "false"}, kv{"range", rng})})
		}
	}
	return out
}

// ── slow-query linkleri (v0.9.1137, Faz 2.4) ────────────────────────

// SlowQueryLinks — yavaş sorgu kartının çipleri, sıra sabit.
//
// PARAM DOĞRULAMASI:
//   - /databases/statement → `stmt` (decodeStmtParam) + `range`
//     (usePageZoomRange → useUrlRange). İfade DETAY SAYFASI; aynı şekli
//     stmtDetailHref (stmtParam.ts) da üretiyor.
//
//     ⚠ v0.9.1377 — bu satır `/slow-queries` yazıyordu ve o yol App.tsx'te
//     KAYITLI DEĞİL. Kayıtsız yol catch-all'a düşüyor
//     (`path="*"` → <Navigate to="/" replace />), yani kartın "İfade
//     detayı" çipi operatörü ANA SAYFAYA atıyordu — 404 bile değil,
//     sessiz bir yön değişimi. Bu, v0.9.1323'ün BİREBİR aynısı: o sürüm
//     frontend'deki stmtDetailHref'i düzeltti, buradaki ikizini
//     bırakmıştı. Üstelik yukarıdaki şerh "aynı şekli stmtDetailHref de
//     üretiyor" diyerek iki tarafın hizalı olduğunu İDDİA ediyordu; 1323
//     sonrası bu iddia yanlıştı ve hiçbir şey onu ölçmüyordu.
//     Artık ölçülüyor: TestServerLinkPathsAreRegisteredRoutes.
//
//     Hedef ayrıca v0.9.1374'te taşındı (çekmece → tam sayfa), yani tek
//     düzeltme iki kusuru birden kapatıyor.
//   - /databases → `dbsys` + `dbname` (Databases.tsx:80-81) + `range`.
//   - /trace → `id`; pencere OKUMAZ (nokta nesnesi, links.go üst notu).
//
// /database (TEKİL, detay sayfası) BİLİNÇLİ YOK: oradaki kimlik bir
// ÜÇLÜ (system, instance, dbName) ve bir ifade sınıfının instance'ı
// yoktur — sınıf motorun bütün instance'larına yayılır. Uydurma bir
// instance sessizce BOŞ bir detay sayfası açar (v0.9.821'in
// "etiketi kimlik sanmak" arızası). databasesFilterHref (v0.9.964) bu
// kararı frontend'de zaten vermiş; sunucu tarafı aynı kararı tekrarlıyor.
//
// dbname da yazılmıyor: gruplama db_name'i KATLIYOR, yani özetten gelen
// ad temsili olabilir (katalog satırının "+N"i) ve 'default' MV'nin
// "db.name yoktu" nöbetçisi. Katalogda motor filtresi kalıyor:
// olabileceğinden dar bir katalog, kendinden emin ama yanlış olandan iyidir.
func SlowQueryLinks(ev SlowQueryEvidence) []Link {
	rng := RangeParam(ev.FromNs, ev.ToNs)
	var out []Link

	if p := strings.TrimSpace(ev.StmtParam); p != "" && rng != "" {
		out = append(out, Link{Label: "İfade detayı",
			Href: href("/databases/statement", kv{"stmt", p}, kv{"range", rng})})
	}
	if tid := strings.TrimSpace(ev.SlowTraceID); tid != "" {
		out = append(out, Link{Label: "En yavaş örnek trace",
			Href: href("/trace", kv{"id", tid})})
	}
	if tid := strings.TrimSpace(ev.ErrorTraceID); tid != "" {
		out = append(out, Link{Label: "Hatalı örnek trace",
			Href: href("/trace", kv{"id", tid})})
	}
	if len(ev.Callers) > 0 && rng != "" {
		if svc := strings.TrimSpace(ev.Callers[0].Service); svc != "" {
			out = append(out, Link{Label: "Çağıran: " + svc,
				Href: href("/service", kv{"name", svc}, kv{"range", rng})})
		}
	}
	if sys := strings.TrimSpace(ev.DBSystem); sys != "" {
		out = append(out, Link{Label: "Veritabanı kataloğu",
			Href: href("/databases", kv{"dbsys", sys}, kv{"range", rng})})
	}
	return out
}

// ── küçük yardımcılar ───────────────────────────────────────────────

type kv struct{ k, v string }

// href — boş DEĞERLİ paramları ATAR (yazmaz), kalanları url.Values ile
// kodlar. Values.Encode() anahtarları alfabetik basar: linkler
// deterministik, dolayısıyla test edilebilir.
func href(path string, params ...kv) string {
	q := url.Values{}
	for _, p := range params {
		if strings.TrimSpace(p.v) == "" {
			continue
		}
		q.Set(p.k, p.v)
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
