package api

// glossary.go — v0.10.434 (CoSRE router boşlukları D7c): deterministik
// terim sözlüğü. Prod /ai "Router boşlukları": "requestid nedir" gibi
// sorular hiçbir kılavuz sinyali taşımadığından RAG doküman katmanına
// düşüp "yüklü dokümanlarda bu bilgi yok" alıyordu. Şimdi Go haritasından
// cevap: LLM ÇAĞRISI YOK (uydurma riski sıfır, maliyet sıfır), exchangeId
// YOK (deterministik sunucu metni oylanamaz — copilot_intent.go #3
// sözleşmesi), "Kaynak:" satırı yok — ürün sözlüğü kanıt değil tanımdır.
//
// Kapı: kılavuz sinyal kapısından ÖNCE (copilotChatGuided). "p95 ne demek"
// sağlık sinyali taşır ve aksi hâlde ask_service'e savrulurdu; "span
// nedir" trace kökü taşır ve none'a düşerdi. Bilinmeyen terim ok=false:
// alt katmanlar (RAG: yüklü dokümanlar) devam eder.

import (
	"regexp"
	"strings"
	"unicode"
)

type glossaryEntry struct {
	Text  string
	Links []guidedAnswerLink
}

// glossaryTerms — anahtarlar NORMALİZE (küçük harf, yalnız harf+rakam):
// "request id" / "request-id" / "requestid" aynı anahtara iner.
var glossaryTerms = map[string]glossaryEntry{
	"requestid": {"Request ID (istek kimliği): bir isteği uçtan uca izlemek için istemcinin ya da ağ geçidinin ürettiği kurumsal kimlik. Coremetry'de loglarda aranır ve bulunursa trace'e köprülenir — bir kimliği sohbete yapıştırırsan CoSRE trace'ini ve loglarını getirir.",
		[]guidedAnswerLink{{Label: "Loglar", Href: "/logs"}}},
	"trace": {"Trace: tek bir isteğin sistemde izlediği tüm yolun kaydı; birbirine bağlı span'lerden oluşan bir ağaç. 32 hex karakterlik trace_id ile bulunur; süre, hata ve hangi servislerden geçtiği buradan okunur.",
		[]guidedAnswerLink{{Label: "Trace'ler", Href: "/traces"}}},
	"span": {"Span: bir trace içindeki tek bir işlem (HTTP çağrısı, DB sorgusu, kuyruk tüketimi…). Süresi, durumu (ok/error) ve attribute'ları vardır; parent→child ilişkisiyle trace ağacını kurar.",
		[]guidedAnswerLink{{Label: "Trace'ler", Href: "/traces"}}},
	"p50": {"p50 (medyan): isteklerin yarısının altında kaldığı gecikme. p95/p99 kuyruk gecikmesini gösterir; Coremetry yüzdelikleri span'lerden TDigest ile hesaplar — ortalama kuyruğu gizler, yüzdelik göstermez.",
		[]guidedAnswerLink{{Label: "Endpoint'ler", Href: "/endpoints"}}},
	"p95": {"p95: isteklerin %95'inin altında kaldığı gecikme (yüzdelik); %5'lik kuyruk bunun üstündedir. p50 medyan, p99 daha uzak kuyruk. Coremetry span'lerden TDigest ile hesaplar — ortalama yerine yüzdelik, çünkü kuyruk gecikmesini ortalama gizler.",
		[]guidedAnswerLink{{Label: "Endpoint'ler", Href: "/endpoints"}}},
	"p99": {"p99: isteklerin %99'unun altında kaldığı gecikme; en yavaş %1'lik kuyruğun sınırı. Kullanıcının 'bazen çok yavaş' dediği şey genelde p99'da görünür. Coremetry TDigest ile hesaplar.",
		[]guidedAnswerLink{{Label: "Endpoint'ler", Href: "/endpoints"}}},
	"yuzdelik": {"Yüzdelik (percentile): gecikme dağılımında belirli bir yüzdenin altında kalan değer — p50 medyan, p95 ve p99 kuyruk. Ortalama kuyruğu gizlediği için servis sağlığı yüzdelikle okunur.",
		[]guidedAnswerLink{{Label: "Endpoint'ler", Href: "/endpoints"}}},
	"apdex": {"Apdex: memnuniyet skoru = (tatmin edici + tolere edilebilir/2) / toplam istek. Eşik T'nin altı tatmin edici, 4T'ye kadar tolere edilebilir, üstü hayal kırıklığı; 1'e yakın iyi.",
		[]guidedAnswerLink{{Label: "SLO'lar", Href: "/slos"}}},
	"slo": {"SLO (Service Level Objective): hedef — ör. 30 günde isteklerin %99,9'u başarılı ve 300 ms altında. SLI onu ölçen oran, hata bütçesi hedefin izin verdiği hata payıdır; bütçe tükeniyorsa yayın hızı düşürülür.",
		[]guidedAnswerLink{{Label: "SLO'lar", Href: "/slos"}}},
	"sli": {"SLI (Service Level Indicator): SLO'yu ölçen oran — ör. başarılı istek / toplam istek, ya da 300 ms altındaki istek oranı. Coremetry SLI'ları giriş span'lerinden (server/consumer) hesaplar.",
		[]guidedAnswerLink{{Label: "SLO'lar", Href: "/slos"}}},
	"hatabutcesi": {"Hata bütçesi: SLO hedefinin izin verdiği hata payı (hedef %99,9 ise bütçe %0,1). Bütçe hızla tükeniyorsa (burn rate) yayınları yavaşlatmak, tükenmemişse hızlanmak için kullanılır.",
		[]guidedAnswerLink{{Label: "SLO'lar", Href: "/slos"}}},
	"problem": {"Problem: Coremetry dedektörlerinin (hata oranı, gecikme, tamamen kayıp servis, exception fırtınası, anomali) açtığı olay kaydı; önceliği (P1 şimdi / P2 bugün / P3 uygun zamanda) ve gerekçesi vardır, Inbox'ta yaşar, çözülünce kapanır.",
		[]guidedAnswerLink{{Label: "Problemler", Href: "/problems"}}},
	"exception": {"Exception grubu: aynı hata türü + mesaj şablonu + yığın imzasıyla gruplanmış exception'lar; ilk/son görülme, sayı, etkilenen servis ve pod'lar. Çok servisi aynı pencerede vuran yeni gruplar 'fırtına' olarak P1 problem açar.",
		[]guidedAnswerLink{{Label: "Exception'lar", Href: "/exceptions"}}},
	"anomali": {"Anomali: bir metriğin kendi geçmişine (mevsimsel baz çizgisi: önceki günlerin/haftaların aynı saati) göre beklenmedik sapması. Coremetry sapmayı kanıt zinciriyle kök nedene bağlamaya çalışır; formül değil korelasyon.",
		[]guidedAnswerLink{{Label: "Anomaliler", Href: "/anomalies"}}},
	"bazcizgisi": {"Baz çizgisi (baseline): bir metriğin 'normal' aralığı — önceki günlerin/haftaların aynı saatinden türetilir. Anomali bu aralıktan sapmadır; bant grafikte gölge olarak çizilir.",
		[]guidedAnswerLink{{Label: "Anomaliler", Href: "/anomalies"}}},
	"rollout": {"Rollout / deploy: bir servisin yeni sürüme geçişi (imaj etiketi ya da revizyon değişimi). Coremetry deploy olaylarını RED metrikleriyle yan yana koyar ('deploy etkisi') ama deploy tek başına problem önceliğini değiştirmez.",
		[]guidedAnswerLink{{Label: "Rollout'lar", Href: "/rollouts"}}},
	"servisharitasi": {"Servis haritası (topoloji): span'lerdeki parent→child çağrı ilişkilerinden türeyen servis-servis grafiği; kenarlar istek sayısı, hata oranı ve p95 taşır. 5 dakikalık ön-toplamlardan okunur.",
		[]guidedAnswerLink{{Label: "Servis haritası", Href: "/service-map"}}},
	"red": {"RED: Rate (istek/sn), Errors (hata oranı), Duration (gecikme) — servis sağlığının üç temel ölçüsü. Coremetry bunları giriş span'lerinden (kind server/consumer) hesaplar; iç span'ler sayıma girmez.",
		[]guidedAnswerLink{{Label: "Servisler", Href: "/services"}}},
	"opentelemetry": {"OpenTelemetry (OTel): trace, metrik ve log için açık standart ve SDK'lar. Coremetry veriyi OTLP (gRPC/HTTP) ile alır ve attribute'ları olduğu gibi saklar; kaynak gerçeği OTel'dir.", nil},
	"otlp":          {"OTLP: OpenTelemetry'nin tel protokolü (gRPC 4317 / HTTP 4318). Coremetry yalnız OTLP kabul eder; collector ya da SDK buraya yazar.", nil},
	"attribute": {"Attribute (öznitelik): span, log ya da kaynak üzerindeki anahtar-değer bilgisi — http.route, db.statement, k8s.pod.name gibi. Coremetry trace aramasını indeksli anahtarlar üzerinden yapar; anahtar indeksli değilse sonuç gelmez, uyarı verilir.",
		[]guidedAnswerLink{{Label: "Trace'ler", Href: "/traces"}}},
	"httproute": {"http.route: sunucu tarafındaki ŞABLONLU yol (/api/orders/{id}) — endpoint kırılımının anahtarı. url.full tam URL'dir (sorgu dizesiyle), http.target eski adıdır. OTel anlam kuralları.",
		[]guidedAnswerLink{{Label: "Endpoint'ler", Href: "/endpoints"}}},
	"urlfull": {"url.full: isteğin tam URL'i (şema + host + yol + sorgu). Şablonlu yol için http.route kullanılır; url.full yüksek kardinalitelidir, gruplamaya değil aramaya uygundur.",
		[]guidedAnswerLink{{Label: "Trace'ler", Href: "/traces"}}},
	"dbstatement": {"db.statement: span'deki çalışan SQL/sorgu metni (OTel). Coremetry aynı sorguyu hash'iyle gruplar (yavaş sorgular sayfası); metin olduğu gibi saklanır, maskelenmez.",
		[]guidedAnswerLink{{Label: "Veritabanları", Href: "/databases"}}},
	"exemplar": {"Exemplar: bir metrik kovasına bağlı örnek trace kimliği; grafikteki noktadan o anın gerçek trace'ine geçmeyi sağlar. Histogram/kova metriklerinde bulunur.",
		[]guidedAnswerLink{{Label: "Metrikler", Href: "/metrics"}}},
	"heatmap": {"Isı haritası (heatmap): zaman × gecikme kovalarında istek yoğunluğu; kuyruk gecikmesini ve dağılımın zamanla değişimini tek bakışta gösterir. 6 saatten uzun pencerede örneklenir.",
		[]guidedAnswerLink{{Label: "Trace'ler", Href: "/traces"}}},
	"flamegraph": {"Flame graph: CPU/bellek profilinde çağrı yığınlarının genişliğe göre çizimi; en geniş kutu en çok zaman harcayan yoldur, derinlik çağrı zinciridir.",
		[]guidedAnswerLink{{Label: "Profilleme", Href: "/profiling"}}},
	"kardinalite": {"Kardinalite: bir etiketin alabildiği farklı değer sayısı. Yüksek kardinalite (kullanıcı id, tam URL) metrik depolarını şişirir; bu yüzden gruplama http.route ile yapılır, url.full ile değil.", nil},
	"ornekleme":   {"Örnekleme (sampling): trace'lerin yalnız bir kısmının tutulması. Coremetry'nin içinde örnekleme yoktur; yapılacaksa OTel collector'da yapılır, metrikler örneklenmez (tam sayım).", nil},
	"gecikme": {"Gecikme (latency): isteğin başlangıçtan bitişe süresi; throughput ise birim zamandaki istek sayısı. Gecikme yüzdelikle (p50/p95/p99), throughput istek/sn ile okunur.",
		[]guidedAnswerLink{{Label: "Servisler", Href: "/services"}}},
	"throughput": {"Throughput (verim): birim zamandaki istek sayısı (istek/sn). RED'in Rate'i; düşüşü çoğu zaman yukarı akıştaki bir sorunun, artışı trafiğin işaretidir.",
		[]guidedAnswerLink{{Label: "Servisler", Href: "/services"}}},
	"consumerlag": {"Consumer lag: kuyruktaki son mesaj ile tüketicinin işlediği son mesaj arasındaki fark (offset ya da süre). Büyüyorsa tüketici yetişemiyor; Coremetry messaging sayfasında izler.",
		[]guidedAnswerLink{{Label: "Messaging", Href: "/messaging"}}},
	"oomkilled": {"OOMKilled: konteyner bellek sınırını aşınca Kubernetes tarafından öldürüldü (exit 137). Bellek sınırını ya da uygulamanın tüketimini gözden geçir; restart sayısı pod sayfasında.",
		[]guidedAnswerLink{{Label: "Cluster'lar", Href: "/clusters"}}},
	"crashloopbackoff": {"CrashLoopBackOff: pod arka arkaya çöküp yeniden başlıyor, Kubernetes her denemede bekleme süresini artırıyor. Genelde başlangıç hatası (config, bağımlılık) ya da OOM; son loglara bak.",
		[]guidedAnswerLink{{Label: "Cluster'lar", Href: "/clusters"}}},
	"cosre": {"CoSRE: Coremetry'nin yerleşik SRE asistanı. Telemetriyi (trace, log, metrik, problem, deploy) araçlarla okuyup kanıtla cevaplar; kanıtta olmayan servis adını ya da sayıyı söylemez. Tanınan sorular deterministik yönlendirilir, gerisi modele gider.",
		[]guidedAnswerLink{{Label: "AI", Href: "/ai"}}},
	"materializedview": {"Materialized view (MV): ClickHouse'un yazım anında hesapladığı ön-toplam (5 dk kovalar: servis, operasyon, topoloji, DB). Coremetry özetleri ham span yerine buradan okur; ham span'e inen bir özet performans hatasıdır.", nil},
	"tdigest":          {"TDigest: yüzdelikleri yaklaşık ama BİRLEŞTİRİLEBİLİR biçimde saklayan yapı; p95/p99 ön-toplamlarda bununla tutulur, kovalar birleştirilince yüzdelik doğru kalır (rezervuar örnekleme kalmaz).", nil},
	"apm":              {"APM (Application Performance Monitoring): uygulamanın isteklerini, hatalarını ve gecikmesini uçtan uca izleme. Coremetry OTel-yerli bir APM'dir: trace + log + metrik tek yerde, korelasyonla.", nil},
}

// glossaryAliases — normalize edilmiş takma adlar → kanonik anahtar.
var glossaryAliases = map[string]string{
	"istekkimligi": "requestid", "istekid": "requestid", "reqid": "requestid", "xrequestid": "requestid", "correlationid": "requestid", "korelasyonkimligi": "requestid",
	"traceid": "trace", "iz": "trace", "spanid": "span", "percentile": "yuzdelik", "yuzdebirlik": "yuzdelik", "p999": "p99",
	"errorbudget": "hatabutcesi", "hatabutce": "hatabutcesi", "problemler": "problem", "exceptiongroup": "exception", "exceptiongrubu": "exception", "istisna": "exception",
	"anomaly": "anomali", "baseline": "bazcizgisi", "deploy": "rollout", "deployment": "rollout", "dagitim": "rollout", "yayin": "rollout",
	"servicemap": "servisharitasi", "topoloji": "servisharitasi", "topology": "servisharitasi", "servismap": "servisharitasi",
	"redmetrikleri": "red", "redmetrics": "red", "otel": "opentelemetry", "attr": "attribute", "oznitelik": "attribute",
	"latency": "gecikme", "verim": "throughput", "kafkalag": "consumerlag", "lag": "consumerlag", "consumer": "consumerlag",
	"oom": "oomkilled", "crashloop": "crashloopbackoff", "coremetry": "cosre", "mv": "materializedview", "sampling": "ornekleme",
	"isiharitasi": "heatmap", "alevgrafigi": "flamegraph", "flame": "flamegraph", "cardinality": "kardinalite", "servicelevelobjective": "slo",
}

var (
	glossaryTRRe = regexp.MustCompile(`^(?:peki\s+)?(.+?)\s+(?:nedir|ne demek(?:tir)?|ne anlama gel(?:ir|iyor)|ne demek oluyor|neyi ifade eder|nas[ıi]l hesaplan[ıi]r|ne i[şs]e yarar|neyin k[ıi]saltmas[ıi])\s*\??\s*$`)
	glossaryENRe = regexp.MustCompile(`^(?:what is|what's|whats|what does|define|explain the term|meaning of)\s+(?:a |an |the )?(.+?)(?:\s+mean)?\s*\??\s*$`)
)

// glossaryKey — harf+rakam dışı her şeyi atar, Türkçe harfleri ASCII'ye
// katlar ("baz çizgisi" → "bazcizgisi").
func glossaryKey(term string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(term)) {
		switch r {
		case 'ç':
			r = 'c'
		case 'ğ':
			r = 'g'
		case 'ı':
			r = 'i'
		case 'ö':
			r = 'o'
		case 'ş':
			r = 's'
		case 'ü':
			r = 'u'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// glossaryFind — takma adı kanonik anahtara indirir; kanonik anahtarı döner.
func glossaryFind(key string) (string, glossaryEntry, bool) {
	if k, ok := glossaryAliases[key]; ok {
		key = k
	}
	e, ok := glossaryTerms[key]
	return key, e, ok
}

// glossaryLookup — SAF: normalize edilmiş mesaj → tanım. Terim ek almış
// olabilir ("trace'in", "p95in", "span'ın"): anahtar bulunamazsa sondan
// 1..4 karakter kırpılarak denenir (≥3 karakter kalır).
func glossaryLookup(norm string) (term string, entry glossaryEntry, ok bool) {
	q := strings.TrimSpace(norm)
	var raw string
	if m := glossaryTRRe.FindStringSubmatch(q); m != nil {
		raw = m[1]
	} else if m := glossaryENRe.FindStringSubmatch(q); m != nil {
		raw = m[1]
	} else {
		return "", glossaryEntry{}, false
	}
	key := glossaryKey(raw)
	if key == "" || len(key) > 40 {
		return "", glossaryEntry{}, false
	}
	if k, e, ok := glossaryFind(key); ok {
		return k, e, true
	}
	for cut := 1; cut <= 4 && len(key)-cut >= 3; cut++ {
		if k, e, ok := glossaryFind(key[:len(key)-cut]); ok {
			return k, e, true
		}
	}
	return "", glossaryEntry{}, false
}

// glossaryAnswer — answer olayının gövdesi: exchangeId YOK (deterministik),
// öneriler küresel (her biri router'da yönlenir).
func glossaryAnswer(entry glossaryEntry) map[string]any {
	ans := map[string]any{"text": entry.Text, "suggestions": []string{"Açık problemler?", "En yavaş trace'ler?"}}
	if len(entry.Links) > 0 {
		ans["links"] = entry.Links
	}
	return ans
}
