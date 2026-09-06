package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcptools"
	"github.com/cilcenk/coremetry/internal/reqid"
)

// Guided chat mode (v0.8.397 — AI audit A3, Davis-CoPilot-style).
//
// The free agentic tool loop (copilot_chat.go: up to 5 rounds × 11
// JSON tool schemas) is unreliable on the 2B-class model that is the
// PRIMARY production target (qwen3.5-2b on vLLM): schema soup, wrong
// tool picks, empty answers. Guided mode inverts the control flow —
// a deterministic intent router recognises the highest-value question
// shapes (Turkish + English), the SERVER prefetches the relevant data
// with the existing bounded chstore/logstore reads, renders a compact
// Turkish evidence block, and the model makes exactly ONE tool-less
// narration call (the analyze-service pattern, copilot_aianalyze.go).
//
// Mode selection is config-free: the router runs first for EVERY
// provider — for these five shapes a deterministic prefetch beats
// tool-roulette even on frontier models. Unmatched questions fall
// through to the free tool loop UNCHANGED (frontier models keep full
// power; on small models unmatched questions may still flounder —
// accepted trade-off, documented in docs/ai-enhancement-audit.md §3).
//
// SSE contract is unchanged: the prefetches emit the same `step`
// events the tool loop emits (CopilotChat.tsx renders e.tool chips),
// then one `answer`, then `done`. The single Explain call self-records
// one ai_calls row under the "chat-guided" surface so the /ai page can
// track guided-path quality separately from the free loop.
//
// v0.8.398 (AI audit env-awareness slice): the router also extracts a
// deployment environment against the LIVE env list ("uat ortamındaki
// hatalar", "errors in the uat environment", bare "uat") and threads
// it into the bundles — problems via ProblemFilter.Env (service-
// scoped), slow traces via TraceFilter.Env (deploy_env conjunct);
// env-less data paths (service RED context, logs, deploys) state the
// limitation in the evidence instead of silently ignoring the ask.

// ─── Intent router (pure, table-tested) ─────────────────────────────

type guidedIntent string

const (
	guidedNone          guidedIntent = ""
	guidedProblems      guidedIntent = "problems"
	guidedServiceHealth guidedIntent = "service_health"
	guidedSlowTraces    guidedIntent = "slow_traces"
	guidedDeployImpact  guidedIntent = "deploy_impact"
	guidedLogErrors     guidedIntent = "log_errors"
	// guidedLogField (v0.10.433, CoSRE router boşlukları D5) — alan süzgeçli
	// log araması: "url.full alanında \"/x\" geçen loglar" (log_field_search.go).
	guidedLogField guidedIntent = "log_field"
	// guidedOpenPage (v0.10.434, D7b) — "X sayfasını aç": LLM'siz link + open.
	guidedOpenPage guidedIntent = "open_page"
	// v0.10.436 (D2) — "A'dan B'ye giden istekler" / "X servisinde içinde … geçen trace'ler".
	guidedPairRequests guidedIntent = "pair_requests"
	guidedTraceSearch  guidedIntent = "trace_search"
	// v0.10.437 (D6) — iki mutlak pencerenin RED kıyası.
	guidedWindowCompare guidedIntent = "window_compare"
	// v0.10.438 (D3) — çağrı periyodu (A→B ya da tek servis).
	guidedCallPeriod guidedIntent = "call_period"
	// v0.10.439 (D4) — A→B→C fan-out (örnek tabanlı).
	guidedFanout guidedIntent = "fanout"
	// guidedFamilyHealth (v0.9.192) — a family-of-services ask:
	// "mobile bff'lerde hangisinde hata var". No single service
	// resolves, but the message's name-fragments (mobile + bff) match
	// 2+ live services as bounded name segments → one MV read compares
	// the family's RED side by side.
	guidedFamilyHealth guidedIntent = "family_health"
	// v0.9.375 (operatör istegi) — "takımımın servisleri/problemleri":
	// login kullanıcının User.Team'i servis metadata'sının ownerTeam/
	// sreTeam'iyle eşlenir; kimlik CallMeta'dan (ctx) okunur.
	guidedMyServices guidedIntent = "my_services"
	guidedMyProblems guidedIntent = "my_problems"
	// guidedTeamServices (v0.9.1134, operatör istegi) — ADI GEÇEN bir
	// takımın servisleri, EN ÇOK HATA ALAN önce. my_services'in iyelik
	// olmayan ikizi: kimlik yerine mesajdaki takım adı özneyi verir.
	//
	// Asıl işi "hangi takım?" diyaloğunu kapatmak: takımı bilinmeyen
	// kullanıcıya canlı takım listesi ÇİP olarak sunulur, çipe tıklamak
	// ÇIPLAK bir takım adı gönderir ve o mesaj buraya yönlenir. Sunucuda
	// konuşma durumu YOK — akış yalnızca çıplak takım adının kendi
	// başına yönlenebilir olması sayesinde çalışıyor.
	guidedTeamServices guidedIntent = "team_services"
	// v0.9.650 (operatör: "Takımıma ait servislerin hataları
	// (Exceptions) neler?") — Problem ve Exception AYRI yüzeyler:
	// Problem bir alarm kuralının açtığı kayıt, Exception ise
	// span'lerden gruplanan ham hata. my_problems ikincisini
	// kapsamıyordu.
	guidedMyExceptions guidedIntent = "my_exceptions"
	// v0.9.376 (operatör istegi, SRE perspektifi) — pod/JVM sağlığı:
	// servisliyse instance listesi + JVM heap; servissizse filo-geneli
	// heap doluluk sıralaması. Veri CH'den (OTel runtime metrikleri) —
	// Thanos şartı yok; restart sayıları KSM gerektirir ve kanıt bunu söyler.
	guidedPodHealth guidedIntent = "pod_health"
	// v0.9.416 (CoSRE fikir #2) — vardiya özeti: "dün gece neler oldu?"
	// Pencere içinde açılan/çözülen problemler + anomali olayları +
	// deploy'lar + yeni P1 exception grupları tek cevapta. Varsayılan
	// pencere 12h (vardiya), açık range her zaman kazanır.
	guidedShiftSummary guidedIntent = "shift_summary"
	// v0.9.420 (CoSRE fikir #5) — bağımlılık sağlığı: "hangi db yavaş?",
	// "kafka lag nasıl?". db_summary_5m / msg MV'lerinden top-N kırılım.
	// guidedRootCause (v0.9.514) — "neden X patladı / sebebi ne".
	// Bir SRE'nin sorduğu EN değerli soru bugüne kadar kendi intent'i
	// olmadan yaşıyordu: ikincil sinyaline savruluyordu ("neden yavaşladı"
	// → slow_traces, yani "yavaş mı" sorusuna çöküyordu) ya da kırılgan
	// serbest tool-loop'a düşüyordu. Kayıtlı kök-neden hipotezi (v0.8.394
	// worker + v0.9.510-512 derin soruşturma) zaten hazır bekliyordu.
	guidedRootCause       guidedIntent = "root_cause"
	guidedDBHealth        guidedIntent = "db_health"
	guidedMessagingHealth guidedIntent = "messaging_health"

	// guidedTraceByID — v0.9.537 (operator-reported: sohbete yapıştırılan
	// trace ID'si "yüklü dokümanlarda bu bilgi yok" cevabına düşüyordu —
	// hiçbir intent 32-hex'i tanımıyor, soru RAG doküman yoluna gidiyordu).
	guidedTraceByID guidedIntent = "trace_by_id"

	// guidedSelfMeta — v0.10.13 (operatör bildirimi: "sen hangi modelsin"
	// sorunca 'Yüklü dokümanlarda bu bilgi yok' diyor).
	//
	// Asistanın KENDİSİ hakkındaki soru ne telemetriye ne dokümana ait.
	// Hiçbir intent tanımadığı için RAG'a düşüyordu ve cevap orada
	// olmadığı için ölü dönüyordu — v0.9.537 (trace ID) ve v0.9.1142
	// (kuyruk birikmesi) ile AYNI sınıf, üçüncü kez.
	//
	// Cevap zaten biliniyor: model adı yapılandırmada (ActiveModel).
	guidedSelfMeta guidedIntent = "self_meta"

	// guidedSpanByID — v0.9.548. Çıplak 16-hex SPAN id'si. Trace'i
	// aranır, bulununca aynı trace kanıt paketi kullanılır.
	guidedSpanByID guidedIntent = "span_by_id"

	// guidedRequestID — v0.9.1142 (operatör istegi). Kurumun kendi
	// YAPILANDIRILMIŞ istek numarası: sabit genişlikli tek bir string ve
	// İÇİNDE işlemin tarihi+saati yazılı (internal/reqid).
	//
	// Neden kendi rotası: operatörün elinde olan kimlik bu — trace/span
	// id değil. Bugüne kadar böyle bir kimlik hiçbir intent'e uymuyordu,
	// yani soru ya RAG doküman yoluna ya kırılgan serbest tool-loop'a
	// düşüyordu (v0.9.537'nin trace-ID'siyle aynı kaza sınıfı).
	//
	// Zincir: kimlik → gömülü zaman → o pencerede log araması →
	// eşleşen kaydın trace_id'si → v0.9.537'nin trace kanıt paketi.
	// İkinci bir montaj YOK.
	guidedRequestID guidedIntent = "request_id"
	// guidedAskService (v0.10.429, D1) — servis adı ÇÖZÜLEMEDİ ya da
	// BELİRSİZ: sessizce none yerine "hangisini kastettin?" — adaylar
	// route.ServiceOptions, sorulan asıl niyet route.AskIntent (çipler o
	// niyetin tam kılavuz cümlesi olur). Router-içi; sınıflandırıcı
	// beyaz listesinde değil (parseIntentJSON kendisi üretir).
	guidedAskService guidedIntent = "ask_service"
)

type guidedRoute struct {
	Intent  guidedIntent
	Service string // extracted entity, "" = none/global
	// Env (v0.8.398 — AI audit env-awareness slice) is the deployment
	// environment extracted from the question against the LIVE env
	// list (ListEnvironments), "" = no env narrowing. Threaded into
	// the prefetch bundles: problems → ProblemFilter.Env (service-
	// scoped, env_members.go), slow_traces → TraceFilter.Env (direct
	// deploy_env conjunct). Logs/deploys carry no env path yet
	// (env-separation Phase 4 pending) — those bundles SAY so in the
	// evidence instead of silently ignoring the ask.
	Env string
	// Family (v0.9.192) — the resolved service list for a
	// guidedFamilyHealth route ("mobile bff'ler"); nil otherwise.
	Family []string
	// TeamServices (v0.9.651) — takım-kapsamlı bir rotada ÇÖZÜLEN servis
	// listesi; Family emsali. Rotanın GİRDİSİ değil, paketin ÇIKTISI:
	// guidedMyTeamBundle dolduruyor, guidedSuggestions okuyor.
	//
	// Neden gerekli: takım servisleri listelendikten sonraki çipler
	// jenerikti ("Açık problemler?"), yani operatör bir servis SEÇİP
	// onun loglarına/trace'lerine inemiyordu. Servis-kapsamlı çipler
	// (svc + " hata logları?" vb.) ZATEN vardı — eksik olan tek halka,
	// çiplere hangi servislerin yazılacağıydı.
	TeamServices []string
	// Team (v0.9.1134) — guidedTeamServices rotasının ÖZNESİ: mesajda
	// adı geçen canlı takım (extractTeamEntity). TeamServices'in tersi
	// yönü — bu rotanın GİRDİSİ, çıktısı değil.
	Team string
	// TeamOptions (v0.9.1134) — "hangi takım?" diyaloğunda operatöre
	// SUNULACAK canlı takım adları (servis sayısına göre en büyükler).
	// Rotanın çıktısı: guidedMyTeamBundle takımsız kullanıcıda dolduruyor,
	// guidedSuggestions çipe çeviriyor. Çipin metni ÇIPLAK takım adıdır —
	// tıklandığında guidedTeamServices'e yönlenir, yani diyalog sunucuda
	// durum tutmadan kapanır.
	TeamOptions []string
	// TraceID (v0.9.537) — guidedTraceByID rotasının öznesi (32-hex).
	TraceID string
	// SpanID (v0.9.548) — guidedSpanByID rotasının öznesi (16-hex).
	SpanID string
	// RequestID (v0.9.1142) — guidedRequestID rotasının öznesi: kurumsal
	// yapılandırılmış istek numarası, ORİJİNAL harf kasasıyla (log araması
	// keyword alanlarında harfe duyarlı olabilir).
	RequestID string
	// ReqWindowFromMs / ReqWindowToMs (v0.9.1142) — kimliğin damgasından
	// türeyen arama penceresi, epoch MİLİSANİYE. Rotanın ÇIKTISI
	// (TeamServices emsali): bundle dolduruyor, guidedAnswerLinks /logs
	// derin linkini bundan kuruyor. ms çünkü /logs'un okuduğu tek mutlak
	// pencere token'ı `range=custom:<fromMs>-<toMs>` (logsUrl.ts) —
	// ns yazan çip ÖLÜ paramdı (v0.9.853 K3 dersi).
	ReqWindowFromMs int64
	ReqWindowToMs   int64
	// ServiceOptions / AskIntent (v0.10.429, D1) — guidedAskService rotası:
	// operatöre sunulacak canlı servis adayları ve çiplerin taşıyacağı
	// niyet. TeamOptions'ın servis ikizi; çip metni ÇIPLAK ad DEĞİL, tam
	// kılavuz cümle ("<svc> sağlığı nasıl?") — çıplak ad kapıdan geçmez.
	ServiceOptions []string
	AskIntent      guidedIntent
	// LogField / LogValue / LogContains (v0.10.433, D5) — alan süzgeçli log
	// araması; LogQuery bundle'ın KOŞTUĞU sorgu (backend'e göre şekil), link
	// aynı sorguyu taşır (ReqWindowFromMs deseni: rota alanı bundle çıktısı).
	LogField    string
	LogValue    string
	LogContains bool
	LogQuery    string
	// Page (v0.10.434, D7b) — open_page hedefi: overview|problems|logs|traces|endpoints.
	Page string
	// v0.10.436 (D2) — pair_requests: PairFrom servis, PairTo servis ya da
	// düğüm parçası (PairToKind "service"|"node"; bundle çözülen düğüm adını
	// yazar), PairMissing "from"|"to" (ask çipleri için); trace_search:
	// SearchText + SearchSQL (db.statement LIKE).
	PairFrom    string
	PairTo      string
	PairToKind  string
	PairMissing string
	SearchText  string
	SearchSQL   bool
	// v0.10.437 (D6) — window_compare: iki mutlak pencere; WindowText
	// mesajdaki tarih/saat parçaları (ask çipleri aynı pencereyi taşır).
	Windows    []absWindow
	WindowText string
	// v0.10.439 (D4) — fanout: üçüncü düğüm.
	FanoutTo     string
	FanoutToKind string
	// v0.10.463 (D1, find_entity.go) — liste sorusu / sorulan ifade (aday turu).
	FindList  bool
	FindQuery string
}

// normalizeGuidedMsg lowercases for matching. Go's ToLower maps the
// Turkish dotted capital İ to "i"+U+0307 (combining dot above); we
// strip the combining dot so "İstek" matches keyword "istek".
func normalizeGuidedMsg(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "̇", "")
}

// guidedTokens splits a normalized message into word tokens. The
// charset keeps service-name characters ([a-z0-9._-]) AND Turkish
// letters together so both "mobile-bff-uat" and "loglarında" survive
// as single tokens. Apostrophes are boundaries, which conveniently
// detaches Turkish possessive suffixes ("checkout-service'in" →
// "checkout-service", "in").
func guidedTokens(msg string) []string {
	return strings.FieldsFunc(msg, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return false
		case r == '.' || r == '_' || r == '-':
			return false
		case r == 'ç' || r == 'ğ' || r == 'ı' || r == 'ö' || r == 'ş' || r == 'ü':
			return false
		}
		return true
	})
}

// tokenHasPrefix reports whether any token starts with any of the
// given stems. Prefix (not equality) matching absorbs Turkish
// agglutination ("hata" matches "hatalar", "hataları", "hatası").
func tokenHasPrefix(tokens []string, stems ...string) bool {
	for _, t := range tokens {
		for _, s := range stems {
			if strings.HasPrefix(t, s) {
				return true
			}
		}
	}
	return false
}

func hasSlowTraceSignal(msg string) bool {
	// v0.9.570 — KELİME SINIRINDA eşleşme. Sınırsız Contains,
	// "bu trace neden yavaş?" sorusunu "en yavaş" kalıbına
	// çarptırıp (ned|EN YAVAŞ) filo geneli liste döndürüyordu.
	// "neden/niye yavaş" AÇIK kalıp olarak listede: bağlamsız sorulduğunda
	// "en yavaş trace'ler" makul bir cevaptır ve bu sözleşme testlerle
	// pinli. Sınırlı eşleşme sayesinde artık kazayla değil BİLEREK
	// eşleşiyor — ve ekranda bir trace varsa aşağıdaki işaret-zamiri
	// kapısı onu geçersiz kılıyor.
	for _, p := range []string{
		"en yavaş", "slowest", "slow trace", "yavaş trace", "en uzun",
		"neden yavaş", "niye yavaş",
	} {
		if containsPhrase(msg, p) {
			return true
		}
	}
	return false
}

func hasDeploySignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "deploy", "rollout", "sürüm", "release")
}

// hasLogSignal: token-bounded so "login" / "catalog" / "topology"
// never trigger the logs intent. Covers the common Turkish case
// suffixes (loglar, loglarında, logunda, logda, logta).
func hasLogSignal(tokens []string) bool {
	for _, t := range tokens {
		if t == "log" || t == "logs" ||
			strings.HasPrefix(t, "loglar") || strings.HasPrefix(t, "logu") ||
			strings.HasPrefix(t, "logd") || strings.HasPrefix(t, "logt") {
			return true
		}
	}
	return false
}

func hasErrorSignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "hata", "error", "exception", "fail", "başarısız", "5xx", "500")
}

// hasExceptionWord — YALNIZ "exception/istisna" kelimesi.
//
// hasErrorSignal "exception"ı da kapsıyor ama çok geniş: "hata" da ona
// giriyor. Takım sorusunu Problem'lerden Exception'lara çevirmek için
// operatörün AÇIKÇA exception demesi gerekiyor — "takımımın hataları"
// bugünkü davranışta (açık problemler) kalsın, "takımımın
// exception'ları" ham hata gruplarına gitsin.
func hasExceptionWord(tokens []string) bool {
	return tokenHasPrefix(tokens, "exception", "istisna")
}

func hasProblemSignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "problem", "sorun", "alarm", "alert", "incident", "arıza", "wrong")
}

func hasHealthSignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "sağl", "health", "durum", "nasıl", "yavaş", "slow",
		"gecikme", "latency", "performan", "p99", "p95", "iyi")
}

// hasShiftSignal (v0.9.416) — vardiya-özeti şekilleri: "vardiya",
// "overnight"/"shift", "neler oldu / ne oldu" kalıpları ve "gece".
// "gece" tek başına yeter çünkü switch'te shift, daha SPESİFİK
// sinyallerden (slow-trace/deploy/log/pod) SONRA gelir — "dün gece
// en yavaş trace'ler" slowTraces'ta kalır.
func hasShiftSignal(msg string, toks []string) bool {
	if tokenHasPrefix(toks, "vardiya", "overnight", "shift", "gece") {
		return true
	}
	return strings.Contains(msg, "neler oldu") || strings.Contains(msg, "ne oldu") ||
		strings.Contains(msg, "what happened")
}

// hasDBSignal (v0.9.420) — veritabanı-şekilli sorular. "db"/"sql"
// tam-token ("dbConnection"/"sqlite" gibi ad parçaları tetiklemesin);
// sistem adları prefix ("postgresql", "postgres'te").
func hasDBSignal(toks []string) bool {
	for _, t := range toks {
		if t == "db" || t == "sql" {
			return true
		}
	}
	return tokenHasPrefix(toks, "database", "veritaban", "postgres", "mysql",
		"oracle", "mongo", "redis", "cassandra", "mssql")
}

// hasMessagingSignal (v0.9.420) — mesajlaşma-şekilli sorular. "mq"
// tam-token; "kuyruk"/"queue" BİLEREK YOK — operatör dilinde "kuyruk"
// iş listesi demek (CLAUDE.md), messaging'e devretmek yanlış yönlendirir.
func hasMessagingSignal(toks []string) bool {
	for _, t := range toks {
		if t == "mq" {
			return true
		}
	}
	if tokenHasPrefix(toks, "kafka", "rabbit", "topic", "artemis",
		"activemq", "ibmmq", "nats", "messaging", "jms", "mesajla") {
		return true
	}
	// v0.9.590 — "kuyruk" TEK BAŞINA hâlâ yok (yukarıdaki gerekçe
	// geçerli), ama BİRİKME sözcüğüyle birlikte belirsizlik kalmıyor:
	// operatörün iş listesinde "birikme/lag/gecikme" olmaz, Kafka'da
	// olur. Bu yüzden kapı bir BİRLEŞİM — iki taraf da gerekli.
	//
	// Boşluk gerçekti: "kuyrukta birikme var mı" HİÇBİR intent'e
	// düşmüyordu, yani serbest/RAG yoluna savruluyor ve orada
	// "yüklü dokümanlarda bu bilgi yok" cevabını alıyordu — elimizde
	// tam da o soruyu cevaplayan MV varken.
	return tokenHasPrefix(toks, "kuyru", "queue") &&
		tokenHasPrefix(toks, "birik", "lag", "gecik", "bekleyen", "tüket", "tuket")
}

// hasGuidedSignal is the cheap precheck the handler runs BEFORE
// fetching the live service list — a message with no guided keyword
// at all skips the catalogue read and goes straight to the tool loop.
// guidedTraceIDRe — v0.9.537. 32-hex = W3C trace ID; operatörün en
// doğal hamlesi ID'yi sohbete yapıştırmak. 16-hex BİLİNÇLİ dışarıda:
// o span ID'sidir ve tek başına yapıştırıldığında hangi trace'e ait
// olduğu ancak spans taramasıyla bulunur — ayrı iş.
var guidedTraceIDRe = regexp.MustCompile(`\b[0-9a-f]{32}\b`)

func extractTraceID(msg string) string {
	return guidedTraceIDRe.FindString(msg)
}

// guidedSpanIDRe — v0.9.548. 16-hex = W3C span ID. \b sınırları
// sayesinde 32-hex trace ID'nin İÇİNDEKİ 16'lık dizileri YAKALAMAZ;
// yine de çağrı sırası önemli — trace ID her zaman önce denenir.
var guidedSpanIDRe = regexp.MustCompile(`\b[0-9a-f]{16}\b`)

func extractSpanID(msg string) string {
	return guidedSpanIDRe.FindString(msg)
}

func hasGuidedSignal(msg string) bool {
	toks := guidedTokens(msg)
	return hasSlowTraceSignal(msg) || hasDeploySignal(toks) ||
		hasLogSignal(toks) || hasErrorSignal(toks) ||
		hasProblemSignal(toks) || hasHealthSignal(toks) ||
		hasTeamSelfSignal(toks) || hasPodSignal(toks) ||
		hasShiftSignal(msg, toks) || hasDBSignal(toks) || hasMessagingSignal(toks) ||
		hasWhySignal(toks) || hasPeriodSignal(toks) || // v0.10.438 (D3)
		// v0.9.537 — açık trace ID'si ya da "trace" kökü (prefix:
		// Türkçe ekli hâlleri de yakalar — "tracei", "trace'in";
		// ekrandaki trace'e bağlamdan gitmek için, ctxTrace çözümü
		// dispatch'te).
		extractTraceID(msg) != "" || extractSpanID(msg) != "" ||
		tokenHasPrefix(toks, "trace") || tokenHasPrefix(toks, "span") ||
		// v0.9.1142 — yapıştırılan kurumsal request kimliği hiçbir guided
		// KELİME taşımıyor ("şu isteğe ne oldu: ABCD…"), o yüzden sinyal
		// listesine kendisi giriyor; aksi hâlde sıfır-maliyet kapısı
		// mesajı serbest döngüye bırakırdı.
		hasStructuredRequestID(msg)
}

// hasStructuredRequestID — mesaj yapılandırılmış bir kurumsal istek
// numarası taşıyor mu (internal/reqid). Tespit harfe duyarsız, o yüzden
// normalize edilmiş metinle çalışır; ARAMADA kullanılacak token router'da
// `raw`dan alınır.
func hasStructuredRequestID(msg string) bool {
	if _, ok := reqid.FindToken(msg); ok {
		return true
	}
	// v0.9.1144 — gevşek eş de sinyaldir: parse edilemeyen kimlik-benzeri
	// token'ın RAG'a/serbest döngüye düşmemesi router'daki gevşek dalla
	// AYNI koşula bağlı; sinyal kapısı daha dar kalırsa dal hiç çalışmaz.
	_, ok := reqid.FindLooseToken(msg)
	return ok
}

// hasWhySignal (v0.9.514) — NEDENSELLİK soran şekiller. Türkçe ekler
// için prefix ("neden", "nedeni", "nedenini", "sebep", "sebebi"),
// İngilizce için tam token.
//
// "kaynak" BİLEREK dışarıda: Türkçede "kaynak kullanımı" (resource
// usage) çok daha sık ve o soru doygunluk yolu, nedensellik değil.
func hasWhySignal(tokens []string) bool {
	// "sebeb" AYRI stem: Türkçe ünsüz yumuşaması sebep→sebebi, yani
	// "sebep" prefix'i çekimli hali yakalamaz.
	if tokenHasPrefix(tokens, "neden", "niye", "niçin", "sebep", "sebeb") {
		return true
	}
	for _, t := range tokens {
		switch t {
		case "why", "cause", "reason", "rootcause":
			return true
		}
	}
	return false
}

// hasPodSignal (v0.9.376) — pod/JVM şekilli sorular. "gc" tam-token
// ("gcp" prefix tuzağı); diğerleri prefix (podlar, jvm'de, heap'i).
func hasPodSignal(tokens []string) bool {
	if tokenHasPrefix(tokens, "pod", "jvm", "heap") {
		return true
	}
	for _, t := range tokens {
		if t == "gc" {
			return true
		}
	}
	return false
}

// hasTeamSelfSignal (v0.9.375) — "kendi takımım" şekilli sorular.
// Türkçe iyelik ekleri prefix'le emilir ("takımımın", "servislerimin");
// İngilizce "my" TEK BAŞINA yeterli değildir (my + team/services/
// problems ister) — "mysql" gibi adlar prefix tuzağına düşmesin diye
// "my" tam-token eşlenir.
func hasTeamSelfSignal(tokens []string) bool {
	if tokenHasPrefix(tokens, "takımım", "ekibim", "servislerim", "problemlerim") {
		return true
	}
	my := false
	benim := false
	for _, t := range tokens {
		switch t {
		case "my":
			my = true
		case "benim", "bizim":
			benim = true
		}
	}
	if my && tokenHasPrefix(tokens, "team", "service", "problem") {
		return true
	}
	// v0.9.650 — Türkçe iyelik SİMETRİSİ. Üstteki önek listesi yalnız
	// "takımım" ekini tanıyordu; "benim takımın", "bizim takımda",
	// "benim ekibin" gibi çok doğal kalıplar DÜŞÜYOR ve soru sessizce
	// FİLO GENELİNE gidiyordu — operatör "takımımın" dediğini sanırken
	// tüm kurumun problemlerini görüyordu.
	//
	// İngilizce dalı ("my" + team/service/problem) bu işi zaten yapıyor;
	// Türkçesinde karşılığı yoktu. Regresyon testiyle bulundu.
	return benim && tokenHasPrefix(tokens, "takım", "ekip", "servis", "problem", "exception")
}

// guidedStopwords are message tokens that must never be treated as a
// service-name candidate in the unique-prefix fallback.
var guidedStopwords = map[string]bool{
	"servis": true, "servisi": true, "servisin": true, "service": true, "services": true,
	"trace": true, "traces": true, "log": true, "logs": true, "error": true, "errors": true,
	"deploy": true, "deploys": true, "deployment": true, "release": true, "rollout": true,
	"slow": true, "slowest": true, "health": true, "healthy": true, "latency": true,
	"son": true, "last": true, "the": true, "for": true, "and": true, "show": true,
	"what": true, "olan": true, "neden": true, "most": true, "hour": true, "hours": true,
	"minute": true, "minutes": true, "day": true, "days": true, "with": true, "problem": true,
	"problems": true, "alert": true, "alerts": true, "incident": true, "how": true,
}

// asciiNameToken reports whether the token could be a service name
// (chstore's serviceTokenRe convention: ascii-only, so Turkish words
// with diacritics never collide with a service).
func asciiNameToken(t string) bool {
	for _, r := range t {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func isNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '.' || b == '_' || b == '-'
}

// indexBounded finds sub inside msg with word-ish boundaries: the
// characters on either side must not be service-name characters, so
// "mobile-bff" never matches inside "mobile-bff-uat".
func indexBounded(msg, sub string) int {
	for start := 0; ; {
		i := strings.Index(msg[start:], sub)
		if i < 0 {
			return -1
		}
		i += start
		leftOK := i == 0 || !isNameChar(msg[i-1])
		r := i + len(sub)
		rightOK := r >= len(msg) || !isNameChar(msg[r])
		if leftOK && rightOK {
			return i
		}
		start = i + 1
	}
}

// extractServiceEntity matches the message against the LIVE service
// list (never a guess): first the longest bounded full-name substring
// (so the suffixed "mobile-bff-uat" beats its "mobile-bff" prefix
// sibling), then a unique-prefix token fallback so "checkout servisi"
// resolves to "checkout-service" when exactly one service starts with
// that token. Ambiguous prefixes (2+ matches) return "" — deterministic
// beats clever.
func extractServiceEntity(msg string, services, envs []string) string {
	best := ""
	for _, svc := range services {
		ls := strings.ToLower(svc)
		if len(ls) < 3 || len(ls) <= len(best) {
			continue
		}
		if indexBounded(msg, ls) >= 0 {
			best = svc
		}
	}
	if best != "" {
		return best
	}
	// v0.8.398 — a token that names a LIVE deployment environment is
	// never a service-PREFIX candidate: "uat ortamındaki hatalar" must
	// not resolve to a "uat-gateway" service. The bounded full-name
	// pass above still wins when the operator literally types the
	// service name ("uat-gateway hataları").
	envTok := map[string]bool{}
	for _, e := range envs {
		envTok[strings.ToLower(e)] = true
	}
	for _, t := range guidedTokens(msg) {
		if len(t) < 3 || guidedStopwords[t] || envTok[t] || !asciiNameToken(t) {
			continue
		}
		match, n := "", 0
		for _, svc := range services {
			ls := strings.ToLower(svc)
			if !strings.HasPrefix(ls, t) {
				continue
			}
			if len(ls) > len(t) && ls[len(t)] != '-' && ls[len(t)] != '_' && ls[len(t)] != '.' {
				continue // "check" must not claim "checkout-service"
			}
			n++
			match = svc
			if n > 1 {
				break
			}
		}
		if n == 1 {
			return match
		}
	}
	return ""
}

// extractEnvEntity matches the message against the LIVE environment
// list (ListEnvironments — never a guess), the env twin of
// extractServiceEntity's bounded full-name pass (v0.8.398). Bounded
// matching gives both asks for free: the bare name ("uat hataları")
// and the phrased forms ("uat ortamında/ortamı", "uat environment",
// "env uat") all contain the standalone env token, while an
// env-suffixed SERVICE name ("mobile-bff-uat") never leaks an env —
// '-' is a name char, so the inner "uat" fails the boundary check.
// Longest match wins ("preprod" beats "prod"; "prod" can't match
// inside "preprod" anyway). Unknown env words return "" — the bundle
// then runs env-less, deterministic beats clever. No prefix fallback:
// env names are short, exact vocabulary.
func extractEnvEntity(msg string, envs []string) string {
	best := ""
	for _, env := range envs {
		le := strings.ToLower(env)
		if len(le) < 2 || len(le) <= len(best) {
			continue
		}
		if indexBounded(msg, le) >= 0 {
			best = env
		}
	}
	return best
}

// ─── Takım varlığı (v0.9.1134) ──────────────────────────────────────

// extractTeamEntity — mesajdaki takım adını CANLI takım kataloğuna karşı
// çözer (asla tahmin değil; teamCatalogue = service_metadata'nın boş
// olmayan ownerTeam+sreTeam değerleri). extractEnvEntity'nin ikizi:
// sınırlı (bounded) tam-ad eşleşmesi, en UZUN kazanır ("SY-Dijital
// Bankacılık", "dijitalsy" alt-adını gölgeler).
//
// KATLAMA: iki taraf da chstore.NormTeamName'den geçer — takım adları
// Türkçe yazılıyor ve iki I tuzağı burada da geçerli ("Bankacılık" vs
// "BANKACILIK"). Katlama i/ı'yı tek forma indirdiği için hem katalog adı
// hem mesaj ASCII'ye iner; sınır denetimi (indexBounded) o yüzden çalışır.
//
// ÇIPLAK takım adı da eşleşir ("avengersy") — "hangi takım?" sorusuna
// verilen cevap turu tam olarak bu şekildedir ve akışın tek dayanağıdır.
//
// UZUNLUK TABANI 2 (v0.9.1246, operatör: gerçek takım adları "SY"/"UG"
// gibi 2 harfli KISA kodlar). v0.9.1134'te taban 3'tü ve gerekçesi "iki
// harflik bir ad rastgele metnin içinde eşleşir" idi — ölçüldüğünde bu
// gerekçe indexBounded'ı yok sayıyordu: eşleşme zaten SINIRLI, yani
// "sy" ancak kendi başına bir token olarak yakalanır ("sy takımının
// exception'ları" ✓, "kaysysteam" ✗). Taban 3 kalsaydı operatörün
// GERÇEK takımı hiçbir soruda çözülemezdi — sessiz ve kalıcı bir
// çıkmaz. Stopword kapısı yerinde duruyor ve adlar CANLI katalogdan
// geliyor, yani yüzey katalogla sınırlı.
//
// Tek karakter hâlâ dışarıda: tek harflik bir token noktalama/kısaltma
// gürültüsüyle karışır ve katalogda böyle bir ad görülmedi.
func extractTeamEntity(msg string, teams []string) string {
	folded := chstore.NormTeamName(msg)
	best := ""
	bestLen := 0
	for _, t := range teams {
		ft := chstore.NormTeamName(t)
		if utf8.RuneCountInString(ft) < 2 || guidedStopwords[ft] || len(ft) <= bestLen {
			continue
		}
		if indexBounded(folded, ft) >= 0 {
			best, bestLen = t, len(ft)
		}
	}
	if best != "" {
		return best
	}
	// v0.10.429 (D1) — takım KODU tireli bir ifadenin parçası olabilir
	// ("SY-XYZ takımı" ↔ katalog "SY", ya da katalog "SY-XYZ" ↔ mesaj
	// "sy xyz"): '-' ad karakteri olduğundan sınırlı arama kaçırıyordu.
	// Jeton eşi (tire/boşluk/noktalama ayırıcı), yalnız TEK takım eşleşirse.
	toks := nameTokens(folded)
	match, n := "", 0
	for _, t := range teams {
		ft := chstore.NormTeamName(t)
		if utf8.RuneCountInString(ft) < 2 || guidedStopwords[ft] {
			continue
		}
		fts := nameTokens(ft)
		// Yalnız ÇOK-JETONLU takım adı ("sy-xyz" ↔ "sy xyz"): tek jetonlu ad
		// tireli daha uzun bir adın içinde eşleşemez ("avengersy" ↛
		// "avengersy-legacy" — sınır kuralı, copilot_team_services_test).
		if len(fts) < 2 {
			continue
		}
		// takımın tüm jetonları mesajda ardışık geçmeli
		for i := 0; i+len(fts) <= len(toks); i++ {
			ok := true
			for j := range fts {
				if toks[i+j] != fts[j] {
					ok = false
					break
				}
			}
			if ok {
				n++
				match = t
				break
			}
		}
		if n > 1 {
			return ""
		}
	}
	if n == 1 {
		return match
	}
	return ""
}

// isBareTeamAsk — mesaj SADECE takım adından mı oluşuyor ("avengersy",
// "Avengersy?"). "hangi takım?" çipine tıklayan operatörün ürettiği tur
// budur; router'da takım dalının tek başına açılmasını haklı kılan sinyal.
// Ad çıkarıldıktan sonra kalanın harf/rakam taşımaması yeterli — noktalama
// ve boşluk serbest.
func isBareTeamAsk(msg, team string) bool {
	if team == "" {
		return false
	}
	folded := chstore.NormTeamName(msg)
	ft := chstore.NormTeamName(team)
	i := indexBounded(folded, ft)
	if i < 0 {
		return false
	}
	rest := folded[:i] + folded[i+len(ft):]
	for _, r := range rest {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// hasTeamWord — "takım/ekip/team" kökü. İyelik hâlleri de bu öneke girer
// ("takımımın") ama router'da hasTeamSelfSignal HER ZAMAN önce gelir, yani
// iyelik soruları buraya hiç düşmez.
func hasTeamWord(tokens []string) bool {
	return tokenHasPrefix(tokens, "takım", "takim", "ekip", "ekib", "team")
}

// hasServiceListWord — "servis/service" kökü; adı geçen takımın SERVİS
// listesini isteyen şekiller ("avengersy servisleri").
func hasServiceListWord(tokens []string) bool {
	return tokenHasPrefix(tokens, "servis", "service")
}

// mayNameTeam — copilotChatGuided'ın katalog okumasını ÇIPLAK takım adı
// için de göze alıp almayacağı ucuz kapı (saf).
//
// Neden gerekli: hasGuidedSignal'da takım adı diye bir sinyal YOKTUR ve
// olamaz (adlar canlı katalogdan gelir). "hangi takım?" çipine basan
// operatörün mesajı ("avengersy") hiçbir guided kelimesi taşımaz, yani
// hızlı-çıkış onu serbest tool döngüsüne atardı — çipin vaadi tam orada
// kırılırdı.
//
// Kapı kısa VE ad-şekilli mesajlarla sınırlı; ödediği bedel üç adet
// 60sn-cache'li katalog okumasıdır. Uzun cümleler zaten guided sinyaliyle
// gelir, taşımıyorsa serbest döngü doğru yerdir.
func mayNameTeam(norm string) bool {
	toks := guidedTokens(norm)
	// v0.10.429 (D1) — takım/servis kökü taşıyan biraz daha uzun cümleler
	// de kapıdan geçer ("SY-XYZ takımına ait servisleri listeler misin" 6
	// jeton / 45 rune); sinyalsiz kısa mesaj sınırı 40 rune + 5 jeton kalır.
	maxRunes, maxToks := 40, 5
	if hasTeamWord(toks) || hasServiceListWord(toks) {
		maxRunes, maxToks = 80, 8
	}
	if utf8.RuneCountInString(strings.TrimSpace(norm)) > maxRunes {
		return false
	}
	if len(toks) == 0 || len(toks) > maxToks {
		return false
	}
	for _, t := range toks {
		// v0.9.1246 — taban 3 → 2. "SY" / "UG" gibi 2 harfli takım
		// KODLARI gerçek (operatör), ve "hangi takım?" çipine basınca
		// mesaj tam olarak o çıplak koddan ibaret oluyor: taban 3'te
		// kapı kapalı kalır, çip serbest tool döngüsüne düşer ve
		// diyalog vaadini kırardı. Bedeli, sinyalsiz kısa mesajlarda
		// üç adet 60sn-cache'li katalog okuması.
		if utf8.RuneCountInString(t) >= 2 {
			return true
		}
	}
	return false
}

// teamCatalogue (v0.9.1134) — CANLI takım kataloğu: service_metadata'nın
// boş olmayan ownerTeam + sreTeam değerleri, SERVİS SAYISINA göre azalan
// (eşitlikte ada göre artan — deterministik sıra, map iterasyonu sızmaz).
//
// Sıra iki işi birden yapıyor: "hangi takım?" çipleri en büyük takımlarla
// başlar (operatörün takımı büyük olasılıkla ilk 8'de), extractTeamEntity
// ise sıradan bağımsız (en uzun ad kazanır).
//
// Tekilleştirme CanonTeam üzerinden — alias tablosu ve Türkçe katlama
// dahil, yani "avengerSY"/"Avengersy" TEK takımdır (v0.8.330'un
// /services tarafında yaptığı işin sunucu ikizi). Gösterilen yazımı
// betterTeamDisplay seçer (deterministik).
//
// v0.9.1244 — KATALOG mcptools.TeamCatalogue'a taşındı (list_teams tool'u
// aynı sayımı ve aynı sırayı vermek zorunda; mcptools api'yi import
// edemez, döngü). Buradaki tek çağıran guidedTeamNames artık
// mcptools.ReadTeamCatalogue'u okuyor — api'de ikinci bir uygulama YOK.
// Sıra/tekilleştirme/determinizm testleri de implementasyonun yanına
// taşındı: mcptools/team_ownership_test.go.

// extractServiceEntities (v0.9.422, CoSRE fikir #7) — mesajda sınırlı
// (bounded) tam-ad olarak geçen TÜM canlı servisler. extractServiceEntity
// en uzun TEK eşleşmeyi döner; kıyas soruları ("checkout-service ile
// payment-service p99 kıyasla") iki adı da ister. Ad-içi gölgeleme yok:
// "mobile-bff", "mobile-bff-uat" içinde boundary'ye takılır.
func extractServiceEntities(msg string, services []string) []string {
	var out []string
	for _, svc := range services {
		ls := strings.ToLower(svc)
		if len(ls) < 3 {
			continue
		}
		if indexBounded(msg, ls) >= 0 {
			out = append(out, svc)
		}
	}
	return out
}

// hasCompareSignal (v0.9.422) — açık karşılaştırma kalıpları.
func hasCompareSignal(toks []string) bool {
	for _, t := range toks {
		if t == "vs" || t == "versus" {
			return true
		}
	}
	return tokenHasPrefix(toks, "kıyas", "kiyas", "karşılaştır", "karsilastir", "compare", "fark")
}

// routeGuidedIntent is THE router: normalized keyword matching over
// the five shapes, most-specific first. Pure — table-tested in
// copilot_guided_test.go with Turkish + English variants.
// teams (v0.9.1134) — canlı takım kataloğu (teamCatalogue); nil = takım
// farkındalığı kapalı, o hâlde davranış bayt-bayt eskisidir.
func routeGuidedIntent(raw string, services, envs, teams []string, ctxService string) guidedRoute {
	msg := normalizeGuidedMsg(raw)
	toks := guidedTokens(msg)
	// v0.9.537 — açık 32-hex trace ID'si EN GÜÇLÜ sinyaldir ve her şeyi
	// ezer: operatör somut bir öznenin adresini vermiş. Eskiden hiçbir
	// intent tanımıyordu, soru RAG doküman yoluna düşüp "yüklü
	// dokümanlarda bu bilgi yok" cevabı alıyordu (operator-reported).
	if id := extractTraceID(msg); id != "" {
		return guidedRoute{Intent: guidedTraceByID, TraceID: id}
	}
	// v0.10.13 — asistanın KENDİSİ hakkındaki soru. Somut bir ID'den
	// SONRA bakılıyor: operatör bir trace yapıştırdıysa niyeti odur,
	// cümlede "sen" geçse bile. Ama telemetri sınıflandırıcılarından
	// ÖNCE: "hangi modelsin" hiçbir servisi adlandırmıyor ve aşağıdaki
	// hiçbir kapıya uymadığı için RAG'a savruluyordu.
	if isSelfMetaQuestion(toks) {
		return guidedRoute{Intent: guidedSelfMeta}
	}
	// v0.9.1142 — YAPILANDIRILMIŞ kurumsal request kimliği. SIRA: açık bir
	// trace ID'si (yukarıda) hâlâ en doğrudan çapa — o varsa log araması
	// yapmadan trace'e gidilir. Bu kontrol 16-hex span'den ÖNCE, çünkü
	// kimlik daha spesifik bir sinyal: uzunluk + sabit ofsetlerde geçerli
	// bir takvim tarihi taşıyor, yani şeklin kendisi doğrulanıyor.
	//
	// `raw` üzerinden (normalize edilmiş msg DEĞİL): harf kasası aramada
	// kullanılacak, ES keyword alanlarında eşleşme harfe duyarlı olabilir.
	// Hex çakışması yok — kimlik en az 47 karakter, \b sınırlı 32/16-hex
	// örüntüleri o uzunlukta bir alnum bloğunun içinde eşleşemez.
	if tok, ok := reqid.FindToken(raw); ok {
		return guidedRoute{Intent: guidedRequestID, RequestID: tok}
	}
	// v0.9.548 — 16-hex SPAN id'si. SIRA önemli: 32-hex önce denendi,
	// yani bir trace ID'nin içindeki 16'lık dizi buraya düşemez.
	if id := extractSpanID(msg); id != "" {
		return guidedRoute{Intent: guidedSpanByID, SpanID: id}
	}
	// v0.9.1144 — kimliğe BENZEYEN ama şablona uymayan token. Operatör
	// prod raporu: AltKod'u alfanümerik gerçek kimlik ilk şablonda parse
	// olmayınca mesaj RAG doküman katmanına düşüp "yüklü dokümanlarda bu
	// bilgi yok" diyordu (aynı hastalığın v0.9.537'deki trace-ID hâli).
	// Şablon yine sürpriz yaparsa davranış dürüst kalsın: bundle "biçim
	// çözülemedi" der, köprü linki yine çıkar — doküman QA'sına düşmez.
	// SIRA: çözümlenebilir sinyallerin (trace/kimlik/span) HEPSİNDEN
	// sonra — mesajta hem span id hem bozuk blob varsa span kazanır.
	if tok, ok := reqid.FindLooseToken(raw); ok {
		return guidedRoute{Intent: guidedRequestID, RequestID: tok}
	}
	svc := extractServiceEntity(msg, services, envs)
	env := extractEnvEntity(msg, envs)
	team := extractTeamEntity(msg, teams)
	// v0.10.439 (D4) — fan-out: A→B→C; periyot ve çift dallarından ÖNCE
	// (cümle istek/giden sözcüğü de taşır).
	if hasFanoutSignal(toks) && hasPairRequestSignal(toks) {
		if aFrag, bFrag, cFrag, ok := splitFanoutFragments(raw); ok {
			aOpts, bOpts, cOpts := resolvePairSide(aFrag, services, envs), resolvePairSide(bFrag, services, envs), resolvePairSide(cFrag, services, envs)
			switch {
			case len(aOpts) == 1 && len(bOpts) == 1 && len(cOpts) == 1:
				return guidedRoute{Intent: guidedFanout, Service: aOpts[0], Env: env, PairFrom: aOpts[0], PairTo: bOpts[0], PairToKind: "service", FanoutTo: cOpts[0], FanoutToKind: "service"}
			case len(aOpts) == 1 && len(bOpts) == 1 && len(cOpts) == 0 && cFrag != "":
				return guidedRoute{Intent: guidedFanout, Service: aOpts[0], Env: env, PairFrom: aOpts[0], PairTo: bOpts[0], PairToKind: "service", FanoutTo: cFrag, FanoutToKind: "node"}
			case len(aOpts) > 1:
				return guidedRoute{Intent: guidedAskService, AskIntent: guidedFanout, ServiceOptions: aOpts, PairTo: bFrag, FanoutTo: cFrag, PairMissing: "from", Env: env}
			}
		}
	}
	// v0.10.438 (D3) — periyot sorusu: çift varsa A→B, yoksa tek servis;
	// hiçbiri yoksa sor. D2 çift dalından ÖNCE (aynı cümle istek sözcüğü
	// de taşır).
	// v0.10.443 — kapı: hata/log/deploy/problem şekilleri kendi yollarında
	// kalır ("cron loglarında hata var mı" log_errors, "düzenli olarak hata
	// alıyoruz" sağlık, "her gün 09:00'da deploy" deploy); özne yoksa yalnız
	// istek/çağrı sözcüğü varken sorulur, aksi hâlde düşer (eskiden koşulsuz
	// dönüp her şeyi yutuyordu).
	if hasPeriodSignal(toks) && !hasErrorSignal(toks) && !hasLogSignal(toks) && !hasDeploySignal(toks) && !hasProblemSignal(toks) {
		if fromFrag, toFrag, ok := splitPairFragments(raw); ok {
			fromOpts, toOpts := resolvePairSide(fromFrag, services, envs), resolvePairSide(toFrag, services, envs)
			switch {
			case len(fromOpts) == 1 && len(toOpts) == 1:
				return guidedRoute{Intent: guidedCallPeriod, Service: fromOpts[0], Env: env, PairFrom: fromOpts[0], PairTo: toOpts[0], PairToKind: "service"}
			case len(fromOpts) == 1 && len(toOpts) == 0 && toFrag != "":
				return guidedRoute{Intent: guidedCallPeriod, Service: fromOpts[0], Env: env, PairFrom: fromOpts[0], PairTo: toFrag, PairToKind: "node"}
			case len(fromOpts) > 1:
				return guidedRoute{Intent: guidedAskService, AskIntent: guidedCallPeriod, ServiceOptions: fromOpts, Env: env}
			}
		}
		if svc != "" {
			return guidedRoute{Intent: guidedCallPeriod, Service: svc, Env: env}
		}
		if opts := serviceCandidates(msg, services, envs, guidedServiceAskMax); len(opts) == 1 {
			return guidedRoute{Intent: guidedCallPeriod, Service: opts[0], Env: env}
		} else if len(opts) > 1 {
			return guidedRoute{Intent: guidedAskService, AskIntent: guidedCallPeriod, ServiceOptions: opts, Env: env}
		}
		if ctxService != "" {
			return guidedRoute{Intent: guidedCallPeriod, Service: ctxService, Env: env}
		}
		if hasPairRequestSignal(toks) {
			return guidedRoute{Intent: guidedAskService, AskIntent: guidedCallPeriod, Env: env}
		}
	}
	// v0.10.436 (D2a) — "A'dan B'ye giden istekler": çift, aile/kıyas
	// dallarından ÖNCE (iki ad + istek sözcüğü aileye düşmesin). Kaynak
	// servis şart; hedef servis ya da dış düğüm parçası.
	if hasPairRequestSignal(toks) {
		if fromFrag, toFrag, ok := splitPairFragments(raw); ok {
			fromOpts := resolvePairSide(fromFrag, services, envs)
			toOpts := resolvePairSide(toFrag, services, envs)
			switch {
			case len(fromOpts) == 1 && len(toOpts) == 1:
				return guidedRoute{Intent: guidedPairRequests, Service: fromOpts[0], Env: env, PairFrom: fromOpts[0], PairTo: toOpts[0], PairToKind: "service"}
			case len(fromOpts) == 1 && len(toOpts) == 0 && toFrag != "":
				return guidedRoute{Intent: guidedPairRequests, Service: fromOpts[0], Env: env, PairFrom: fromOpts[0], PairTo: toFrag, PairToKind: "node"}
			case len(fromOpts) > 1:
				other := toFrag
				if len(toOpts) == 1 {
					other = toOpts[0]
				}
				return guidedRoute{Intent: guidedAskService, AskIntent: guidedPairRequests, ServiceOptions: fromOpts, PairTo: other, PairMissing: "from", Env: env}
			case len(fromOpts) == 1 && len(toOpts) > 1:
				return guidedRoute{Intent: guidedAskService, AskIntent: guidedPairRequests, ServiceOptions: toOpts, PairFrom: fromOpts[0], PairMissing: "to", Env: env}
			}
		}
	}
	// v0.10.436 (D2b) — "X servisinde içinde <parça> geçen trace'ler".
	if frag, isSQL, ok := extractTraceSearch(raw, toks); ok {
		tsvc := svc
		if tsvc == "" {
			cleaned := strings.NewReplacer(strings.ToLower(frag), " ").Replace(msg)
			switch opts := serviceCandidates(cleaned, services, envs, guidedServiceAskMax); {
			case len(opts) == 1:
				tsvc = opts[0]
			case len(opts) > 1:
				return guidedRoute{Intent: guidedAskService, AskIntent: guidedTraceSearch, ServiceOptions: opts, SearchText: frag, SearchSQL: isSQL, Env: env}
			}
		}
		return guidedRoute{Intent: guidedTraceSearch, Service: tsvc, Env: env, SearchText: frag, SearchSQL: isSQL}
	}
	// v0.9.422 (CoSRE fikir #7) — çoklu tam-ad kıyası: soru 2+ canlı
	// servisi ADIYLA anıyor ve sağlık/hata/kıyas şekliyse familyHealth
	// yan-yana RED karşılaştırması zaten işi yapar. Tek-ad çözümü
	// (en uzun) bu durumda yanlış daraltmaydı.
	if multi := extractServiceEntities(msg, services); len(multi) >= 2 &&
		(hasHealthSignal(toks) || hasErrorSignal(toks) || hasCompareSignal(toks)) {
		return guidedRoute{Intent: guidedFamilyHealth, Env: env, Family: multi}
	}
	// Family resolution (v0.9.192 — operator-reported: "mobile
	// bff'lerde hangisinde hata var" servis bulamıyordu): tek servis
	// çözülmediyse ve soru sağlık/hata/problem şekilliyse, mesajın
	// ad-parçaları (mobile + bff) 2+ canlı servisi aile olarak
	// yakalayabilir. Açık tek-servis eşleşmesi HER ZAMAN aileden önce
	// gelir (svc != "" iken hiç denenmez).
	var family []string
	if svc == "" && (hasHealthSignal(toks) || hasErrorSignal(toks) || hasProblemSignal(toks)) {
		family = extractServiceFamily(msg, services, envs)
	}
	// Context-awareness (v0.9.164): mesaj bir servis ADI taşımıyorsa ve
	// frontend geçerli (katalogda olan) bir sayfa-servisi geçirmişse onu
	// varsayılan al — "neden yavaş?" checkout sayfasında → checkout. Şeffaf:
	// banner scope'u söyler. Mesajda açık servis varsa ELLEMEZ (kullanıcı
	// başka servisi kastediyorsa context ezmez). Aile yakalandıysa da
	// ELLEMEZ — "mobile bff'ler" sorusu checkout sayfasında checkout'a
	// daralmamalı.
	if svc == "" && len(family) == 0 && ctxService != "" {
		for _, s := range services {
			if s == ctxService {
				svc = ctxService
				break
			}
		}
	}
	// v0.10.429 (D1) — servis-şekilli soru, çözülmemiş ad: mesajın
	// jetonları canlı adların parçalarına oturuyorsa TEK aday doğrudan
	// yönlenir ("mobile commercial bff" → mobile-commercial-bff-prod),
	// 2+ aday "hangisini kastettin?" olur (sessiz none yerine). Takım
	// çözüldüyse takım dalı önce (aşağıda) — buraya girmez. Aile (2+
	// parça-eşi) yakalandıysa ve soru sağlık/hata şekliyse aile rotası
	// (v0.9.192, yan yana RED) korunur; ama neden/yavaş/deploy/log gibi
	// TEK servis isteyen şekillerde aile eskiden sessizce düşüp soru filo
	// geneline çöküyordu ("login neden yavaş" → servissiz slow_traces) —
	// şimdi aile üyeleri aday olarak sorulur.
	//
	// v0.10.433 (D5) — alan süzgeçli log araması ÖNCE çıkarılır: "message
	// alanında 'x' geçen loglar" cümlesindeki "message" jetonu aday üreticide
	// bir servisin parçasına oturup soruyu sessizce o servise daraltabilirdi;
	// alan sorgusu varken aday aranmaz (servis yalnız açık eşleşmeyle).
	lfField, lfValue, lfContains, lfOK := extractLogFieldQuery(raw, toks)
	if lfOK {
		// Alan adı ve tırnaklı değer servis eşleştiricisinden GİZLENİR:
		// "message alanında 'timeout' geçen loglar" cümlesindeki "message"
		// önek eşleşmesiyle message-broker'a çözülüyordu.
		cleaned := strings.NewReplacer(strings.ToLower(lfField), " ", strings.ToLower(lfValue), " ").Replace(msg)
		svc = extractServiceEntity(cleaned, services, envs)
	}
	if svc == "" && team == "" && !lfOK {
		if ask := serviceIntentFor(msg, toks); ask != guidedNone && (len(family) == 0 || ask != guidedServiceHealth) {
			opts := family
			if len(opts) == 0 {
				opts = serviceCandidates(msg, services, envs, guidedServiceAskMax)
			}
			switch {
			case len(opts) == 1:
				svc = opts[0]
			case len(opts) > 1:
				if len(opts) > guidedServiceAskMax {
					opts = opts[:guidedServiceAskMax]
				}
				return guidedRoute{Intent: guidedAskService, AskIntent: ask, ServiceOptions: opts, Env: env}
			case ask == guidedRootCause && (hasHealthSignal(toks) || hasSlowTraceSignal(msg)) &&
				!hasErrorSignal(toks) && !hasProblemSignal(toks) && !hasMessagingSignal(toks) && !hasDBSignal(toks) && !hasPodSignal(toks):
				// v0.10.434 (D7a) — "yavaşlığın sebebi ne?" / "neden yavaş?":
				// özne yok, aday yok, sayfa bağlamı yok. Eskiden filo geneli
				// en-yavaş listesine çöküyordu (nedensellik sessizce düşüyordu);
				// şimdi sorar, adayları bundle açık problemlerden doldurur.
				// "neden hata alıyoruz" filo-geneli problems yolunda kalır.
				return guidedRoute{Intent: guidedAskService, AskIntent: guidedRootCause, Env: env}
			}
		}
	}
	switch {
	// v0.9.375 — iyelik takım sinyali HER ŞEYDEN önce: "takımımın açık
	// problemleri" generic problem yolundan önce yakalanmalı, yoksa tüm
	// filonun problemleri döner ve "takımımın" kelimesi sessizce düşer.
	case hasTeamSelfSignal(toks):
		// v0.9.650 — exception kelimesi problem/hata sinyalinden ÖNCE:
		// "takımımın exception'ları" ikisini birden taşıyor ve problems
		// dalı kazanırsa soru sessizce "açık problemler"e çöker.
		if hasExceptionWord(toks) {
			return guidedRoute{Intent: guidedMyExceptions, Env: env}
		}
		if hasProblemSignal(toks) || hasErrorSignal(toks) {
			return guidedRoute{Intent: guidedMyProblems, Env: env}
		}
		return guidedRoute{Intent: guidedMyServices, Env: env}
	// v0.9.1134 — ADI GEÇEN takım, iyelik dallarından HEMEN SONRA ve
	// servis-adı dallarından ÖNCE.
	//
	// Neden buraya: (1) iyelik ("takımımın servisleri") kimlikten çözülür
	// ve mesajda ad taşımaz — o dal önce kalmalı, yoksa "benim takımım
	// avengersy" gibi bir cümlede ad, kimliği ezerdi. (2) Servis
	// dallarından önce, çünkü bir takım SERVİSLE AYNI ADI taşıyabilir
	// ("payments" hem takım hem servis) ve o durumda çıplak ad
	// gölgelenirse "hangi takım?" diyaloğu ÇALIŞMAZ: operatörün tıkladığı
	// çip sessizce servis sağlığına giderdi. Aynı gerekçe hasTeamSelfSignal
	// için v0.9.375'te de verilmişti (kapsam kelimesi sessizce düşmesin).
	//
	// Gölgeleme bedeli bilerek KAPI ile sınırlandı — takım dalı yalnız üç
	// hâlde açılır:
	//   a) mesaj SADECE takım adı (çip turu),
	//   b) takım/ekip kelimesi de var ("payments takımının servisleri"),
	//   c) servis çözülmedi VE liste/sağlık/hata şekli var
	//      ("avengersy servisleri nasıl").
	// (c)'deki `svc == ""` şartı sayesinde "payments neden yavaş" gibi
	// SERVİS soruları takıma kaçmaz; ada ek bir sinyal gerekmesi de
	// "payments hataları" sorusunu servis tarafında bırakır.
	case team != "" && (isBareTeamAsk(msg, team) || hasTeamWord(toks) ||
		(svc == "" && (hasServiceListWord(toks) || hasHealthSignal(toks) || hasErrorSignal(toks)))):
		return guidedRoute{Intent: guidedTeamServices, Team: team, Env: env}
	// v0.10.433 (D5) — alan süzgeçli log araması, "yavaş"/"hata" gibi
	// içerik sözcükleri taşıyabilen tırnaklı değerlerden ÖNCE: "message
	// alanında 'slow' geçen loglar" slow_traces'a kaçmasın.
	// v0.10.434 (D7b) — "sayfasını aç": sayfa türü + özne; overview özne
	// ister (yoksa copilotChatGuided önceki turdan devralır ya da sorar),
	// problems/logs/traces/endpoints filo geneli de açılabilir.
	case hasOpenPageSignal(toks):
		return guidedRoute{Intent: guidedOpenPage, Service: svc, Env: env, Page: openPageKind(toks)}
	case lfOK:
		return guidedRoute{Intent: guidedLogField, Service: svc, Env: env, LogField: lfField, LogValue: lfValue, LogContains: lfContains}
	// v0.9.514 — kök-neden, spesifik sinyallerden ÖNCE. "neden checkout
	// yavaşladı" hem why hem slow sinyali taşır; slow yolu kazanırsa soru
	// "yavaş mı"ya çöker ve NEDENSELLİK sessizce düşer. Servis şart:
	// hipotez bir anchor'a bağlıdır, servissiz "neden hata alıyoruz"
	// aşağıdaki problems yoluna düşsün (filo geneli zaten o).
	case svc != "" && hasWhySignal(toks):
		return guidedRoute{Intent: guidedRootCause, Service: svc, Env: env}
	case hasSlowTraceSignal(msg):
		return guidedRoute{Intent: guidedSlowTraces, Service: svc, Env: env}
	case hasDeploySignal(toks):
		return guidedRoute{Intent: guidedDeployImpact, Service: svc, Env: env}
	case hasLogSignal(toks) && hasErrorSignal(toks):
		return guidedRoute{Intent: guidedLogErrors, Service: svc, Env: env}
	case hasPodSignal(toks):
		// v0.9.376 — servisli soru o servisin pod'ları, servissiz soru
		// filo-geneli JVM heap sıralaması (ikisi de bundle'da).
		return guidedRoute{Intent: guidedPodHealth, Service: svc, Env: env}
	// v0.9.420 — bağımlılık sağlığı. Messaging DB'den önce ("kafka db'ye
	// yazamıyor" → messaging tarafı daha spesifik ipucu). Servis çözülse
	// bile bağımlılık sinyali kazanır ("payments db hataları" → DB
	// kırılımı; bundle servis notunu düşer).
	case hasMessagingSignal(toks):
		return guidedRoute{Intent: guidedMessagingHealth, Service: svc, Env: env}
	case hasDBSignal(toks):
		return guidedRoute{Intent: guidedDBHealth, Service: svc, Env: env}
	// v0.9.416 — vardiya özeti: spesifik sinyallerden (slow/deploy/log/
	// pod) SONRA, problems'tan ÖNCE — "dün gece problem var mıydı" özet
	// cevabı hak eder (problemler zaten bundle'ın ilk bloğu).
	case hasShiftSignal(msg, toks):
		return guidedRoute{Intent: guidedShiftSummary, Service: svc, Env: env}
	case len(family) >= 2:
		return guidedRoute{Intent: guidedFamilyHealth, Env: env, Family: family}
	case hasProblemSignal(toks):
		return guidedRoute{Intent: guidedProblems, Service: svc, Env: env}
	case svc != "" && (hasHealthSignal(toks) || hasErrorSignal(toks)):
		return guidedRoute{Intent: guidedServiceHealth, Service: svc, Env: env}
	case hasErrorSignal(toks):
		return guidedRoute{Intent: guidedProblems, Env: env}
	}
	// v0.10.463 (D1) — hiçbir niyet tanımadı: ad-şekilli mesaj varlık kademesine
	// (find_entity.go). Yalnız burada, tüm sinyal dallarından SONRA.
	if r, ok := routeFindEntity(msg, toks, svc, env, services, envs); ok {
		return r
	}
	return guidedRoute{}
}

// extractServiceFamily resolves a family-of-services ask against the
// LIVE service list (v0.9.192). Fragments = message tokens that occur
// inside ≥1 service name as a BOUNDED segment ("mobile", "bff" inside
// "mobile-overview-bff-prod"; separators -_.). The family = services
// containing ALL fragments. <2 matches → nil (single-service paths
// already handle 1; zero fragments = not a name-shaped ask). >40 →
// nil: a lone generic fragment ("prod") must not claim the fleet.
func extractServiceFamily(msg string, services, envs []string) []string {
	envTok := map[string]bool{}
	for _, e := range envs {
		envTok[strings.ToLower(e)] = true
	}
	var frags []string
	for _, t := range guidedTokens(msg) {
		if len(t) < 3 || guidedStopwords[t] || envTok[t] || !asciiNameToken(t) {
			continue
		}
		for _, svc := range services {
			if segmentInName(strings.ToLower(svc), t) {
				frags = append(frags, t)
				break
			}
		}
	}
	if len(frags) == 0 {
		return nil
	}
	var fam []string
	for _, svc := range services {
		ls := strings.ToLower(svc)
		all := true
		for _, f := range frags {
			if !segmentInName(ls, f) {
				all = false
				break
			}
		}
		if all {
			fam = append(fam, svc)
		}
	}
	if len(fam) < 2 || len(fam) > 40 {
		return nil
	}
	return fam
}

// segmentInName reports whether t occurs in name as a whole segment —
// bounded by -_. separators or the name's ends ("bff" matches
// "mobile-bff-prod", never "rebuff-svc").
func segmentInName(name, t string) bool {
	for start := 0; ; {
		i := strings.Index(name[start:], t)
		if i < 0 {
			return false
		}
		i += start
		leftOK := i == 0 || name[i-1] == '-' || name[i-1] == '_' || name[i-1] == '.'
		r := i + len(t)
		rightOK := r == len(name) || name[r] == '-' || name[r] == '_' || name[r] == '.'
		if leftOK && rightOK {
			return true
		}
		start = i + 1
	}
}

// guidedRangeRe extracts "son 2 saat" / "last 30 minutes" style
// windows. Longer unit spellings come first in the alternation so
// "minutes" isn't half-eaten by "min".
var guidedRangeRe = regexp.MustCompile(`(\d+)\s*(gün|gun|days|day|saat|hours|hour|hrs|hr|dakika|dk|minutes|minute|mins|min)`)

// guidedRangeS derives the lookback window (seconds) from the
// question. Default 1800 (30m, the chat tools' default); bare unit
// words ("son bir saat", "today") map to 1h/1d. Clamped to
// [300, 86400] so a typo can't trigger a week-wide scan.
// guidedRangeRungs — ekrandan gelen aralığın oturtulacağı basamaklar.
// frontend'in PRESET_SECONDS kümesiyle aynı (1m … 30d).
var guidedRangeRungs = []int64{
	60, 300, 900, 1800, 3600, 10800, 21600, 43200,
	86400, 172800, 259200, 604800, 1209600, 2592000,
}

// snapRangeS — EKRANDAN gelen aralığı sabit basamaklara oturtur.
//
// Neden gerekli (v0.9.529): ekrandaki aralık MUTLAK (custom from/to)
// olabilir ve o hâlde keyfi bir saniye sayısı üretir — "6 saat 3 dakika
// 17 saniye". Bu değer guided prefetch'lerin sunucu cache anahtarlarına
// giriyor; sınırsız kardinalite, her sorunun kendi cache satırını
// yazması demek. Aynı sınıf v0.8.270'te ES tarafında yakalanmıştı.
//
// YUKARI oturtulur: pencere operatörün gördüğünü KAPSAMALI. Aşağı
// oturtmak "6 saate bakıyorum ama cevap 3 saatlik" demek olurdu ve
// eksik rapor, biraz fazla okumadan kötüdür.
func snapRangeS(v int64) int64 {
	if v <= 0 {
		return 0
	}
	for _, r := range guidedRangeRungs {
		if v <= r {
			return r
		}
	}
	return guidedRangeRungs[len(guidedRangeRungs)-1] // 30 günde tavan
}

func guidedRangeS(raw string) int64 {
	v, _ := guidedRangeSExplicit(normalizeGuidedMsg(raw))
	return v
}

// guidedRangeSExplicit (v0.9.410) — guidedRangeS'in çekirdeği; ikinci
// dönüş, mesajın AÇIK bir pencere içerip içermediğini söyler (takip
// devralması "peki payments?" için önceki sorunun penceresini ancak
// mevcut soru açık pencere TAŞIMIYORSA korumalı). msg normalize
// edilmiş olmalı.
func guidedRangeSExplicit(msg string) (int64, bool) {
	explicit := true
	rangeS := int64(1800)
	if m := guidedRangeRe.FindStringSubmatch(msg); m != nil {
		n := int64(0)
		fmt.Sscanf(m[1], "%d", &n)
		switch unit := m[2]; {
		// "dk"/"dakika" also start with 'd' — day units must be
		// matched by full stem, never a bare 'd' prefix (this exact
		// branch is pinned by TestGuidedRangeS, the unit-mixing rule).
		case strings.HasPrefix(unit, "gün") || strings.HasPrefix(unit, "gun") || strings.HasPrefix(unit, "day"):
			rangeS = n * 86400
		case strings.HasPrefix(unit, "saat") || strings.HasPrefix(unit, "hour") || strings.HasPrefix(unit, "hr"):
			rangeS = n * 3600
		default: // dakika | dk | minute | min
			rangeS = n * 60
		}
	} else if strings.Contains(msg, "saat") || strings.Contains(msg, "hour") {
		rangeS = 3600
	} else if strings.Contains(msg, "gün") || strings.Contains(msg, "day") ||
		strings.Contains(msg, "bugün") || strings.Contains(msg, "today") {
		rangeS = 86400
	} else {
		explicit = false
	}
	if rangeS < 300 {
		rangeS = 300
	}
	if rangeS > 86400 {
		rangeS = 86400
	}
	return rangeS, explicit
}

// fmtAgoTR renders "how long ago" in compact Turkish units. EVERY
// unit branch is exercised by TestFmtAgoTR (the Nh/Nd unit-mixing
// rule). Negative deltas (clock skew) clamp to 0.
func fmtAgoTR(seconds int64) string {
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

// ─── Entry point (called from copilotChat before the tool loop) ─────

// copilotChatGuided tries the guided path for the last user message.
// Returns handled=false when the router doesn't match or a primary
// prefetch fails — the caller then runs the free tool loop unchanged.
// handled=true means the exchange is complete (answer or error
// emitted); ok mirrors the `done` event's success flag.
// explain (v0.9.479) — AI çekmecesinden gelen "ekrandaki açıklama"
// bağlamı; boşken bu fonksiyonun davranışı bayt-bayt eskisidir.
// ctxRangeS (v0.9.529) — operatörün EKRANDAKİ zaman aralığı, saniye.
// 0 = bilgi yok (eski istemci); o hâlde davranış bayt-bayt eskisidir.
// ctxTrace (v0.9.537) — ekrandaki trace ID'si (/trace?id=), "" = yok.
func (s *Server) copilotChatGuided(ctx context.Context, emit func(string, any), msgs []copilot.ChatMessage, ctxService, ctxOperation, explain string, ctxRangeS int64, ctxTrace, ctxEnv string, anchorTo time.Time, tzOffsetMin int, tzName string) (handled, ok bool) {
	question := strings.TrimSpace(lastUserText(msgs))
	if question == "" {
		return false, false
	}
	norm := normalizeGuidedMsg(question)
	// v0.9.410 — takip soruları ("peki payments?", "son 24 saatte?")
	// guided sinyal taşımasa da katalog okumasını hak eder; devralma
	// applyFollowUpContext'te (copilot_followup.go).
	prior := priorUserTexts(msgs)
	followCue := len(prior) > 0 && isFollowUpCue(norm)
	// v0.9.1134 — ÇIPLAK takım adı hiçbir guided kelime taşımaz ("hangi
	// takım?" çipinin gönderdiği tur), o yüzden kısa + ad-şekilli mesajlar
	// da katalog okumasını hak eder (mayNameTeam; üç okuma da 60sn cache'li).
	// v0.10.434 (D7c) — sözlük, sinyal kapısından ÖNCE: "p95 ne demek" sağlık
	// sinyali taşır (ask_service'e savrulurdu), "requestid nedir" hiçbir
	// sinyal taşımaz (RAG'a düşerdi). LLM yok, exchangeId yok (glossary.go).
	if _, entry, ok := glossaryLookup(norm); ok {
		emit("answer", glossaryAnswer(entry))
		return true, true
	}
	// v0.10.437 (D6) — mutlak tarih/saat şekli de katalog okumasını hak
	// eder ("08/08/2026 04-08 ile 08-09 arası servis süreleri" başka
	// hiçbir sinyal taşımıyor).
	absShape := looksLikeAbsoluteWindow(question)
	// v0.10.463 (D1) — bulma fiili taşıyan uzunca cümle de katalog okumasını
	// hak eder ("mobile bff'yi bulabilir misin lütfen" 6 jeton, mayNameTeam 5'te
	// keser).
	findCue := hasFindSignal(guidedTokens(norm))
	if !hasGuidedSignal(norm) && !followCue && !mayNameTeam(norm) && !absShape && !findCue {
		return false, false // zero-cost fast path: no catalogue read
	}
	svcNames, envNames := s.guidedServiceNames(ctx), s.guidedEnvNames(ctx)
	teamNames := s.guidedTeamNames(ctx)
	route := routeGuidedIntent(question, svcNames, envNames, teamNames, ctxService)
	// v0.9.537 — "bu trace" / "ekrandaki trace" İD'siz sorulduğunda
	// ekrandaki trace'e otur: operatör /trace sayfasında, adres zaten
	// özneyi taşıyor. Mesajdaki AÇIK 32-hex her zaman kazanır
	// (routeGuidedIntent onu en üstte yakalar); burası yalnız hiçbir
	// rota çıkmadığında ve mesaj "trace" kökü taşıdığında devreye girer.
	// v0.9.570 — İŞARET ZAMİRİ KAPISI: operatör "BU trace" derken
	// ekrandaki şeyi soruyor ve bu, başka her niyeti geçersiz kılar.
	//
	// Öncesinde "bu trace neden yavaş?" filo geneli en yavaş 10 trace
	// listesi döndürüyordu: hasSlowTraceSignal sınırsız Contains ile
	// "ned|EN YAVAŞ" yakalıyor, slow_traces dalı ctxTrace
	// devralmasından ÖNCE geliyordu. Sınır eşleşmesi (v0.9.570) o
	// kazayı bitirdi ama "neden yavaş" artık AÇIK kalıp — yani bu kapı
	// olmadan aynı soru yine listeye giderdi.
	//
	// Ayrım şu: "en yavaş trace'ler" bir LİSTE sorusu, "bu trace neden
	// yavaş?" EKRANDAKİ şeyi soruyor. İşaret zamiri o ayrımı taşıyan
	// tek sinyal.
	if ctxTrace != "" && hasDemonstrativeTrace(norm) {
		route = guidedRoute{Intent: guidedTraceByID, TraceID: ctxTrace}
		emitGuidedContextStep(emit, "bağlam: ekrandaki trace ("+ctxTrace+")")
	} else if route.Intent == guidedNone && ctxTrace != "" && tokenHasPrefix(guidedTokens(norm), "trace") {
		route = guidedRoute{Intent: guidedTraceByID, TraceID: ctxTrace}
		emitGuidedContextStep(emit, "bağlam: ekrandaki trace ("+ctxTrace+")")
	}
	// v0.9.529 — soru AÇIK bir pencere taşımıyorsa ekrandaki aralığı
	// kullan. Sabit 30dk, operatör 6 saatlik pencereye bakarken "hata
	// oranı ne" diye sorduğunda BAŞKA bir pencerenin cevabını veriyordu
	// ve fark görünmüyordu. Öncelik sırası bilinçli: sorunun açık
	// penceresi > ekran > 30dk varsayılanı. Soru her zaman ekrandan
	// güçlü — "son 24 saatte" yazan operatör ekranını değiştirmek
	// zorunda kalmamalı.
	rangeS, explicitRange := guidedRangeSExplicit(norm)
	if !explicitRange && ctxRangeS > 0 {
		rangeS = snapRangeS(ctxRangeS)
	}
	// v0.9.1259 — env devri (ctxRangeS deseninin aynası, v0.9.529
	// gerekçesi): soru açık env taşımıyorsa ekrandaki seçim kullanılır.
	// Öncelik bilinçli: sorunun açık env'i > ekran > (boş = tümü).
	// Çip şeffaflığı: bundle'lar env'i zaten arg yankısında gösteriyor
	// (withEnvArg) — devralınan env de oradan görünür.
	if route.Env == "" && ctxEnv != "" {
		route.Env = ctxEnv
	}
	followBase := "" // devralınan temel mesaj (operasyon çözümü için)
	if followCue {
		if nr, nrange, base, changed := applyFollowUpContext(route, question, prior, svcNames, envNames, teamNames); changed {
			route, rangeS, followBase = nr, nrange, base
			scope := string(route.Intent)
			if route.Service != "" {
				scope = route.Service
			}
			emitGuidedContextStep(emit, "bağlam: "+scope+" (önceki sorudan)")
		}
	}
	// v0.10.437 (D6) — mutlak pencere(ler): tek pencere çıpayı ve uzunluğu
	// ezer (hangi rota olursa olsun), iki pencere window_compare olur.
	// Rota none olsa da geçerli ("… arası servis süreleri" sinyalsiz).
	if absShape {
		loc := chatLocationNamed(tzName, tzOffsetMin)
		if wins, ok := extractAbsoluteWindows(question, time.Now(), loc); ok {
			var label string
			route, anchorTo, rangeS, label = applyAbsoluteWindows(route, wins, ctxService, anchorTo, rangeS, loc, absWindowText(question))
			if label != "" {
				emitGuidedContextStep(emit, label)
			}
			if len(wins) == 1 && route.Intent == guidedNone && route.Service == "" && ctxService != "" {
				route = guidedRoute{Intent: guidedServiceHealth, Service: ctxService, Env: route.Env}
			}
		}
	}
	// v0.10.434 (D7b) — "sayfasını aç" öznesiz: takip ipucu olmasa da
	// önceki turun servisi devralınır; o da yoksa runGuidedRoute sorar.
	if route.Intent == guidedOpenPage && route.Service == "" && route.Page == "overview" {
		if p := newestPriorService(prior, svcNames, envNames); p != "" {
			route.Service = p
			emitGuidedContextStep(emit, "bağlam: "+p+" (önceki sorudan)")
		}
	}
	if route.Intent == guidedNone {
		return false, false
	}
	// v0.9.479 — çekmece bağlamı varken SOMUT ÖZNEYE oturmayan rota
	// guided'a bırakılmaz: filo geneli prefetch ekrandaki konuyu
	// ıskalıyordu (operatör raporu — ekranda exception dururken filo
	// JVM GC anlatıldı). Explain-grounded yol devralır
	// (copilot_drawer.go). explain boşken bu daima false.
	if drawerSuppressesGuided(explain, route, question) {
		return false, false
	}
	// v0.9.416 — vardiya özeti varsayılanı 12h (guidedRangeS'in 30dk'sı
	// bir vardiyayı anlatamaz); soru açık pencere taşıyorsa o kazanır.
	if route.Intent == guidedShiftSummary {
		if _, explicit := guidedRangeSExplicit(norm); !explicit {
			rangeS = 12 * 3600
		}
	}
	// v0.10.33 — ÇIPA. Eskiden koşulsuz `time.Now()`du: operatör dün gece
	// 03:00-04:00'a zoom yapıp soru sorduğunda, sohbet aynı UZUNLUKTA ama
	// BUGÜNKÜ pencereyi cevaplıyordu. Sayılar gerçek olduğu için hata
	// sessiz kalıyordu. anchorTo göreli aralıkta zaten `now()`dur
	// (chat_anchor.go); mutlak seçimde operatörün penceresidir.
	return s.runGuidedRoute(ctx, emit, route, rangeS, question, msgs, explain, ctxService, ctxOperation, followBase, anchorTo)
}

// runGuidedRoute — v0.10.172: ÇÖZÜLMÜŞ bir rota için prefetch → anlatım →
// answer. copilotChatGuided'dan ayrıldı ki LLM niyet sınıflandırıcısı
// (copilot_intent.go) deterministik router'ı atlayıp aynı paketleri ve aynı
// anlatım çağrısını kullanabilsin: iki yolun cevabı, çipleri ve linkleri
// birebir aynı üretimden çıkar. Davranış değişmedi — gövde olduğu gibi taşındı.
func (s *Server) runGuidedRoute(ctx context.Context, emit func(string, any), route guidedRoute, rangeS int64, question string, msgs []copilot.ChatMessage, explain, ctxService, ctxOperation, followBase string, anchorTo time.Time) (handled, ok bool) {
	to := anchorTo
	if to.IsZero() {
		to = time.Now()
	}
	from := to.Add(-time.Duration(rangeS) * time.Second)

	// v0.10.434 (D7b) — open_page: overview özne ister; yoksa "hangisini
	// kastettin?" (çip: "X sayfasını aç"). Deterministik cevap, LLM yok.
	if route.Intent == guidedOpenPage && route.Service == "" && route.Page == "overview" {
		route.Intent, route.AskIntent = guidedAskService, guidedOpenPage
	}
	// v0.10.453 — trace_by_id: Explain'in çekirdeği BİREBİR (aynı prompt
	// çifti, aynı önbellek anahtarı, yüzey explain-trace:chat). Bulunamayan
	// trace eski dürüst kanıt yoluna (guidedTraceBundle) düşer.
	if route.Intent == guidedTraceByID {
		if handled, ok := s.guidedTraceExplain(ctx, emit, route, question, from, to, ctxService); handled {
			return handled, ok
		}
	}
	// v0.10.463 (D1) — find_entity: LLM'siz varlık kartı / aday çipleri / liste
	// (find_entity.go). Okuma başarısızsa serbest döngüye bırakır.
	if route.Intent == guidedFindEntity {
		if handled, ok := s.guidedFindEntityAnswer(ctx, emit, route, from, to, rangeS); handled {
			return handled, ok
		}
		return false, false
	}
	if route.Intent == guidedOpenPage {
		links := dedupLinksByHref(guidedAnswerLinks(route, linkWindowBetween(from, to)))
		ans := map[string]any{"text": openPageAnswerTR(route), "suggestions": guidedSuggestions(route), "links": links}
		if len(links) > 0 {
			ans["open"] = links[0].Href
		}
		emit("answer", ans)
		return true, true
	}

	var evidence, sources string
	var err error
	switch route.Intent {
	case guidedRootCause:
		evidence, sources, err = s.guidedRootCauseBundle(ctx, emit, route.Service, route.Env, from, to, rangeS)
	case guidedProblems:
		evidence, sources, err = s.guidedProblemsBundle(ctx, emit, route.Service, route.Env, to)
	case guidedServiceHealth:
		// v0.9.184 — operasyon-scope yükseltmesi: soru belirli bir
		// operasyonu adlandırıyorsa (ya da operatör bir operasyon
		// sayfasındaysa) RED'i o span-name'e daraltıp operasyon
		// bundle'ına yönlendir; aksi halde servis-geneli kalır.
		// v0.9.410 — takip sorusu operasyonu adlandırmaz ("peki p99?");
		// mevcut soru çözmezse devralınan temel mesajdan dene.
		op := s.resolveGuidedOperation(ctx, route.Service, question, ctxOperation)
		if op == "" && followBase != "" {
			op = s.resolveGuidedOperation(ctx, route.Service, followBase, "")
		}
		if op != "" {
			evidence, sources, err = s.guidedOperationHealthBundle(ctx, emit, route.Service, op, route.Env, from, to, rangeS)
		} else {
			evidence, sources, err = s.guidedServiceHealthBundle(ctx, emit, route.Service, route.Env, from, to, rangeS)
		}
	case guidedFamilyHealth:
		evidence, sources, err = s.guidedFamilyHealthBundle(ctx, emit, route.Family, route.Env, from, to, rangeS)
	case guidedSlowTraces:
		evidence, sources, err = s.guidedSlowTracesBundle(ctx, emit, route.Service, route.Env, from, to, rangeS)
	case guidedDeployImpact:
		evidence, sources, err = s.guidedDeployBundle(ctx, emit, route.Service, route.Env, rangeS, anchorTo)
	case guidedLogErrors:
		evidence, sources, err = s.guidedLogErrorsBundle(ctx, emit, route.Service, route.Env, from, to, rangeS)
	case guidedLogField: // v0.10.433 (D5)
		evidence, sources, err = s.guidedLogFieldBundle(ctx, emit, &route, from, to, rangeS)
	case guidedPairRequests: // v0.10.436 (D2a)
		evidence, sources, err = s.guidedPairBundle(ctx, emit, &route, from, to, rangeS)
	case guidedTraceSearch: // v0.10.436 (D2b)
		evidence, sources, err = s.guidedTraceSearchBundle(ctx, emit, &route, from, to, rangeS)
	case guidedWindowCompare: // v0.10.437 (D6)
		evidence, sources, err = s.guidedWindowCompareBundle(ctx, emit, &route)
	case guidedCallPeriod: // v0.10.438 (D3)
		evidence, sources, err = s.guidedCallPeriodBundle(ctx, emit, &route, to)
	case guidedFanout: // v0.10.439 (D4)
		evidence, sources, err = s.guidedFanoutBundle(ctx, emit, &route, from, to, rangeS)
	case guidedMyServices:
		evidence, sources, err = s.guidedMyTeamBundle(ctx, emit, "health", &route, from, to, rangeS)
	case guidedMyProblems:
		evidence, sources, err = s.guidedMyTeamBundle(ctx, emit, "problems", &route, from, to, rangeS)
	case guidedMyExceptions:
		evidence, sources, err = s.guidedMyTeamBundle(ctx, emit, "exceptions", &route, from, to, rangeS)
	case guidedTeamServices:
		evidence, sources, err = s.guidedTeamServicesBundle(ctx, emit, &route, from, to, rangeS)
	case guidedPodHealth:
		evidence, sources, err = s.guidedPodHealthBundle(ctx, emit, route.Service, from, to)
	case guidedShiftSummary:
		evidence, sources, err = s.guidedShiftSummaryBundle(ctx, emit, route.Service, from, to, rangeS)
	case guidedDBHealth:
		evidence, sources, err = s.guidedDBHealthBundle(ctx, emit, route.Service, from, to, rangeS)
	case guidedMessagingHealth:
		evidence, sources, err = s.guidedMessagingBundle(ctx, emit, route.Service, from, to, rangeS)
	case guidedTraceByID:
		evidence, sources, err = s.guidedTraceBundle(ctx, emit, route.TraceID)
	case guidedSelfMeta:
		evidence, sources, err = s.guidedSelfMetaBundle(emit)
	case guidedSpanByID:
		evidence, sources, err = s.guidedSpanBundle(ctx, emit, route.SpanID, from, to)
	case guidedRequestID:
		// Pencere ROTADAN gelmiyor: kimliğin İÇİNDEKİ damgadan türüyor,
		// yani sohbetin from/to'su bilinçli olarak geçilmiyor. Bundle
		// çözdüğü trace'i ve pencereyi rotaya YAZAR (derin linkler için).
		evidence, sources, err = s.guidedRequestIDBundle(ctx, emit, &route)
	case guidedAskService:
		// v0.10.429 (D1) — "hangisini kastettin?": adaylar rotada (router/
		// sınıflandırıcı) ya da buradan (açık problemi olan servisler).
		evidence, sources, err = s.guidedAskServiceEvidence(ctx, &route, question)
	}
	if err != nil {
		// Prefetch failed hard → let the free loop try; its tools may
		// route differently. The steps already emitted just render as
		// extra progress chips.
		return false, false
	}

	// The ONE self-recording model call, via the surface-explicit
	// wrapper so the ai_calls row lands as "chat-guided" — quality
	// tracking for the guided path, separate from the free-loop
	// "chat" rows.
	// v0.8.404 — token streaming: the narration call streams its
	// answer tokens as `delta` events on the chat SSE. The `answer`
	// event below stays the UNCHANGED source of truth (full text +
	// exchangeId feedback anchor) — old frontends that ignore `delta`
	// render exactly as before, and when the endpoint can't stream
	// (vLLM builds that 400 on stream:true) StreamText falls back to
	// the buffered call transparently: zero deltas, same answer.
	// v0.9.479 — çekmece bağlamı (varsa) narration bloğuna ek bölüm
	// olarak girer; explain boşken üretilen metin bayt-bayt eskisidir
	// (guidedNarrationUser, copilot_drawer.go — testte pinli).
	// v0.9.1231 — konuşma geçmişi de bloğa girer. msgs, copilot_chat.go'da
	// assemble.ClampHistory'den GEÇMİŞ dizinin ta kendisidir (rotalamadan
	// önce klamplanır); guided ikinci bir kaynak kurmaz, yalnız kendi dar
	// bütçesini (guidedHistoryMaxRunes) uygular — kanıt geçmişten önce
	// gelir. Geçmiş yoksa metin yine bayt-bayt eskisidir.
	user := guidedNarrationUser(question, evidence, explain, msgs)
	// v0.9.528 Faz 2 — kiminle konuşulduğu prompt'un başına girer
	// (ctx'ten; ön-söz boşsa metin bayt-bayt eskisi).
	raw, exErr := s.copilotStreamSurface(ctx, "chat-guided", withAddressee(addresseeFromCtx(ctx), guidedNarrationPrompt(route.Intent)), user, func(delta string) {
		emit("delta", map[string]string{"text": delta})
	})
	if exErr != nil {
		emit("error", map[string]string{"error": exErr.Error()})
		return true, false
	}
	// Deterministic provenance footer — appended server-side, never
	// trusted to the model.
	// exchangeId (v0.8.399): the id the chat handler minted rides in
	// on CallMeta — the Explain call above already recorded it on the
	// "chat-guided" ai_calls row, so the UI's thumbs up/down joins
	// back to it exactly like the free tool loop's answers.
	// v0.10.70 — MODELİN AKTARDIĞI ÇİT, SUNUCUNUN YAZDIĞI ÇİT OLMALI.
	//
	// Guided'da grafik ekrana ancak model çiti AKTARIRSA ulaşıyor
	// (v0.10.68 bunu neden sökemeyeceğimizle birlikte yazdı). Kalan risk
	// oradaydı: model spec'i DEĞİŞTİREBİLİR ya da kendi uydurduğunu araya
	// sokabilir — sonuç "doğru veriyle çizilmiş yanlış kapsam".
	//
	// İzin listesi KANITTAN türüyor ve kanıt sunucu-yazımı; yani model
	// listeye üye EKLEYEMEZ. İmzaya gerek yok: sunucu ne yazdığını zaten
	// biliyor (guided_chart_allowlist.go).
	answer := strings.TrimSpace(raw) + "\n\nKaynak: " + sources
	answer, _ = filterModelChartFences(answer, serverChartScopes(evidence))
	// v0.9.419 — rotadan türetilen deterministik derin linkler; frontend
	// çip olarak çizer (eski frontend'ler yok sayar).
	// v0.9.1321 (§3.1 K6) — çipler cevabın ÜZERİNDE hesaplandığı pencereyi
	// taşır. `from`/`to` yukarıda bundle'lara verilen aralığın ta kendisi,
	// yani link operatöre "bu cevap şu aralıktan çıktı" der; şimdiye
	// çapalanan bir tahmin değil.
	links := guidedAnswerLinks(route, linkWindowBetween(from, to))
	// v0.9.1142 — yapılandırılmış request-ID rotasında köprü çipi
	// ROTADAN da kurulur: kimliği sunucu biliyor, modelin onu cevapta
	// tekrar etmesini beklemek gereksiz kırılganlık. Metinden avlanan
	// kopyayla çakışırsa href'e göre tekilleşir.
	links = append(links, s.knownRequestIDLinks(ctx, route.RequestID, ctxService)...)
	// v0.9.709 — rota çiplerine cevap metnindeki request_id log
	// köprüsü çipleri eklenir (operatör-bildirimi: CoSRE id'yi
	// buluyor ama linklemiyordu).
	links = append(links, s.answerRequestIDLinks(ctx, answer, ctxService)...)
	// v0.9.411 — konuya-duyarlı takip önerileri (guidedSuggestions,
	// copilot_followup.go). Eski frontend'ler alanı yok sayar.
	emit("answer", map[string]any{
		"text": answer, "exchangeId": copilot.MetaFromContext(ctx).ExchangeID,
		"suggestions": guidedSuggestions(route),
		"links":       dedupLinksByHref(links),
	})
	return true, true
}

// guidedServiceNames returns the live service-name list for entity
// extraction, Redis-cached for 60s so chat traffic costs at most one
// catalogue read per minute per replica. Soft-fails to nil — the
// router still handles the entity-free intents.
func (s *Server) guidedServiceNames(ctx context.Context) []string {
	const key = "copilot:guided:svcnames"
	if b, ok, _ := s.cache.Get(ctx, key); ok && len(b) > 0 {
		var names []string
		if json.Unmarshal(b, &names) == nil {
			return names
		}
	}
	names, _, err := s.store.ListServiceNames(ctx, "", 2000, 0)
	if err != nil {
		return nil
	}
	if b, merr := json.Marshal(names); merr == nil {
		_ = s.cache.Set(ctx, key, b, 60*time.Second)
	}
	return names
}

// guidedEnvNames returns the live deployment-environment list for
// env-entity extraction (v0.8.398), the env twin of guidedServiceNames:
// Redis-cached 60s so chat traffic costs at most one enumeration per
// minute per replica. ListEnvironments with zero from/to resolves to
// the last hour and is count-ordered (busiest env first — extraction's
// equal-length tie-break follows list order, so the busiest wins).
// Soft-fails to nil — the router then runs env-blind, which is the
// pre-v0.8.398 behaviour.
func (s *Server) guidedEnvNames(ctx context.Context) []string {
	const key = "copilot:guided:envnames"
	if b, ok, _ := s.cache.Get(ctx, key); ok && len(b) > 0 {
		var names []string
		if json.Unmarshal(b, &names) == nil {
			return names
		}
	}
	names, _, err := s.store.ListEnvironments(ctx, time.Time{}, time.Time{}, "", 200)
	if err != nil {
		return nil
	}
	if b, merr := json.Marshal(names); merr == nil {
		_ = s.cache.Set(ctx, key, b, 60*time.Second)
	}
	return names
}

// guidedTeamNames (v0.9.1134) — canlı takım kataloğu, guidedServiceNames'in
// takım ikizi: Redis'te 60sn, yani sohbet trafiği replika başına dakikada
// en fazla bir türetme ödüyor.
//
// Maliyet notu: kaynak okuma ListServiceMetadata ve o ZATEN süreç-içi 30sn
// cache'li + yazma-tarafı invalidasyonlu (v0.8.359). Redis katmanı asıl
// olarak takım sayımı + sıralamayı ve alias point-read'ini her mesajda
// yeniden yapmamak için var. Hata → nil: router takım-kör koşar, yani
// v0.9.1134 öncesi davranış.
//
// v0.9.1244 — okuma mcptools.ReadTeamCatalogue: list_teams tool'u ile
// AYNI katalog, aynı sıra. İki yüzeyin farklı takım listesi göstermesi
// (biri alias'ları birleştirir, öbürü birleştirmez) sessiz bir sapma
// olurdu.
func (s *Server) guidedTeamNames(ctx context.Context) []string {
	const key = "copilot:guided:teamnames"
	if b, ok, _ := s.cache.Get(ctx, key); ok && len(b) > 0 {
		var names []string
		if json.Unmarshal(b, &names) == nil {
			return names
		}
	}
	data, err := mcptools.ReadTeamCatalogue(ctx, s.mcpDeps())
	if err != nil {
		return nil
	}
	names := mcptools.TeamCatalogueNames(data.Teams)
	if b, merr := json.Marshal(names); merr == nil {
		_ = s.cache.Set(ctx, key, b, 60*time.Second)
	}
	return names
}

// emitGuidedStep — guided kanıt paketinin ⚙ çipi. v0.9.1229'dan beri
// çipin KİMLİĞİNİ döndürür; çağıran o kimlikle eşli kanıtı
// (emitGuidedStepResult) yayınlar. Kimlik üretimi chat_step_ids.go'da,
// tek sayaçta.
func emitGuidedStep(emit func(string, any), tool, args string) int {
	return emitStepChipOrigin(emit, tool, args, "guided") // v0.10.161 — köken rozeti
}

// emitGuidedStepResult — adımın KANITI (v0.9.1229).
//
// `segment` o adımın OKUDUĞU verinin metnidir: ya prompt'a giren
// bloğun ta kendisi, ya da okumanın kendi çıktısı (ör. takım→servis
// çözümlemesinin ad listesi). Model tarafından yazılmış bir özet ASLA
// değil — özetlenmiş kanıt kanıt değildir; operatörün sınayacağı şey
// modelin okuduğu şey olmalı.
//
// Birkaç okuma TEK bloğa akıyorsa (ör. pod envanteri + heap tek ortak
// katman çağrısında) blok her iki çipe de verilir: yarısını uydurmak
// ya da çipi ölü bırakmak, aynı bloğu iki kez göstermekten kötü.
func emitGuidedStepResult(emit func(string, any), i int, tool, segment string, err error) {
	emitStepEvidence(emit, i, tool, segment, err)
}

// emitGuidedContextStep — kanıt okuması OLMAYAN bağlam çipi ("bağlam:
// ekrandaki trace (…)"). Kimlik alır (çip şeridi ile detay dizisi
// hizada kalsın) ama eşli kanıt YAYINLAMAZ: okunan bir şey yok, yani
// çip düz etiket olarak kalır — doğrusu da bu.
func emitGuidedContextStep(emit func(string, any), label string) {
	_ = emitStepChipOrigin(emit, label, "", "guided")
}

// guidedStepSegment — b'ye adım açıldığından beri YAZILAN metin.
// strings.Builder.String() kopya üretmez, yani bu dilim ucuzdur.
// Kanıt bloğunu satır satır b'ye yazan bundle'lar (vardiya özeti,
// servis/operasyon sağlığı) adımın payını böyle ölçüyor.
func guidedStepSegment(b *strings.Builder, at int) string {
	s := b.String()
	if at < 0 || at > len(s) {
		return ""
	}
	return s[at:]
}

// withEnvArg appends the applied env to a step-event args echo so the
// operator's progress chip shows env=uat when the bundle read was
// env-narrowed (v0.8.398). Pure — table-tested. Bundles that CANNOT
// apply the env (logs/deploys, Phase 4 pending) never call this —
// the step echo only ever shows filters that were actually applied.
func withEnvArg(argsJSON, env string) string {
	if env == "" {
		return argsJSON
	}
	if argsJSON == "" || argsJSON == "{}" {
		return `{"env":"` + env + `"}`
	}
	return strings.TrimSuffix(argsJSON, "}") + `,"env":"` + env + `"}`
}

// guidedEnvlessNoteTR flags an env ask on a bundle whose data path has
// no env dimension yet (logs + deploy markers — env-separation Phase 4
// pending): the evidence SAYS the filter was not applied instead of
// silently ignoring it. The narration prompt forbids inventing, so
// this line is what keeps the 2B model from claiming "uat'ta ...".
// Pure — table-tested (v0.8.398).
func guidedEnvlessNoteTR(what, env string) string {
	if env == "" {
		return ""
	}
	return fmt.Sprintf("Not: %s ortam boyutu taşımıyor (env-ayrımı Faz 4 bekliyor) — %q ortam filtresi UYGULANMADI; sayılar tüm ortamların toplamı.\n", what, env)
}

// guidedProblemFilter builds the problems prefetch filter. Extracted
// pure so the env threading (ProblemFilter.Env — service-scoped
// semantics, env_members.go) is pinned by a table test (v0.8.398).
func guidedProblemFilter(service, env string, limit int) chstore.ProblemFilter {
	return chstore.ProblemFilter{Status: "open", Service: service, Env: env, Limit: limit}
}

// guidedTraceFilter builds the slow-traces prefetch filter. Extracted
// pure so the env threading (TraceFilter.Env — direct deploy_env
// conjunct, raw-fallback path) is pinned by a table test (v0.8.398).
func guidedTraceFilter(service, env string, from, to time.Time) chstore.TraceFilter {
	return chstore.TraceFilter{
		Service: service, Env: env, From: from, To: to,
		Sort: "duration", Order: "desc", Limit: 10, CountMode: "skip",
	}
}

// ─── Prefetch bundles (bounded, existing reads only) ────────────────

// (a) "errors/problems now" → open problems + triage priority + the
// persisted deterministic root-cause hypotheses (v0.8.394 enrichment).
// env (v0.8.398) rides ProblemFilter.Env — the service-scoped
// semantics from env_members.go; the evidence spells that out so the
// narration never oversells the filter.
// guidedRootCauseBundle (v0.9.514) — "neden X patladı" sorusunun kanıtı.
//
// Hipotez ZATEN kayıtlı: RootCauseSynthesizer her anchor için
// deterministik sıralı adayları üretip saklıyor, v0.9.510-512 derin
// soruşturması onu pod/metrik/log/iş-boyutu kanıtıyla besliyor. Bu
// bundle o birikimi okuyup önüne koyuyor — model nedenselliği kendi
// UYDURMUYOR, hesaplanmış hipotezi anlatıyor.
//
// Sıra kasıtlı: önce hipotez (asıl cevap), sonra RED (ne değişti), sonra
// deploy (en sık sebep). Açık problem yoksa hipotez de yoktur — o durum
// SESSİZCE geçilmez, açıkça söylenir ve model değişim verisiyle cevap
// verir.
func (s *Server) guidedRootCauseBundle(ctx context.Context, emit func(string, any), service, env string, from, to time.Time, rangeS int64) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "SORU ŞEKLİ: nedensellik — %q servisinde NE OLDU değil NEDEN OLDU sorusu.\n\n", service)

	nProb := emitGuidedStep(emit, "list_problems", withEnvArg(`{"service":"`+service+`","status":"open"}`, env))
	probs, probTotal, perr := s.guidedProblemsWithTotal(ctx, guidedProblemFilter(service, env, 10))
	if perr == nil && len(probs) > 0 {
		probs = s.enrichProblemsForRead(ctx, probs) // v0.9.553 — deploy+öncelik, sırası sabit
		// v0.9.1229 — okumanın kanıtı hipotez EKLENMEDEN önceki bloktur;
		// sıradaki çip aynı listeyi hipotezle gösteriyor, yani
		// zenginleştirmenin NE KATTIĞI operatörde görünür oluyor.
		emitGuidedStepResult(emit, nProb, "list_problems",
			renderProblemsEvidenceTR(probs, service, env, evidenceAsOf(to), probTotal), nil)
		nRC := emitGuidedStep(emit, "root_cause_hypotheses", "")
		probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
		blk := renderProblemsEvidenceTR(probs, service, env, evidenceAsOf(to), probTotal)
		emitGuidedStepResult(emit, nRC, "root_cause_hypotheses", blk, nil)
		b.WriteString(blk)
	} else {
		none := "Bu serviste AÇIK PROBLEM YOK — dolayısıyla kayıtlı bir kök-neden hipotezi de yok. " +
			"Aşağıdaki değişim verisiyle cevap ver ve hipotez olmadığını AÇIKÇA söyle; sebep uydurma.\n\n"
		emitGuidedStepResult(emit, nProb, "list_problems", none, perr)
		b.WriteString(none)
	}

	nCtx := emitGuidedStep(emit, "service_context", `{"service":"`+service+`"}`)
	cx := s.buildServiceContext(ctx, service, from, to)
	atCtx := b.Len()
	b.WriteString(renderServiceSnapshot(cx))
	if cx.Current.Spans == 0 {
		b.WriteString("Bu pencerede span verisi yok — değişim de okunamıyor.\n")
	}
	emitGuidedStepResult(emit, nCtx, "service_context", guidedStepSegment(&b, atCtx), nil)

	// Deploy: "neden bozuldu" sorusunun en sık cevabı. Ayrı bir adım
	// olarak emit ediliyor ki operatör hangi kanıtın çekildiğini görsün.
	nDep := emitGuidedStep(emit, "recent_deploys", `{"service":"`+service+`"}`)
	dep, _, derr := s.guidedDeployBundle(ctx, func(string, any) {}, service, env, rangeS, to)
	emitGuidedStepResult(emit, nDep, "recent_deploys", dep, derr)
	if derr == nil && strings.TrimSpace(dep) != "" {
		b.WriteString("\n")
		b.WriteString(dep)
	}

	b.WriteString("\nKURAL: Yukarıdaki kök-neden hipotezi HESAPLANMIŞ bir sıralamadır, tahmin değil. " +
		"Onu anlat ve güven skorunu birlikte ver. Hipotez yoksa ya da güveni düşükse sebep UYDURMA — " +
		"hangi kanıta baktığını yaz ve 'kesin sebep için yeterli kanıt yok' de.\n")

	src := fmt.Sprintf("kök-neden hipotezi + açık problemler + servis RED değişimi + deploy geçmişi (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += fmt.Sprintf("; problemler ortam: %s", env)
	}
	return b.String(), src, nil
}

// asOf (v0.10.65) — kanıt YAŞLARININ dayanağı. Diğer paketler pencere
// sonunu (`to`) zaten taşıyor; bu paket taşımıyordu ve `time.Now()`
// kullanıyordu, yani çıpalı bir pencerede "4 saattir açık" cümlesi
// saatlerce kayıyordu.
func (s *Server) guidedProblemsBundle(ctx context.Context, emit func(string, any), service, env string, asOf time.Time) (string, string, error) {
	nProb := emitGuidedStep(emit, "list_problems", withEnvArg(`{"status":"open"}`, env))
	probs, probTotal, err := s.guidedProblemsWithTotal(ctx, guidedProblemFilter(service, env, 50))
	if err != nil {
		emitGuidedStepResult(emit, nProb, "list_problems", "", err)
		return "", "", err
	}
	probs = s.enrichProblemsForRead(ctx, probs) // v0.9.553 — deploy+öncelik, sırası sabit
	// v0.9.1229 — çipin kanıtı: hipotez eklenmeden ÖNCEKİ liste.
	emitGuidedStepResult(emit, nProb, "list_problems",
		renderProblemsEvidenceTR(probs, service, env, evidenceAsOf(asOf), probTotal), nil)
	nRC := emitGuidedStep(emit, "root_cause_hypotheses", "")
	probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
	evidence := renderProblemsEvidenceTR(probs, service, env, evidenceAsOf(asOf), probTotal)
	emitGuidedStepResult(emit, nRC, "root_cause_hypotheses", evidence, nil)
	src := "açık problemler + triage önceliği + kök-neden hipotezleri (canlı)"
	if env != "" {
		evidence += fmt.Sprintf("Not: problem kayıtları ortam boyutu taşımaz — %q filtresi servis üyeliğiyle uygulandı (bu ortamda koşan servislerin problemleri + global kurallar).\n", env)
		src += fmt.Sprintf(", ortam: %s (servis kapsamlı)", env)
	}
	return evidence, src, nil
}

// (b) "service X sağlığı/health/slow" → the analyze-service context
// bundle (buildServiceContext + renderServiceSnapshot, reused
// verbatim) + the service's open problems with root-cause.
// buildServiceContext is env-BLIND (its MV reads have no env
// dimension) — an env ask (v0.8.398) narrows only the problems
// sub-read and prepends an honest one-liner so the model attributes
// the RED numbers correctly instead of claiming they're env-scoped.
func (s *Server) guidedServiceHealthBundle(ctx context.Context, emit func(string, any), service, env string, from, to time.Time, rangeS int64) (string, string, error) {
	nCtx := emitGuidedStep(emit, "service_context", `{"service":"`+service+`"}`)
	cx := s.buildServiceContext(ctx, service, from, to)
	var b strings.Builder
	if env != "" {
		fmt.Fprintf(&b, "Not: RED değerleri tüm ortamların toplamı (servis bağlamı ortam kırılımı yapmıyor); açık problemler %q ortamına daraltıldı.\n", env)
	}
	atSnap := b.Len()
	b.WriteString(renderServiceSnapshot(cx))
	if cx.Current.Spans == 0 {
		b.WriteString("Bu pencerede span verisi yok.\n")
	}
	emitGuidedStepResult(emit, nCtx, "service_context", guidedStepSegment(&b, atSnap), nil)
	nProb := emitGuidedStep(emit, "list_problems", withEnvArg(`{"service":"`+service+`"}`, env))
	probs, probTotal, perr := s.guidedProblemsWithTotal(ctx, guidedProblemFilter(service, env, 10))
	if perr == nil {
		probs = s.enrichProblemsForRead(ctx, probs) // v0.9.553 — deploy+öncelik, sırası sabit
		probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
		atProb := b.Len()
		if len(probs) == 0 {
			b.WriteString("Açık problem yok.\n")
		} else {
			b.WriteString(renderProblemsEvidenceTR(probs, service, env, evidenceAsOf(to), probTotal))
		}
		emitGuidedStepResult(emit, nProb, "list_problems", guidedStepSegment(&b, atProb), nil)
	} else {
		emitGuidedStepResult(emit, nProb, "list_problems", "", perr)
	}
	// v0.9.183 — CoSRE grafik: yanıta yapılandırılmış bir ```chart``` bloğu
	// ekle; frontend (CosreChart) bunu mevcut uPlot motoruyla GERÇEK
	// telemetriden çizer (LLM değil, spanMetricBatch). Servis-sağlık
	// headline'ı = error_rate. Blok görsel olarak dokümana gömülü değildir;
	// eski istemci parse edemezse düz metin olarak görünür (zararsız).
	b.WriteString(chartFence(guidedChartSpec{Title: service + " · error_rate", Service: service, Agg: "error_rate", RangeS: rangeS}))
	// CoSRE Faz-2 — ikinci deterministik kart: p99 latency. Guided yol
	// tool çağrısı yapamayan küçük modelde (gemma4) de zengin görsel
	// versin diye server iki kart basar; serbest döngüdeki eşdeğeri
	// render_chart tool'udur (copilot_chat.go).
	b.WriteString(chartFence(guidedChartSpec{Title: service + " · p99", Service: service, Agg: "p99", RangeS: rangeS}))
	src := fmt.Sprintf("servis RED özeti + baseline + en sık hatalar + deploy işaretçileri + açık problemler + grafikler (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += fmt.Sprintf("; RED tüm ortamlar, problemler ortam: %s", env)
	}
	return b.String(), src, nil
}

// guidedOperationHealthBundle (v0.9.184) — the operasyon twin of the
// service-health bundle. Scopes RED to a single span name + emits a
// live operasyon-scoped chart (name = "..." DSL). Problems stay
// service-level (no operation-scoped Problem row exists) and the
// evidence says so.
func (s *Server) guidedOperationHealthBundle(ctx context.Context, emit func(string, any), service, operation, env string, from, to time.Time, rangeS int64) (string, string, error) {
	nCtx := 0
	if argsB, merr := json.Marshal(map[string]string{"service": service, "operation": operation}); merr == nil {
		nCtx = emitGuidedStep(emit, "operation_context", string(argsB))
	}
	cx := s.buildOperationContext(ctx, service, operation, from, to)
	var b strings.Builder
	if env != "" {
		fmt.Fprintf(&b, "Not: RED tüm ortamların toplamı; açık problemler %q ortamına daraltıldı.\n", env)
	}
	atSnap := b.Len()
	b.WriteString(renderOperationSnapshot(cx))
	if cx.Current.Spans == 0 {
		b.WriteString("Bu pencerede bu operasyon için span verisi yok.\n")
	}
	emitGuidedStepResult(emit, nCtx, "operation_context", guidedStepSegment(&b, atSnap), nil)

	nProb := emitGuidedStep(emit, "list_problems", withEnvArg(`{"service":"`+service+`"}`, env))
	probs, probTotal, perr := s.guidedProblemsWithTotal(ctx, guidedProblemFilter(service, env, 10))
	if perr == nil {
		probs = s.enrichProblemsForRead(ctx, probs) // v0.9.553 — deploy+öncelik, sırası sabit
		probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
		atProb := b.Len()
		if len(probs) == 0 {
			b.WriteString("Servis düzeyinde açık problem yok.\n")
		} else {
			b.WriteString("Servis düzeyinde açık problemler (operasyon-özel değil):\n")
			b.WriteString(renderProblemsEvidenceTR(probs, service, env, evidenceAsOf(to), probTotal))
		}
		emitGuidedStepResult(emit, nProb, "list_problems", guidedStepSegment(&b, atProb), nil)
	} else {
		emitGuidedStepResult(emit, nProb, "list_problems", "", perr)
	}

	// v0.9.184 — operasyon-scoped canlı grafik: error_rate headline.
	// Frontend (CosreChart) bunu spanMetricBatch(name = "op") ile çizer.
	b.WriteString(chartFence(guidedChartSpec{Title: operation + " · error_rate", Service: service, Operation: operation, Agg: "error_rate", RangeS: rangeS}))

	src := fmt.Sprintf("operasyon RED özeti + baseline + servis açık problemleri + grafik (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += fmt.Sprintf("; problemler ortam: %s", env)
	}
	return b.String(), src, nil
}

// maxTeamServices — takım-kapsamlı MV okumasının IN listesi tavanı.
// Üstü okunmaz ve kanıt bunu SÖYLER. v0.9.1134'te fonksiyon içinden
// dosya kapsamına çıkarıldı: guidedTeamServicesBundle da aynı tavana
// uymalı, iki ayrı sayı zamanla ayrışırdı. v0.9.1244'te mcptools'a
// taşındı — get_team_services de aynı tavanı ilan ediyor.
const maxTeamServices = mcptools.MaxTeamServices

// servicesForUserTeam (v0.9.375) — kullanıcının takımına ait servisler:
// ownerTeam VEYA sreTeam eşleşmesi (case-insensitive). Inbox'ın
// servicesForTeam'inden farkı: orada owner ve SRE ayrı süzgeçler (AND),
// burada "benim servisim" iki rolün BİRLEŞİMİ.
//
// v0.9.1244 — gövde mcptools.TeamServiceNames'e taşındı (get_team_services
// AYNI çözümlemeyi kullanmak zorunda; mcptools api'yi import edemez).
// Burası tek satırlık delegasyon: KİMLİK yarısı (CallMeta → users →
// User.Team) api'de kalıyor, çünkü MCP tarafında kimlik yok.
func servicesForUserTeam(ta chstore.TeamAliases, mds map[string]chstore.ServiceMetadata, team string) []string {
	return mcptools.TeamServiceNames(ta, mds, team)
}

// guidedMyTeamBundle (v0.9.375, operatör istegi) — "takımımın
// servisleri / problemleri". Kimlik CallMeta'dan; takım users
// tablosundan (tek satır FINAL okuma); servis eşlemesi metadata
// katalogundan. Takımsız kullanıcı ve servissiz takım DÜRÜST cevap
// alır (boş liste sessizce "her şey" olmaz — inbox'ın v0.9.353
// dersinin tersi yönü). wantProblems=false → aile-sağlık bundle'ı
// (tek MV okuması), true → o servislerin açık problemleri.
// guidedMyTeamBundle — takım-kapsamlı üç sorunun ORTAK gövdesi.
//
// v0.9.650 — `wantProblems bool` yerine KİP aldı. Üçüncü bir soru
// (takımın exception'ları) gelince bool yetmiyordu ve alternatif,
// kullanıcı→takım→servis çözümlemesini (25 satır: oturum kimliği,
// takım ataması, katalog eşleşmesi, 100-servis tavanı, her birinin
// kendi operatör mesajı) İKİNCİ bir yere kopyalamaktı. Bu kod
// tabanında tekrar eden hata sınıfı tam olarak o: bir kural iki yere
// bölünüp zamanla ayrışıyor.
//
// mode: "health" | "problems" | "exceptions"
func (s *Server) guidedMyTeamBundle(ctx context.Context, emit func(string, any), mode string, route *guidedRoute, from, to time.Time, rangeS int64) (string, string, error) {
	env := route.Env
	meta := copilot.MetaFromContext(ctx)
	if meta.UserID == "" {
		// v0.9.1134 — eskiden burada ÇIKMAZ vardı ("yanıtlanamıyor").
		// Artık takımı SORUYORUZ ve canlı takım listesini çip olarak
		// sunuyoruz; operatör tıklayınca guidedTeamServices devralıyor.
		return s.guidedAskTeamEvidence(ctx, route,
			"Oturum kimliği yok (auth kapalı ya da token kullanıcıya bağlı değil), bu yüzden kullanıcının takımını KENDİM okuyamıyorum.\n")
	}
	nTeam := emitGuidedStep(emit, "resolve_user_team", "")
	u, uerr := s.store.GetUserByID(ctx, meta.UserID)
	if uerr != nil || u == nil {
		err := fmt.Errorf("user lookup: %w", uerr)
		emitGuidedStepResult(emit, nTeam, "resolve_user_team", "", err)
		return "", "", err
	}
	if u.Team == "" {
		ev, src, aerr := s.guidedAskTeamEvidence(ctx, route,
			fmt.Sprintf("Kullanıcının (%s) hesabına takım atanmamış (admin Settings → Users → Team alanı bunu kalıcı çözer).\n", u.Email))
		emitGuidedStepResult(emit, nTeam, "resolve_user_team", ev, aerr)
		return ev, src, aerr
	}
	mds, merr := s.store.ListServiceMetadata(ctx)
	if merr != nil {
		emitGuidedStepResult(emit, nTeam, "resolve_user_team", "", merr)
		return "", "", merr
	}
	ta := s.teamAliasesCtx(ctx)
	svcs := servicesForUserTeam(ta, mds, u.Team)
	if len(svcs) == 0 {
		ev := fmt.Sprintf("%q takımı hiçbir serviste ownerTeam/sreTeam olarak geçmiyor (Service Catalog). Kullanıcıya söyle: katalogda takım ataması yapılmalı.\n", u.Team)
		emitGuidedStepResult(emit, nTeam, "resolve_user_team", ev, nil)
		return ev, fmt.Sprintf("servis kataloğu (takım: %s)", u.Team), nil
	}
	trimmed := 0
	if len(svcs) > maxTeamServices {
		trimmed = len(svcs) - maxTeamServices
		svcs = svcs[:maxTeamServices]
	}
	// v0.9.651 — çözülen liste rotaya yazılıyor; guidedSuggestions
	// buradan servis-adlı çipler üretiyor.
	route.TeamServices = svcs
	// v0.9.1246 — KİMLİK burada çözülüyor, LİNKTE değil: "takımımın
	// exception'ları" cevabının altındaki derin link
	// /inbox?kind=exception&team=<AD> yazacak, yani URL paylaşılabilir
	// olmalı ve "benim" kelimesini TAŞIYAMAZ (başkasının açtığı link
	// onun takımını gösterirdi — kimlik URL'e girmez).
	//
	// Yazım KATALOGDAN (TeamDisplayName): users tablosunda "sy" yazıyor
	// olabilir ama katalogda "SY" geçiyorsa çipte kataloğun yazımı
	// görünmeli. Eşleşme zaten katlamalı (TeamEqual), yani fark
	// yalnızca operatörün GÖRDÜĞÜ metinde — ve orada iki yazım iki ayrı
	// takım gibi okunuyor.
	//
	// Servissiz takımda (yukarıdaki erken dönüş) bilerek YAZILMIYOR:
	// cevabın kendisi "bu takım hiçbir serviste geçmiyor" diyorken
	// takım-filtreli boş bir sayfaya link vermek, cevabı tekrar eden
	// bir çıkmaz olurdu.
	route.Team = mcptools.TeamDisplayName(ta, mds, u.Team)
	header := fmt.Sprintf("Kullanıcının takımı: %s — %d servis (owner/SRE eşleşmesi).\n", u.Team, len(svcs)+trimmed)
	if trimmed > 0 {
		header += fmt.Sprintf("Not: ilk %d servis okundu, %d servis dışarıda kaldı.\n", maxTeamServices, trimmed)
	}
	// v0.9.1229 — çözümün kanıtı: takım satırı + ÇÖZÜLEN servis adları.
	// Adlar prompt'a liste olarak girmiyor ama okumanın çıktısı tam olarak
	// bu; "hangi servisleri benim saydı" operatörün ilk sorusu.
	emitGuidedStepResult(emit, nTeam, "resolve_user_team",
		header+"Çözülen servisler:\n- "+strings.Join(svcs, "\n- ")+"\n", nil)
	if mode == "problems" {
		nProb := emitGuidedStep(emit, "list_problems", fmt.Sprintf(`{"status":"open","teamServices":%d}`, len(svcs)))
		probs, probTotal, perr := s.guidedProblemsWithTotal(ctx, chstore.ProblemFilter{Status: "open", Services: svcs, Env: env, Limit: 50})
		if perr != nil {
			emitGuidedStepResult(emit, nProb, "list_problems", "", perr)
			return "", "", perr
		}
		probs = s.enrichProblemsForRead(ctx, probs) // v0.9.553 — deploy+öncelik, sırası sabit
		emitGuidedStepResult(emit, nProb, "list_problems",
			renderProblemsEvidenceTR(probs, "", env, evidenceAsOf(to), probTotal), nil)
		nRC := emitGuidedStep(emit, "root_cause_hypotheses", "")
		probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
		var b strings.Builder
		b.WriteString(header)
		atProb := b.Len()
		if len(probs) == 0 {
			fmt.Fprintf(&b, "Takımın servislerinde açık problem yok.\n")
		} else {
			b.WriteString(renderProblemsEvidenceTR(probs, "", env, evidenceAsOf(to), probTotal))
		}
		emitGuidedStepResult(emit, nRC, "root_cause_hypotheses", guidedStepSegment(&b, atProb), nil)
		src := fmt.Sprintf("takım: %s (%d servis) — açık problemler + triage önceliği + kök-neden hipotezleri", u.Team, len(svcs)+trimmed)
		return b.String(), src, nil
	}
	if mode == "exceptions" {
		// v0.9.650 (operatör: "Takımıma ait servislerin hataları
		// (Exceptions) neler?") — Problem'ler ile Exception'lar AYRI
		// yüzeyler: Problem bir alarm kuralının açtığı kayıt,
		// Exception ise span'lerden gruplanan ham hata. "Takımımın
		// problemleri" ikincisini KAPSAMIYORDU.
		//
		// Tek sorgu: ExceptionFilter.Services (v0.9.650) takımın tüm
		// servislerini IN ile tarıyor — servis başına ayrı çağrı,
		// takım büyüdükçe doğrusal büyüyen bir JSON-kazıma yükü
		// olurdu.
		nEx := emitGuidedStep(emit, "list_exceptions", fmt.Sprintf(`{"teamServices":%d}`, len(svcs)))
		exs, eerr := s.store.GetExceptions(ctx, chstore.ExceptionFilter{
			Services: svcs, GroupBy: "type-service", From: from, To: to, Limit: 30,
		})
		if eerr != nil {
			emitGuidedStepResult(emit, nEx, "list_exceptions", "", eerr)
			return "", "", eerr
		}
		var b strings.Builder
		b.WriteString(header)
		atEx := b.Len()
		if len(exs) == 0 {
			b.WriteString("Takımın servislerinde bu pencerede exception yok.\n")
		} else {
			fmt.Fprintf(&b, "Takımın servislerindeki exception'lar (en çok görülen önce, %d grup):\n", len(exs))
			for i, e := range exs {
				if i >= 20 {
					fmt.Fprintf(&b, "… ve %d grup daha.\n", len(exs)-20)
					break
				}
				fmt.Fprintf(&b, "- %s · %s · %d kez", e.Service, e.Type, e.Count)
				if e.Message != "" {
					fmt.Fprintf(&b, " · %s", truncate(e.Message, 140))
				}
				b.WriteString("\n")
			}
		}
		emitGuidedStepResult(emit, nEx, "list_exceptions", guidedStepSegment(&b, atEx), nil)
		src := fmt.Sprintf("takım: %s (%d servis) — exception grupları", u.Team, len(svcs)+trimmed)
		return b.String(), src, nil
	}
	evidence, src, err := s.guidedFamilyHealthBundle(ctx, emit, svcs, env, from, to, rangeS)
	if err != nil {
		return "", "", err
	}
	return header + evidence, fmt.Sprintf("takım: %s — %s", u.Team, src), nil
}

// guidedTeamAskMax — "hangi takım?" turunda sunulan çip sayısı. 8, çip
// şeridinin tek satırda okunabildiği üst sınır; katalog sırası servis
// sayısına göre azalan olduğu için operatörün takımı büyük olasılıkla
// içinde. Liste dışında bir takımı YAZABİLECEĞİ de kanıtta söyleniyor —
// çipler bir menü değil, kısayol.
const guidedTeamAskMax = 8

// guidedAskTeamEvidence (v0.9.1134, operatör istegi: "takım bilinmiyorsa
// KULLANICIYA SOR") — takım-kapsamlı bir soru kimliğe oturmadığında
// üretilen kanıt. İki degrade dalının (kimlik yok / takım atanmamış)
// ORTAK gövdesi; ikisi de eskiden çıkmaz cümleyle bitiyordu.
//
// Akışın mekaniği: buradaki takım adları route.TeamOptions'a yazılır,
// guidedSuggestions onları ÇIPLAK metin çipine çevirir, çipe tıklamak o
// metni yeni bir kullanıcı mesajı olarak gönderir ve router çıplak takım
// adını guidedTeamServices'e yönlendirir. Sunucuda konuşma durumu YOK —
// tek dayanak "çıplak takım adı kendi başına yönlenebilir" olması.
func (s *Server) guidedAskTeamEvidence(ctx context.Context, route *guidedRoute, why string) (string, string, error) {
	opts := s.guidedTeamNames(ctx)
	if len(opts) > guidedTeamAskMax {
		opts = opts[:guidedTeamAskMax]
	}
	route.TeamOptions = opts
	var b strings.Builder
	b.WriteString(why)
	if len(opts) == 0 {
		b.WriteString("Servis kataloğunda hiç takım ataması da yok (ownerTeam/sreTeam boş). " +
			"Kullanıcıya söyle: şimdilik belirli bir SERVİS adıyla sorabilir; kalıcı çözüm Service Catalog'da takım atamak.\n")
		return b.String(), "kullanıcı kimliği + servis kataloğu (takım ataması yok)", nil
	}
	b.WriteString("KULLANICIYA SOR: hangi takımda çalışıyor? Takım adını söylediğinde o takımın servislerini " +
		"EN ÇOK HATA ALAN önce sıralayıp getireceğim.\n")
	fmt.Fprintf(&b, "Katalogdaki en büyük takımlar (%d): %s\n", len(opts), strings.Join(opts, ", "))
	b.WriteString("Bu adlar cevabın altında ÇİP olarak da duruyor — tıklaması yeter; listede yoksa adı yazabileceğini de söyle.\n")
	b.WriteString("KURAL: takım adı UYDURMA, yalnız yukarıdaki listeyi say.\n")
	return b.String(), "servis kataloğu takım listesi (takım sorusu)", nil
}

// teamServicesMaxRows — kanıtta listelenen servis satırı tavanı. Üstü
// "… ve N servis daha" satırıyla DÜRÜSTÇE söylenir; 15, küçük modelin
// (gemma4) anlatıda kaybetmeden sayabildiği üst sınır.
const teamServicesMaxRows = 15

// SIRALAMA SÖZLEŞMESİ (v0.9.1134, operatör istegi: "en çok hata alan /
// error rate yüksek olanlara göre sırala") — ORAN birincil, SAYI eşitlik
// bozucu, ad üçüncü. guidedFamilyHealthBundle'ın sırası bilerek FARKLI
// (sayı birincil): orada soru "hangisinde hata var", yani mutlak hacim.
//
// v0.9.1244 — sıralama artık ORTAK OKUMANIN İÇİNDE
// (mcptools.ReadTeamServicesRED → SortServicesByErrorRate), yani takım
// cevabının "en kötü servis" iddiası guided'da ve get_team_services'te
// aynı satırı gösteriyor. Tablo testi implementasyonun yanında:
// mcptools/team_ownership_test.go.

// renderTeamServicesEvidenceTR — takım servis listesinin kanıt bloğu
// (saf; tablo-testli). rows ZATEN sıralı gelmeli (ortak okuma sıralıyor).
// readSvcs = MV'ye sorulan servis sayısı, trimmed = tavana takılıp hiç
// sorulmayanlar. Üç dürüstlük satırı: tavana takılanlar, satır tavanı,
// pencerede hiç span üretmeyenler.
func renderTeamServicesEvidenceTR(team string, rows []chstore.ServiceSummary, readSvcs, trimmed int, rangeS int64, env string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Takım: %s — %d servis (Service Catalog ownerTeam/sreTeam eşleşmesi).\n", team, readSvcs+trimmed)
	if trimmed > 0 {
		fmt.Fprintf(&b, "Not: ilk %d servis okundu, %d servis dışarıda kaldı.\n", maxTeamServices, trimmed)
	}
	if env != "" {
		fmt.Fprintf(&b, "Not: RED değerleri tüm ortamların toplamı; %q ortam daraltması bu listede UYGULANMADI.\n", env)
	}
	if len(rows) == 0 {
		b.WriteString("Bu pencerede takımın hiçbir servisinden span verisi yok — hata oranı okunamıyor.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Servisler HATA ORANINA göre azalan (son %s, eşitlikte hata sayısı):\n", fmtAgoTR(rangeS))
	for i, r := range rows {
		if i >= teamServicesMaxRows {
			fmt.Fprintf(&b, "… ve %d servis daha (hepsinin hata oranı daha düşük).\n", len(rows)-teamServicesMaxRows)
			break
		}
		fmt.Fprintf(&b, "- %s: hata oranı %%%.2f (%d hata), p99=%.0fms, %d span\n",
			r.Name, r.ErrorRate, r.ErrorCount, r.P99Ms, r.SpanCount)
	}
	if silent := readSvcs - len(rows); silent > 0 {
		fmt.Fprintf(&b, "Not: takımın %d servisi bu pencerede hiç span üretmedi (listede yok, sessiz olabilir).\n", silent)
	}
	// YÖNLENDİRME kuralı (operatörün asıl istegi "kullanıcıyı ilgili
	// servise yönlendir"): liste tek başına bir cevap değil. Model en
	// kötü servisi ADIYLA söylemeli, yoksa 15 satırlık tablo operatöre
	// karar bırakır. Hata yoksa bunu da açıkça söylemeli — "en kötü"
	// diye temiz bir servisi işaretlemek yanlış alarm olurdu.
	b.WriteString("KURAL: cevaba EN KÖTÜ servisi adıyla ve sayısıyla başla, sonra kısa sıralamayı ver. " +
		"Hiçbir serviste hata yoksa bunu AÇIKÇA söyle ve kimseyi işaretleme. " +
		"Son cümlede o servise nasıl inebileceğini söyle (servis sayfası / trace'leri) — " +
		"cevabın altındaki çipler ve linkler zaten oraya gidiyor. Listede olmayan servis adı UYDURMA.\n")
	return b.String()
}

// guidedTeamServicesBundle (v0.9.1134, operatör istegi) — ADI GEÇEN
// takımın servisleri, en çok hata alan önce.
//
// Okuma yolu my_services ile AYNI: takım→servis eşlemesi katalogdan
// (servicesForUserTeam, alias farkındalıklı), RED tek MV okumasından
// (GetServicesAggFilteredIn — servis başına fan-out YOK). Ayrışan tek şey
// sıralama: operatör oranı istedi (sortServicesByErrorRate).
func (s *Server) guidedTeamServicesBundle(ctx context.Context, emit func(string, any), route *guidedRoute, from, to time.Time, rangeS int64) (string, string, error) {
	team := route.Team
	if team == "" {
		return "", "", errors.New("team_services: takım adı boş")
	}
	nResolve := emitGuidedStep(emit, "resolve_team_services", `{"team":`+jsonStr(team)+`}`)
	svcs, trimmed, merr := mcptools.ReadTeamServiceNames(ctx, s.mcpDeps(), team)
	if merr != nil {
		emitGuidedStepResult(emit, nResolve, "resolve_team_services", "", merr)
		return "", "", merr
	}
	if len(svcs) == 0 {
		ev := fmt.Sprintf("%q takımı hiçbir serviste ownerTeam/sreTeam olarak geçmiyor (Service Catalog). "+
			"Kullanıcıya söyle: katalogda takım ataması yapılmalı ya da başka bir takım adı denemeli.\n", team)
		emitGuidedStepResult(emit, nResolve, "resolve_team_services", ev, nil)
		return ev, fmt.Sprintf("servis kataloğu (takım: %s)", team), nil
	}
	// Çözümün kanıtı, RED okumasından ÖNCE: hangi servisler bu takım
	// sayıldı? (v0.9.1229 — katalog eşleşmesi okumanın kendi çıktısı.)
	// Tavan (mcptools.MaxTeamServices) çözümlemenin İÇİNDE uygulandı;
	// `trimmed` tavana takılıp hiç okunmayanları sayıyor.
	emitGuidedStepResult(emit, nResolve, "resolve_team_services",
		fmt.Sprintf("%q takımına eşleşen servisler (%d):\n- %s\n", team, len(svcs), strings.Join(svcs, "\n- ")), nil)
	nRED := emitGuidedStep(emit, "team_services_red", fmt.Sprintf(`{"team":%s,"services":%d}`, jsonStr(team), len(svcs)))
	// v0.9.1244 — okuma + hata-oranı sırası ortak katmanda
	// (get_team_services AYNI çağrıyı yapıyor): "en kötü servis" iddiası
	// iki yüzeyde de aynı satırı göstersin.
	rows, err := mcptools.ReadTeamServicesRED(ctx, s.mcpDeps(), svcs, from, to)
	if err != nil {
		emitGuidedStepResult(emit, nRED, "team_services_red", "", err)
		return "", "", err
	}
	// v0.9.651 emsali — çözülen liste rotaya yazılıyor; çipler ve derin
	// linkler buradan besleniyor. SIRA hata oranına göre, yani
	// TeamServices[0] = EN KÖTÜ servis (link/çip metni ona bakıyor).
	names := make([]string, 0, teamServicesMaxRows)
	for i, r := range rows {
		if i >= teamServicesMaxRows {
			break
		}
		names = append(names, r.Name)
	}
	if len(names) == 0 {
		// Pencerede hiç veri yok: çipler yine de takımın servislerini
		// adlandırsın (alfabetik) — boş çip şeridi operatörü çıkmaza sokar.
		for i, sv := range svcs {
			if i >= teamServicesMaxRows {
				break
			}
			names = append(names, sv)
		}
	}
	route.TeamServices = names
	b := strings.Builder{}
	redBlk := renderTeamServicesEvidenceTR(team, rows, len(svcs), trimmed, rangeS, route.Env)
	emitGuidedStepResult(emit, nRED, "team_services_red", redBlk, nil)
	b.WriteString(redBlk)
	// En kötü servisin canlı error_rate kartı (aile bundle'ının emsali).
	if len(rows) > 0 && rows[0].ErrorCount > 0 {
		b.WriteString(chartFence(guidedChartSpec{
			Title: rows[0].Name + " · error_rate", Service: rows[0].Name,
			Agg: "error_rate", RangeS: rangeS,
		}))
	}
	src := fmt.Sprintf("takım: %s (%d servis) — servis RED'i tek MV okuması, hata oranına göre sıralı (son %s)",
		team, len(svcs)+trimmed, fmtAgoTR(rangeS))
	return b.String(), src, nil
}

// jsonStr — step-event args'ında ad kaçışı. fmt %q strconv.Quote'tur ve
// kontrol karakterli bir adda geçersiz JSON üretir (v0.9.187 dersi).
func jsonStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// guidedFamilyHealthBundle (v0.9.192) — compares a service FAMILY's RED
// side by side ("mobile bff'lerde hangisinde hata var"). ONE MV read
// (GetServicesAggFilteredIn with the family as the serviceIn allowlist)
// — no per-service fan-out. Evidence is error-ranked so the narration
// can answer "hangisinde" directly; the worst service gets a live
// error_rate chart.
func (s *Server) guidedFamilyHealthBundle(ctx context.Context, emit func(string, any), family []string, env string, from, to time.Time, rangeS int64) (string, string, error) {
	nFam := 0
	if argsB, merr := json.Marshal(map[string]any{"services": family}); merr == nil {
		nFam = emitGuidedStep(emit, "family_context", string(argsB))
	}
	rows, err := s.store.GetServicesAggFilteredIn(ctx, from, to, "", family, "", "", len(family), 0)
	if err != nil {
		emitGuidedStepResult(emit, nFam, "family_context", "", err)
		return "", "", err
	}
	// Most-broken first: errors desc, then error-rate, then traffic.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ErrorCount != rows[j].ErrorCount {
			return rows[i].ErrorCount > rows[j].ErrorCount
		}
		if rows[i].ErrorRate != rows[j].ErrorRate {
			return rows[i].ErrorRate > rows[j].ErrorRate
		}
		return rows[i].SpanCount > rows[j].SpanCount
	})

	var b strings.Builder
	if env != "" {
		fmt.Fprintf(&b, "Not: RED değerleri tüm ortamların toplamı; %q ortam daraltması bu karşılaştırmada uygulanmıyor.\n", env)
	}
	fmt.Fprintf(&b, "Servis ailesi (%d servis, son %d dakika), hata sayısına göre sıralı:\n", len(family), rangeS/60)
	const maxRows = 12
	winSec := float64(rangeS)
	for i, r := range rows {
		if i >= maxRows {
			fmt.Fprintf(&b, "… ve %d servis daha (hepsi daha az hatalı).\n", len(rows)-maxRows)
			break
		}
		rate := 0.0
		if winSec > 0 {
			rate = float64(r.SpanCount) / winSec
		}
		fmt.Fprintf(&b, "- %s: rate=%.1f req/s, error=%.2f%% (%d hata), p99=%.0fms\n",
			r.Name, rate, r.ErrorRate, r.ErrorCount, r.P99Ms)
	}
	if len(rows) == 0 {
		b.WriteString("Bu pencerede ailenin hiçbir servisinden span verisi yok.\n")
	}
	// v0.9.1229 — kanıt, grafik bloğundan ÖNCEKİ metin: ```chart``` fence'i
	// okumanın çıktısı değil, ondan türetilmiş bir görselleştirme talimatı.
	emitGuidedStepResult(emit, nFam, "family_context", b.String(), nil)
	// Worst-offender chart: the family question is about errors, so the
	// top row (errors-ranked) gets the live error_rate card.
	if len(rows) > 0 && rows[0].ErrorCount > 0 {
		b.WriteString(chartFence(guidedChartSpec{
			Title: rows[0].Name + " · error_rate", Service: rows[0].Name,
			Agg: "error_rate", RangeS: rangeS,
		}))
	}
	src := fmt.Sprintf("servis ailesi RED karşılaştırması — %d servis tek MV okuması (son %s)", len(family), fmtAgoTR(rangeS))
	return b.String(), src, nil
}

// guidedChartSpec is the deterministic chart block CoSRE emits inside a
// ```chart``` fence. json.Marshal (v0.9.187) — NOT fmt %q, which is
// strconv.Quote: a service/operation name with a control char produces
// \a / \x1b escapes that are invalid JSON, so the frontend's JSON.parse
// throws and the chart is silently dropped. Marshal keeps the block
// valid for any name.
type guidedChartSpec struct {
	Title     string `json:"title"`
	Service   string `json:"service"`
	Operation string `json:"operation,omitempty"`
	Agg       string `json:"agg"`
	RangeS    int64  `json:"rangeS"`
	// v0.9.1186 (Faz 4.4) — kırılım. TEK anahtar, çünkü sohbet balonundaki
	// ~560px'lik bir kartta iki anahtarlı kırılım seri sayısını çarpar ve
	// okunmaz bir spagetti üretir; kırılım tam da okunabilirlik için var.
	GroupBy string `json:"groupBy,omitempty"`
	// FromNs/ToNs — MUTLAK pencere (unix ns). Doluysa RangeS'i ezer.
	//
	// Neden yalnız SUNUCU doldurur (tool'da yok): küçük modele epoch
	// nanosaniye hesaplatmak, [[project-copilot-runtime]]'ın "prefetch+
	// narrate, aritmetik yaptırma" doktrininin tersi. Guided/insight
	// yolları pencereyi ZATEN biliyor (problemin/olayın penceresi) —
	// bilen taraf yazsın, modelden istemeyelim.
	FromNs int64 `json:"fromNs,omitempty"`
	ToNs   int64 `json:"toNs,omitempty"`
}

func chartFence(spec guidedChartSpec) string {
	j, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	return "\n```chart\n" + string(j) + "\n```\n"
}

// resolveGuidedOperation upgrades a service-health question to an
// operation-scoped one. Two signals, precision over recall:
//  1. the question TEXT contains a live operation name of the service
//     (bounded substring, len ≥ 6 to skip bare verbs) — the strongest,
//     "GET /orders/:id nasıl";
//  2. the operator is ON an operation page (ctxOperation from ?op=) AND
//     the message has an operation-signal word ("bu operasyonun durumu").
//
// Returns "" (→ service-level) when neither fires, so "checkout nasıl"
// stays a service answer. The op-name list is Redis-cached 60s.
func (s *Server) resolveGuidedOperation(ctx context.Context, service, raw, ctxOperation string) string {
	if service == "" {
		return ""
	}
	return pickGuidedOperation(normalizeGuidedMsg(raw), s.guidedOperationNames(ctx, service), ctxOperation)
}

// pickGuidedOperation is the PURE core of resolveGuidedOperation (table-
// tested in copilot_guided_test.go). msg is already normalized; ops is
// the live operation-name list; ctxOperation is the ?op= the operator is
// viewing. Longest text-matched op wins; else the ctx op iff a signal
// word is present AND it's a real op; else "".
func pickGuidedOperation(msg string, ops []string, ctxOperation string) string {
	best := ""
	for _, op := range ops {
		if len(op) < 6 || !opNameDistinctive(op) {
			continue
		}
		// indexBounded (not strings.Contains) so an op name never matches
		// INSIDE a word/service token — same word-boundary discipline the
		// service matcher uses ("mobile-bff" ∉ "mobile-bff-uat").
		if indexBounded(msg, normalizeGuidedMsg(op)) >= 0 && len(op) > len(best) {
			best = op
		}
	}
	if best != "" {
		return best
	}
	if ctxOperation != "" && hasThisOperationSignal(msg) {
		for _, op := range ops {
			if op == ctxOperation {
				return ctxOperation // guard a stale ?op= against the live list
			}
		}
	}
	return ""
}

// opNameDistinctive — the op name carries a structural separator
// (space/slash/dot/colon) that real APM span names have ("GET /orders",
// "SELECT users", "svc.Method/Call") but a bare business word
// ("checkout", "payment") does not. Free-text matching a BARE word
// collides with the service-identifying token that resolved the service
// (asking "checkout neden yavaş?" is a SERVICE question, not the span
// named "checkout"), so bare-word ops are reachable only via the ?op=
// context fallback, never text-match. (v0.9.184 review-fix.)
func opNameDistinctive(op string) bool {
	return strings.ContainsAny(op, " /.:")
}

// hasThisOperationSignal — the message DEICTICALLY points at one
// operation ("bu operasyon", "this endpoint"). Used ONLY for the ?op=
// context fallback. Demonstrative-scoped on purpose: a bare noun like
// "işlem"/"route" also appears in whole-service asks ("tüm işlemler
// nasıl?") which must NOT be narrowed to the viewed op. (v0.9.184
// review-fix — plural/bare-noun false-trigger.)
func hasThisOperationSignal(msg string) bool {
	for _, kw := range []string{
		"bu operasyon", "bu endpoint", "bu işlem", "bu islem",
		"bu uç nokta", "bu uc nokta", "bu servis çağrısı",
		"şu operasyon", "su operasyon", "this operation", "this endpoint",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// guidedOperationNames returns a service's live operation (span-name)
// list for entity extraction, Redis-cached 60s per service. Soft-fails
// to nil (→ resolveGuidedOperation returns "", service-level answer).
func (s *Server) guidedOperationNames(ctx context.Context, service string) []string {
	key := "copilot:guided:opnames:" + service
	if b, ok, _ := s.cache.Get(ctx, key); ok && len(b) > 0 {
		var names []string
		if json.Unmarshal(b, &names) == nil {
			return names
		}
	}
	names, _, err := s.store.ListOperationNames(ctx, service, "", 500, 0)
	if err != nil {
		return nil
	}
	if b, merr := json.Marshal(names); merr == nil {
		_ = s.cache.Set(ctx, key, b, 60*time.Second)
	}
	return names
}

// (c) "en yavaş/slowest traces [service]" → duration-ranked trace
// summaries from the trace_summary_5m fast path (Sort=duration,
// CountMode=skip — the same shape /traces uses). env (v0.8.398) rides
// TraceFilter.Env — the direct deploy_env conjunct; a non-empty env
// takes the raw-fallback path exactly like the /traces ?env= pick.
func (s *Server) guidedSlowTracesBundle(ctx context.Context, emit func(string, any), service, env string, from, to time.Time, rangeS int64) (string, string, error) {
	nSlow := emitGuidedStep(emit, "slow_traces", withEnvArg(`{"service":"`+service+`","sort":"duration"}`, env))
	rows, _, _, err := s.store.GetTraces(ctx, guidedTraceFilter(service, env, from, to))
	if err != nil {
		emitGuidedStepResult(emit, nSlow, "slow_traces", "", err)
		return "", "", err
	}
	src := fmt.Sprintf("duration'a göre sıralı trace listesi (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += fmt.Sprintf(", ortam: %s", env)
	}
	ev := renderSlowTracesEvidenceTR(rows, service, env, rangeS)
	emitGuidedStepResult(emit, nSlow, "slow_traces", ev, nil)
	return ev, src, nil
}

// guidedTraceBundle — v0.9.537. Yapıştırılan/bağlamdan gelen trace
// ID'sinin kanıt paketi. Montaj buildTraceExplainInput'un AYNISI
// (explain butonu, çekmece sohbeti ve guided yol tek paketi paylaşır —
// v0.9.482 deseni): span seçimi + ilişkili loglar + dürüstlük notu.
//
// Bulunamayan trace HATA DEĞİL dürüst kanıttır: yol serbest döngüye
// düşerse operatör yine cevapsız kalırdı; model "bulunamadı + olası
// nedenler" kanıtını anlatır (guided prompt uydurmayı yasaklar).
func (s *Server) guidedTraceBundle(ctx context.Context, emit func(string, any), traceID string) (string, string, error) {
	nTrace := emitGuidedStep(emit, "trace", `{"id":"`+traceID+`"}`)
	in, err := s.buildTraceExplainInput(ctx, traceID)
	if errors.Is(err, errExplainTraceNotFound) {
		ev := fmt.Sprintf("Trace %s Coremetry deposunda BULUNAMADI.\n"+
			"Olası nedenler: ID eksik/yanlış kopyalandı, trace retention "+
			"penceresinin dışında kaldı, ya da bu ortam yerine başka bir "+
			"ortamda üretildi. Kullanıcıya bunu söyle; span/istatistik UYDURMA.", traceID)
		// Bulunamamak HATA DEĞİL kanıttır (ok=true): çipin arkasında
		// aranan kimlik ve neden bulunamamış olabileceği duruyor.
		emitGuidedStepResult(emit, nTrace, "trace", ev, nil)
		return ev, "trace araması (bulunamadı)", nil
	}
	if err != nil {
		emitGuidedStepResult(emit, nTrace, "trace", "", err)
		return "", "", err
	}
	emitGuidedStepResult(emit, nTrace, "trace", in.User, nil)
	return in.User, fmt.Sprintf("trace %s — span kanıtı + ilişkili loglar", traceID), nil
}

// guidedSpanBundle — v0.9.548. Çıplak SPAN id'sinin kanıt paketi.
//
// Span tek başına anlamsızdır: neyin parçası olduğu bilinmeden "bu span
// neden yavaş" cevaplanamaz. O yüzden önce trace'i bulunur, sonra
// v0.9.537'nin trace paketi AYNEN kullanılır — ikinci bir montaj yok.
// Anlatıma hangi span'in sorulduğu ayrıca söylenir ki model waterfall
// içinde onu işaret edebilsin.
//
// Pencere çağırandan gelir (sohbetin aralığı, v0.9.529): span_id'de
// indeks yok, arama zaman-sınırlı bir tarama. Sınırsız pencere
// prod'da pahalıya kaçardı.
func (s *Server) guidedSpanBundle(ctx context.Context, emit func(string, any), spanID string, from, to time.Time) (string, string, error) {
	nSpan := emitGuidedStep(emit, "span", `{"id":"`+spanID+`"}`)
	traceID, err := s.store.FindTraceIDBySpan(ctx, spanID, from, to)
	if err != nil {
		emitGuidedStepResult(emit, nSpan, "span", "", err)
		return "", "", err
	}
	if traceID == "" {
		// Bulunamamak HATA DEĞİL dürüst kanıt. Hata döndürsek soru
		// serbest döngüye düşer ve operatör yine cevapsız kalır.
		ev := fmt.Sprintf("Span %s bu zaman penceresinde BULUNAMADI.\n"+
			"Olası nedenler: ID eksik/yanlış kopyalandı, span sohbetin zaman "+
			"aralığının dışında (aralığı genişletmeyi öner), ya da retention "+
			"penceresinin dışına düştü. Kullanıcıya bunu söyle; span/istatistik "+
			"UYDURMA.", spanID)
		emitGuidedStepResult(emit, nSpan, "span", ev, nil)
		return ev, "span araması (bulunamadı)", nil
	}
	in, terr := s.buildTraceExplainInput(ctx, traceID)
	if errors.Is(terr, errExplainTraceNotFound) {
		ev := fmt.Sprintf("Span %s trace %s'e ait ama trace'in span'leri okunamadı "+
			"(retention ya da kısmi yazım). Kullanıcıya bunu söyle; UYDURMA.", spanID, traceID)
		emitGuidedStepResult(emit, nSpan, "span", ev, nil)
		return ev, "span → trace (span'ler okunamadı)", nil
	}
	if terr != nil {
		emitGuidedStepResult(emit, nSpan, "span", "", terr)
		return "", "", terr
	}
	// Hangi span'in sorulduğu anlatımın ÖNÜNE yazılıyor: model
	// waterfall içinde onu işaret edebilsin.
	ev := fmt.Sprintf("SORULAN SPAN: %s (trace %s içinde)\n\n%s", spanID, traceID, in.User)
	emitGuidedStepResult(emit, nSpan, "span", ev, nil)
	return ev, fmt.Sprintf("span %s → trace %s kanıtı", spanID, traceID), nil
}

// guidedRequestIDBundle — v0.9.1142. YAPILANDIRILMIŞ kurumsal istek
// numarasının kanıt paketi.
//
// Zincirin tamamı: kimlik → gömülü tarih+saat (yerel banka saati) →
// o pencerede TEK log araması → eşleşen kaydın trace_id'si →
// v0.9.537'nin trace kanıt paketi (buildTraceExplainInput). Montaj
// guidedSpanBundle'ın AYNISI, çünkü ikisi de aynı şeyi yapıyor: elde
// olan kimliği trace'e çevirip mevcut anlatıyı beslemek.
//
// PENCERE NEDEN SOHBETİN ARALIĞI DEĞİL: kimlik kendi damgasını taşıyor.
// Sohbetin 30dk'lık varsayılanı dün öğlen üretilmiş bir kimliği asla
// bulamazdı; ±10dk'lık dar pencere ise hem doğru hem ucuz (ES 10B
// doc/gün — pencere maliyetin TEK sınırı). Tz belirsizliğini pencereyi
// genişleterek çözmüyoruz: locu doğru kullanıyoruz (reqid.Location).
//
// Bulunamamak HATA DEĞİL dürüst kanıttır: cevap ARANAN PENCEREYİ ve
// ARANAN YERİ söyler, halüsinasyon yasaktır (guided prompt zaten
// uydurmayı yasaklıyor, kanıt da açıkça talimat veriyor).
func (s *Server) guidedRequestIDBundle(ctx context.Context, emit func(string, any), route *guidedRoute) (string, string, error) {
	loc := reqid.Location(s.reqidTZSetting(ctx))
	id, ok := reqid.Parse(route.RequestID, loc)
	if !ok {
		// Router aynı ayrıştırıcıyla yönlendirdiği için buraya normalde
		// düşülmez; düşülürse kanıt dürüst kalır (panik/uydurma yok).
		return fmt.Sprintf("Request ID %s yapılandırılmış biçime uymadı, gömülü zaman okunamadı. "+
			"Kullanıcıya bunu söyle ve kimliği log arayüzünde aramasını öner; veri UYDURMA.",
			route.RequestID), "request_id (biçim çözülemedi)", nil
	}
	from, to := id.Window()
	route.ReqWindowFromMs = from.UnixMilli()
	route.ReqWindowToMs = to.UnixMilli()

	nParse := emitGuidedStep(emit, "parse_request_id", fmt.Sprintf(`{"time":%q,"tz":%q}`, reqid.FmtLocal(id.TS), id.TS.Location().String()))
	nSearch := emitGuidedStep(emit, "search_logs", fmt.Sprintf(`{"request_id":%q,"from":%q,"to":%q}`,
		id.Raw, reqid.FmtLocal(from), reqid.FmtLocal(to)))

	backend := "log"
	if s.logs != nil {
		backend = s.logs.Backend()
	}
	// Kimliğin kendisi kanıtın BAŞINDA: model hangi işlemi anlattığını
	// (kanal, müşteri, saat) söyleyebilsin.
	//
	// v0.9.1229 — bu blok AYRIŞTIRMA adımının kanıtı, o yüzden log
	// aramasından ÖNCE kuruluyor: arama düşse bile operatör kimlikten
	// nelerin okunduğunu (ve hangi pencerenin tarandığını) görebilmeli.
	head := fmt.Sprintf("SORULAN REQUEST ID: %s\n"+
		"Kimlikten okunan: işlem zamanı %s · fonksiyon %s · kanal %s · alt kod %s · müşteri no %s\n"+
		"Aranan pencere: %s → %s (kimliğin damgası ±10dk), aranan yer: LOG kayıtları (%s backend).\n",
		id.Raw, reqid.FmtLocal(id.TS), id.FuncCode, id.Channel, id.SubCode, id.CustomerNo,
		reqid.FmtLocal(from), reqid.FmtLocal(to), backend)
	emitGuidedStepResult(emit, nParse, "parse_request_id", head, nil)

	res, err := reqid.Resolve(ctx, s.logs, id)
	if err != nil {
		emitGuidedStepResult(emit, nSearch, "search_logs", "", err)
		return "", "", err
	}
	if res.Partial {
		head += "UYARI: log backend'i KISMİ cevap döndürdü (soft timeout / eksik shard) — " +
			"aşağıdaki sonuç gerçek cevabın alt kümesi olabilir.\n"
	}

	if res.TraceID == "" {
		body := fmt.Sprintf(
			"\nBu pencerede bu kimliği taşıyan, trace bağlamı olan bir log kaydı BULUNAMADI "+
				"(eşleşen log satırı: %d).\n"+
				"Olası nedenler: kimlik eksik/yanlış kopyalandı; kimliği loglayan bileşen trace "+
				"üretmiyor; kimlik log GÖVDESİNDE değil yapısal bir alanda duruyor (serbest metin "+
				"araması gövdeyi tarar); ya da kayıtlar retention penceresinin dışına düştü.\n"+
				"Kullanıcıya BUNU ve aranan pencereyi söyle; trace/span/süre UYDURMA. "+
				"Varsa dış log köprüsü linkini kullanmasını öner.", res.MatchedLogs)
		emitGuidedStepResult(emit, nSearch, "search_logs", body, nil)
		return head + body, fmt.Sprintf("request_id → log araması (%s → %s, bulunamadı)",
			reqid.FmtLocal(from), reqid.FmtLocal(to)), nil
	}

	route.TraceID = res.TraceID
	// Servis rotanın ÇIKTISI (TeamServices emsali): takip çipleri ve derin
	// linkler böylece somut bir servise oturuyor — operatör "peki bu
	// servisin hata logları?" diye devam edebilsin.
	route.Service = res.Service
	in, terr := s.buildTraceExplainInput(ctx, res.TraceID)
	if errors.Is(terr, errExplainTraceNotFound) {
		body := fmt.Sprintf("\nKimlik trace %s ile eşleşti (log servisi: %s) ama trace'in "+
			"span'leri okunamadı (retention ya da kısmi yazım). Kullanıcıya bunu söyle; UYDURMA.",
			res.TraceID, res.Service)
		emitGuidedStepResult(emit, nSearch, "search_logs", body, nil)
		return head + body, "request_id → trace (span'ler okunamadı)", nil
	}
	if terr != nil {
		emitGuidedStepResult(emit, nSearch, "search_logs", "", terr)
		return "", "", terr
	}
	match := fmt.Sprintf("\nEşleşen log kaydı: servis %s · trace %s · span %s (eşleşen satır: %d).\n",
		res.Service, res.TraceID, res.SpanID, res.MatchedLogs)
	ev := head + match
	if res.DistinctTraces > 1 {
		// Aynı kimlik yeniden deneme / asenkron devam yüzünden birden çok
		// trace'e dokunmuş olabilir; tek trace gibi anlatmak yanlış olurdu.
		note := fmt.Sprintf("NOT: bu pencerede kimliği taşıyan %d FARKLI trace var; "+
			"aşağıdaki en yenisi. Bunu söyle.\n", res.DistinctTraces)
		match += note
		ev += note
	}
	// Arama adımının kanıtı EŞLEŞMEnin kendisi; altındaki trace paketi
	// ayrı bir okumadan geliyor (kendi çipi yok, kanıtı da burada değil).
	emitGuidedStepResult(emit, nSearch, "search_logs", match, nil)
	ev += "\n" + in.User
	return ev, fmt.Sprintf("request_id → log araması → trace %s kanıtı", res.TraceID), nil
}

// guidedDeployRef unifies the two deploy reads (global
// RecentDeployEntry vs per-service Deploy) for the renderer.
type guidedDeployRef struct {
	Service string
	Version string
	TimeNs  int64
}

// (d) "deploy etkisi/son deploy" → recent rollouts + before/after RED
// impact (ComputeDeployImpact, single bounded CH pass per deploy,
// capped at 3). Deploy markers carry NO env dimension (env-separation
// Phase 4 pending) — an env ask (v0.8.398) is answered honestly via
// guidedEnvlessNoteTR instead of silently ignored; the step echo also
// omits env because the filter was not applied.
// anchorTo (v0.10.64) — pencerenin SONU. Sıfır = şimdi.
//
// ⚠ v0.10.33 çıpayı guided kademesine getirdi ama BU fonksiyona hiç
// geçirmedi: pencere `time.Now()` ile kuruluyordu. Operatör dün geceye
// zoom yapıp "son deploy neydi" diye sorduğunda BUGÜNÜN deploy'ları
// dönüyor ve cevap DÜNÜN penceresi diye etiketleniyordu — v0.10.50'de
// serbest döngüde düzeltilen kusurun guided'daki ikizi.
func (s *Server) guidedDeployBundle(ctx context.Context, emit func(string, any), service, env string, rangeS int64, anchorTo time.Time) (string, string, error) {
	// Deploy questions imply a wider horizon than the default 30m chat
	// window — "son deploy" is rarely in the last half hour. Floor the
	// lookback at 6h, cap 24h (GetRecentDeploys scales its CH timeout
	// with the window).
	lookback := time.Duration(rangeS) * time.Second
	if lookback < 6*time.Hour {
		lookback = 6 * time.Hour
	}
	var refs []guidedDeployRef
	nRecent := emitGuidedStep(emit, "recent_deploys", `{"service":"`+service+`"}`)
	if service != "" {
		now := anchorTo
		if now.IsZero() {
			now = time.Now()
		}
		deps, err := s.store.GetServiceDeploys(ctx, service, now.Add(-lookback), now)
		if err != nil {
			emitGuidedStepResult(emit, nRecent, "recent_deploys", "", err)
			return "", "", err
		}
		for _, d := range deps {
			refs = append(refs, guidedDeployRef{Service: service, Version: d.Version, TimeNs: d.TimeUnixNs})
		}
	} else {
		deps, err := s.store.GetRecentDeploys(ctx, lookback, 10)
		if err != nil {
			emitGuidedStepResult(emit, nRecent, "recent_deploys", "", err)
			return "", "", err
		}
		for _, d := range deps {
			refs = append(refs, guidedDeployRef{Service: d.Service, Version: d.Version, TimeNs: d.FirstSeenNs})
		}
	}
	// Newest first, impact for the top 3 only (bounded CH cost).
	sort.Slice(refs, func(i, j int) bool { return refs[i].TimeNs > refs[j].TimeNs })
	if len(refs) > 5 {
		refs = refs[:5]
	}
	// v0.9.1229 — işaretçi okumasının kanıtı: etki HESAPLANMADAN önceki
	// liste, aynı renderer'la. Her etki adımı kendi satırını ayrıca
	// gösteriyor, yani hangi çipin ne getirdiği ayrışıyor.
	emitGuidedStepResult(emit, nRecent, "recent_deploys",
		renderDeployEvidenceTR(refs, nil, lookback, evidenceAsOf(anchorTo)), nil)
	impacts := make([]*chstore.DeployImpact, len(refs))
	for i, ref := range refs {
		if i >= 3 {
			break
		}
		nImp := emitGuidedStep(emit, "deploy_impact", `{"service":"`+ref.Service+`","version":"`+ref.Version+`"}`)
		imp, ierr := s.store.ComputeDeployImpact(ctx, ref.Service, ref.Version, ref.TimeNs, 600)
		if ierr == nil {
			impacts[i] = imp
		}
		emitGuidedStepResult(emit, nImp, "deploy_impact",
			renderDeployEvidenceTR(refs[i:i+1], impacts[i:i+1], lookback, evidenceAsOf(anchorTo)), ierr)
	}
	src := "deploy işaretçileri + öncesi/sonrası RED etkisi (±10dk pencere)"
	if env != "" {
		src += "; ortam filtresi uygulanamadı (deploy verisi ortam boyutu taşımıyor)"
	}
	return guidedEnvlessNoteTR("deploy işaretçileri", env) +
		renderDeployEvidenceTR(refs, impacts, lookback, evidenceAsOf(anchorTo)), src, nil
}

// (e) "log hataları/log errors [service]" → severity histogram totals
// + the curated failure-pattern detector hits (both reads carry the
// existing ES/CH cost guards; the pattern window snaps to the same
// rungs the /anomalies endpoint uses). Logs carry NO env dimension
// (env-separation Phase 4 pending) — an env ask (v0.8.398) is
// answered honestly via guidedEnvlessNoteTR instead of silently
// ignored; the step echo omits env because the filter was not applied.
func (s *Server) guidedLogErrorsBundle(ctx context.Context, emit func(string, any), service, env string, from, to time.Time, rangeS int64) (string, string, error) {
	nHist := emitGuidedStep(emit, "log_severity_histogram", `{"service":"`+service+`"}`)
	bucketSec := int(rangeS / 30)
	if bucketSec < 60 {
		bucketSec = 60
	}
	series, err := s.logs.Histogram(ctx, logstore.Filter{Service: service, From: from, To: to}, bucketSec, "severity")
	if err != nil {
		emitGuidedStepResult(emit, nHist, "log_severity_histogram", "", err)
		return "", "", err
	}
	nPat := emitGuidedStep(emit, "log_patterns", "")
	pats, perr := anomaly.DetectLogPatterns(ctx, s.logs, snapAnomalyWindow(time.Duration(rangeS)*time.Second))
	if perr != nil {
		pats = nil // patterns are additive evidence — soft-fail
	}
	if service != "" {
		kept := pats[:0]
		for _, p := range pats {
			if p.Service == service {
				kept = append(kept, p)
				continue
			}
			for _, ts := range p.TopServices {
				if ts.Service == service {
					kept = append(kept, p)
					break
				}
			}
		}
		pats = kept
	}
	sort.Slice(pats, func(i, j int) bool { return pats[i].CurrentCount > pats[j].CurrentCount })
	if len(pats) > 5 {
		pats = pats[:5]
	}
	src := fmt.Sprintf("log severity histogramı + hata pattern tespitleri (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += "; ortam filtresi uygulanamadı (log verisi ortam boyutu taşımıyor)"
	}
	ev := guidedEnvlessNoteTR("log verisi", env) +
		renderLogErrorsEvidenceTR(series, pats, service, rangeS)
	// v0.9.1229 — iki okuma TEK bloğa akıyor (renderer severity dağılımını
	// ve pattern eşleşmelerini birlikte kuruyor), o yüzden blok iki çipe de
	// veriliyor: yarısını uydurmaktansa aynı kanıtı iki kez göstermek.
	// Pattern okuması yumuşak-düşer (pats=nil) — çip bunu ok=false ile SÖYLER.
	emitGuidedStepResult(emit, nHist, "log_severity_histogram", ev, nil)
	emitGuidedStepResult(emit, nPat, "log_patterns", ev, perr)
	return ev, src, nil
}

// ─── Evidence renderers (pure, table-tested) ────────────────────────

const guidedMaxLines = 10

// guidedScopeTR renders the "(servis: X, ortam: Y)" scope fragment
// shared by the problems + slow-traces evidence headers (v0.8.398).
// Pure — table-tested. Empty parts drop out; both empty = "".
func guidedScopeTR(service, env string) string {
	var parts []string
	if service != "" {
		parts = append(parts, "servis: "+service)
	}
	if env != "" {
		parts = append(parts, "ortam: "+env)
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

func renderProblemsEvidenceTR(probs []chstore.Problem, service, env string, now time.Time, total problemsTotal) string {
	scope := ""
	if sc := guidedScopeTR(service, env); sc != "" {
		scope = " (" + sc[2:] + ")" // strip the leading ", " — header form
	}
	if len(probs) == 0 {
		return "Açık problem yok" + scope + ".\n"
	}
	var crit, warn, info int
	for _, p := range probs {
		switch p.Severity {
		case "critical":
			crit++
		case "warning":
			warn++
		default:
			info++
		}
	}
	var b strings.Builder
	// v0.10.21 — TOPLAM ARTIK len(probs) DEĞİL. `probs` bir SQL LIMIT'inin
	// çıktısı; uzunluğunu "toplam" diye basmak, 47 problemli bir serviste
	// modele "toplam 10" vermek demekti (guided_problem_total.go).
	// Şiddet sayımları GÖSTERİLEN satırlar üzerinden. Liste kırpılmamışsa
	// bu zaten tam sayımdır ve fazladan açıklama gürültü olur; kırpıldıysa
	// "toplam 47 (kritik 1, warning 1, info 0)" kendi içinde çelişir, o
	// yüzden ifşa ŞART.
	fmt.Fprintf(&b, "Açık problemler%s: %s (kritik %d, warning %d, info %d%s)\n",
		scope, problemsCountPhraseTR(total, len(probs)), crit, warn, info,
		problemsBreakdownCaveatTR(total, len(probs)))
	shown := len(probs)
	if shown > guidedMaxLines {
		shown = guidedMaxLines
	}
	for i, p := range probs {
		if i >= guidedMaxLines {
			break
		}
		name := p.RuleName
		if name == "" {
			name = p.Metric
		} else if p.Metric != "" && p.Metric != p.RuleName {
			// v0.10.405 (CoSRE denetimi P4) — kural adı varken METRİK adı
			// düşüyordu; model "değer 4.20"nin neyin değeri olduğunu bilmiyordu.
			name += " [" + p.Metric + "]"
		}
		unit := problemMetricUnitTR(p.Metric)
		fmt.Fprintf(&b, "- [%s] %s — %s (%s, %s önce): değer %.2f%s / eşik %.2f%s",
			p.Priority, p.Service, name, p.Severity,
			fmtAgoTR(now.UnixNano()/1e9-p.StartedAt/1e9), p.Value, unit, p.Threshold, unit)
		if p.RootCause != nil && p.RootCause.TopSuspect != "" {
			fmt.Fprintf(&b, " | kök-neden şüphelisi: %s (güven %.2f)",
				p.RootCause.TopSuspect, p.RootCause.Confidence)
		}
		if p.PriorityReason != "" {
			fmt.Fprintf(&b, " | öncelik nedeni: %s", p.PriorityReason)
		}
		b.WriteString("\n")
	}
	// v0.10.21 — KIRPMA İFŞASI. Eski dal `i >= guidedMaxLines` idi ve
	// limit=10 rotalarında YAPISAL OLARAK ULAŞILAMAZDI (döngü 0..9, indeks
	// hiç 10'a çıkmaz), yani model yanlış toplamı üstüne hiçbir uyarı
	// almadan alıyordu. İfşa artık GÖSTERİLEN ile TOPLAM farkından çıkıyor.
	b.WriteString(problemsTruncationNoteTR(total, shown, len(probs)))
	return b.String()
}

func renderSlowTracesEvidenceTR(rows []chstore.TraceRow, service, env string, rangeS int64) string {
	scope := guidedScopeTR(service, env)
	if len(rows) == 0 {
		return fmt.Sprintf("En yavaş trace'ler (son %s%s): bu pencerede trace bulunamadı.\n", fmtAgoTR(rangeS), scope)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "En yavaş trace'ler (son %s%s, duration'a göre):\n", fmtAgoTR(rangeS), scope)
	for _, r := range rows {
		flag := ""
		if r.HasError {
			flag = ", HATA"
		}
		fmt.Fprintf(&b, "- %.0fms — %s / %s (%d span%s) trace=%s\n",
			r.DurationMs, r.ServiceName, r.RootName, r.SpanCount, flag, r.TraceID)
	}
	return b.String()
}

func renderDeployEvidenceTR(refs []guidedDeployRef, impacts []*chstore.DeployImpact, lookback time.Duration, now time.Time) string {
	if len(refs) == 0 {
		return fmt.Sprintf("Son %s içinde deploy görülmedi.\n", fmtAgoTR(int64(lookback.Seconds())))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Son deploylar (son %s):\n", fmtAgoTR(int64(lookback.Seconds())))
	for i, ref := range refs {
		fmt.Fprintf(&b, "- %s %s (%s önce)", ref.Service, ref.Version,
			fmtAgoTR(now.UnixNano()/1e9-ref.TimeNs/1e9))
		if i < len(impacts) && impacts[i] != nil {
			imp := impacts[i]
			fmt.Fprintf(&b, " | etki (±10dk): p99 %.0fms→%.0fms (%%%+.1f), error %%%.2f→%%%.2f, rps %.1f→%.1f",
				imp.Before.P99Ms, imp.After.P99Ms, imp.P99DeltaPct,
				imp.Before.ErrorRate*100, imp.After.ErrorRate*100,
				imp.Before.RPS, imp.After.RPS)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// guidedSeverityOrder pins the histogram render order worst-first so
// the model reads FATAL/ERROR before the INFO noise.
var guidedSeverityOrder = []string{"FATAL", "ERROR", "WARN", "INFO", "DEBUG", "TRACE"}

func renderLogErrorsEvidenceTR(series []logstore.LogSeries, pats []anomaly.LogPatternAnomaly, service string, rangeS int64) string {
	scope := ""
	if service != "" {
		scope = fmt.Sprintf(", servis: %s", service)
	}
	totals := map[string]int64{}
	var grand int64
	for _, s := range series {
		for _, p := range s.Points {
			totals[s.Name] += p.V
			grand += p.V
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Log severity dağılımı (son %s%s): ", fmtAgoTR(rangeS), scope)
	if grand == 0 {
		b.WriteString("bu pencerede log yok.\n")
	} else {
		var parts []string
		seen := map[string]bool{}
		for _, name := range guidedSeverityOrder {
			if v, ok := totals[name]; ok && v > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", name, v))
				seen[name] = true
			}
		}
		// Non-canonical band names (backend-specific) trail, sorted for
		// deterministic output.
		var rest []string
		for name, v := range totals {
			if !seen[name] && v > 0 {
				rest = append(rest, fmt.Sprintf("%s %d", name, v))
			}
		}
		sort.Strings(rest)
		parts = append(parts, rest...)
		b.WriteString(strings.Join(parts, ", "))
		fmt.Fprintf(&b, " (toplam %d)\n", grand)
	}
	if len(pats) > 0 {
		b.WriteString("Öne çıkan hata pattern'leri:\n")
		for _, p := range pats {
			fmt.Fprintf(&b, "- %s ×%d (%s, %s", p.Pattern, p.CurrentCount, p.Service, p.Kind)
			if p.BaselineCount > 0 {
				fmt.Fprintf(&b, ", baseline %d", p.BaselineCount)
			}
			b.WriteString(")\n")
		}
	} else {
		b.WriteString("Bilinen hata pattern'lerinde eşleşme yok.\n")
	}
	return b.String()
}

// containsPhrase — çok kelimeli bir kalıbı KELİME SINIRINDA arar
// (v0.9.570).
//
// Sınırsız strings.Contains, Türkçede sessiz bir yanlış-yönlendirme
// üretiyordu ve çarpışma tam da en doğal SRE sorusundaydı:
//
//	"bu trace neden yavaş?" ⊃ "en yavaş"   →  ned|EN YAVAŞ
//	"neden uzun sürdü"      ⊃ "en uzun"    →  ned|EN UZUN sürdü
//
// Yani operatör /trace sayfasında ekrandaki trace'i sorarken router
// onu "en yavaş trace'leri listele" niyetine yönlendiriyor ve FİLO
// GENELİ bir liste dönüyordu. Hata görünmez: cevap makul bir cevap,
// yalnız SORULAN soruya ait değil.
//
// Sınır tanımı: kalıbın YALNIZ BAŞINDA harf/rakam olmamalı. Son ek
// serbest — ve bu bilinçli bir asimetri:
//
//   - Bug BAŞTAYDI: "ned|en yavaş" kalıbı bir kelimenin ortasında
//     yakalıyordu.
//   - SONDA ek dayatmak yanlış olurdu: "slow trace" → "slow traceS"
//     (İngilizce çoğul) ve "en yavaş" → "en yavaşI", "yavaşLAR"
//     (Türkçe eklemeli yapı). İki uçta da sınır isteyen ilk taslağım
//     mevcut testleri düşürdü ve haklı olarak düşürdü.
//
// Türkçe harfler unicode.IsLetter ile doğru ele alınır — ASCII testi
// ş/ğ/ı'yı sınır sanardı ve kalıbın kendisini bölerdi.
func containsPhrase(msg, phrase string) bool {
	if phrase == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(msg[i:], phrase)
		if j < 0 {
			return false
		}
		start := i + j
		if !alnumBefore(msg, start) {
			return true
		}
		// Kayarak devam et: aynı kalıp ilerde SINIRDA geçebilir.
		i = start + 1
		if i >= len(msg) {
			return false
		}
	}
}

func alnumBefore(s string, idx int) bool {
	if idx <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:idx])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// hasDemonstrativeTrace — mesaj EKRANDAKİ trace'i mi işaret ediyor?
//
// "bu trace", "şu trace", "this trace" gibi işaret zamirli kullanımlar
// bir LİSTE değil, TEK bir şey sorar. Bu ayrım olmadan "bu trace neden
// yavaş?" sorusu "en yavaş trace'leri listele" niyetiyle çarpışıyor ve
// operatör ekranındaki trace yerine filo geneli bir liste görüyordu.
func hasDemonstrativeTrace(msg string) bool {
	for _, p := range []string{"bu trace", "şu trace", "this trace", "bu iz"} {
		if containsPhrase(msg, p) {
			return true
		}
	}
	return false
}

// guidedNarrationPrompt — intent'e göre anlatım sistem prompt'u (saf,
// tablo testli; v0.9.1131, operatör-raporlu).
//
// Rapor: "✨ Explain trace ile chat'e 'bu trace'i açıkla' demek farklı
// davranıyor; Explain daha iyi." Kanıt paketi v0.9.537'den beri AYNI
// (guidedTraceBundle → buildTraceExplainInput), fark yalnız
// anlatıcıdaydı: guided yol HER intent'i jenerik sohbet prompt'uyla
// anlatıyordu; ✨ Explain ise v0.9.842'nin 3-bölümlü derin trace
// prompt'unu kullanıyor. Trace/span intent'leri artık explain'in
// prompt'unu birebir kullanır — iki kapı, aynı kanıt, AYNI anlatı.
// Diğer intent'ler sohbet formatında kalır (tablo+takip akışına o
// uyuyor).
func guidedNarrationPrompt(intent guidedIntent) string {
	switch intent {
	// v0.9.1142 — request_id rotasının kanıtı da (çözüldüğünde) trace
	// paketidir; anlatıcı aynı olmalı, yoksa aynı kanıt iki kapıdan iki
	// farklı derinlikte anlatılırdı (v0.9.1131'in tam olarak bu kazası).
	case guidedTraceByID, guidedSpanByID, guidedRequestID:
		return copilot.SystemPromptTrace()
	case guidedSelfMeta:
		return copilot.SystemPromptSelfMeta()
	}
	return copilot.SystemPromptGuidedChat()
}

// isSelfMetaQuestion — soru asistanın KENDİSİ hakkında mı?
//
// v0.10.13, operatör bildirimi: "sen hangi modelsin" sorusu hiçbir
// intent'e uymuyor, RAG doküman yoluna düşüyor ve orada "Yüklü
// dokümanlarda bu bilgi yok" cevabı alıyordu — oysa cevap
// yapılandırmada duruyor.
//
// Kapı BİRLEŞİM, tek kelime değil (guidedQueueLag emsali): tek başına
// "model" telemetri sorularında da geçebilir ("model servisinin p99'u"),
// tek başına "sen" ise her cümlede olabilir. İkisi birlikte niyeti
// belirliyor. `kimsin`/`nesin` gibi tek kelimeler kendi başına yeterli,
// çünkü onların telemetri karşılığı yok.
func isSelfMetaQuestion(toks []string) bool {
	// Tek başına yeterli olanlar: bunlar bir servisi ya da metriği
	// adlandıramaz.
	if tokenHasPrefix(toks, "kimsin", "nesin", "kimsiniz") {
		return true
	}
	// İngilizce kalıp İFADE olarak aranıyor, token birleşimi olarak
	// DEĞİL: "who/what" + "you" birleşimi "what services do you have"
	// gibi gerçek telemetri sorularını da yakalardı ve düzeltme yeni bir
	// kusur üretirdi. Dar kalıp, dar eşleşme.
	joined := strings.Join(toks, " ")
	if strings.Contains(joined, "who are you") || strings.Contains(joined, "what are you") {
		return true
	}
	// Birleşim: özne (sen/siz/hangi/which/who) + kimlik (model/asistan/...)
	subject := tokenHasPrefix(toks, "sen", "senin", "siz", "hangi", "which", "who", "what")
	identity := tokenHasPrefix(toks, "model", "modeli", "modelsin", "asistan", "assistant", "llm", "yapay")
	return subject && identity
}

// guidedSelfMetaBundle — deterministik cevap; ClickHouse'a hiç gitmiyor.
//
// Kanıt YAPILANDIRMADAN geliyor (ActiveModel), tahminden değil. Bu
// önemli: küçük modeller "hangi modelsin" sorusuna kendi adı yerine
// tanınmış bir markanın adını söylemeye meyilli. Kanıtı birebir vermek +
// anlatıcıya "harfi harfine aktar" demek, o uydurmanın önündeki tek
// gerçek engel.
//
// Model adı sır DEĞİL (operatör Helm values'ına kendi yazıyor);
// ActiveModel zaten yalnız modeli döndürüyor, baseURL/apiKey'i değil.
func (s *Server) guidedSelfMetaBundle(emit func(string, any)) (string, string, error) {
	emit("Yapılandırma okunuyor", nil)
	model := ""
	if s.copilot != nil {
		model = s.copilot.ActiveModel()
	}
	if model == "" {
		return "AI asistanı bu kurulumda YAPILANDIRILMAMIŞ (model seçilmemiş " +
			"ya da sağlayıcı kapalı). Ayarlar → AI bölümünden yapılandırılır.", "", nil
	}
	return "Bu kurulumda çalışan LLM modelinin adı TAM OLARAK şudur: " + model +
		"\n\nAsistanın adı CoSRE'dir ve Coremetry'nin içine gömülüdür; " +
		"telemetriyi (trace, log, metrik, problem) okuyup anlatır.", "", nil
}

// evidenceAsOf — "kaç saat önce" hesabının DAYANAĞI (v0.10.65).
//
// Çıpalı bir pencerede yaşları GERÇEK şimdiye göre yazmak, doğru veriye
// yanlış etiket koymaktır: dün gece 03:00'teki bir deploy "2 saat önce"
// değil, ÇAPAYA göre öyle. Aynı şey problem yaşları için de geçerli —
// "4 saattir açık" cümlesi çıpalı pencerede saatlerce kayıyordu.
//
// Tek yardımcı, çünkü kural tek: kanıtın yaşı, kanıtın PENCERESİNE göre.
// Çıpa yoksa davranış aynen eskisi.
func evidenceAsOf(anchorTo time.Time) time.Time {
	if anchorTo.IsZero() {
		return time.Now()
	}
	return anchorTo
}

// problemMetricUnitTR — v0.10.405 (CoSRE denetimi P4): problem kanıt
// satırındaki sayının BİRİMİ. error_rate yüzde (anomaly.go "error_rate(%)"),
// gecikme aileleri ms, throughput req/s; tanınmayan metrik birimsiz kalır
// (tahmin yok — yanlış birim birimsizden kötü).
func problemMetricUnitTR(metric string) string {
	m := strings.ToLower(metric)
	switch {
	case strings.Contains(m, "error_rate") || strings.HasSuffix(m, "_pct") || strings.Contains(m, "percent"):
		return "%"
	case strings.Contains(m, "p50") || strings.Contains(m, "p95") || strings.Contains(m, "p99") ||
		strings.Contains(m, "latency") || strings.Contains(m, "duration"):
		return " ms"
	case m == "rps" || strings.Contains(m, "throughput") || strings.Contains(m, "req_per_s"):
		return " req/s"
	}
	return ""
}

// serviceIntentFor — v0.10.429 (D1): mesaj servis-kapsamlı bir kılavuz
// niyet taşıyor mu ve hangisi (router switch'inin servisli dallarıyla AYNI
// öncelik). guidedNone ⇒ servis sorusu değil, aday aranmaz.
func serviceIntentFor(msg string, toks []string) guidedIntent {
	switch {
	case hasWhySignal(toks):
		return guidedRootCause
	case hasSlowTraceSignal(msg):
		return guidedSlowTraces
	case hasDeploySignal(toks):
		return guidedDeployImpact
	case hasLogSignal(toks) && hasErrorSignal(toks):
		return guidedLogErrors
	case hasPodSignal(toks):
		return guidedPodHealth
	case hasMessagingSignal(toks):
		return guidedMessagingHealth
	case hasDBSignal(toks):
		return guidedDBHealth
	case hasHealthSignal(toks) || hasErrorSignal(toks) || hasProblemSignal(toks):
		return guidedServiceHealth
	}
	return guidedNone
}

// guidedAskServiceEvidence — v0.10.429 (D1): guidedAskTeamEvidence'in
// servis ikizi. Adaylar rotada yoksa (sınıflandırıcı "servis gerekli ama
// yok" dedi) açık problemi olan servisler sunulur; o da yoksa katalogun
// ilk adları. Çipler guidedSuggestions'ta tam kılavuz cümle olur.
func (s *Server) guidedAskServiceEvidence(ctx context.Context, route *guidedRoute, question string) (string, string, error) {
	src := "canlı servis kataloğu (ad eşleşmedi)"
	if len(route.ServiceOptions) == 0 {
		src = "açık problemli servisler"
		seen := map[string]bool{}
		if probs, err := s.store.ListProblems(ctx, chstore.ProblemFilter{Status: "open"}); err == nil {
			for _, p := range probs {
				if p.Service == "" || seen[p.Service] {
					continue
				}
				seen[p.Service] = true
				route.ServiceOptions = append(route.ServiceOptions, p.Service)
				if len(route.ServiceOptions) >= guidedServiceAskMax {
					break
				}
			}
		}
		if len(route.ServiceOptions) == 0 {
			src = "servis kataloğu"
			names := s.guidedServiceNames(ctx)
			if len(names) > guidedServiceAskMax {
				names = names[:guidedServiceAskMax]
			}
			route.ServiceOptions = append(route.ServiceOptions, names...)
		}
	}
	if route.AskIntent == guidedNone {
		route.AskIntent = guidedServiceHealth
	}
	var b strings.Builder
	b.WriteString("Operatörün sorusu bir SERVİS gerektiriyor ama adı çözülemedi ya da birden çok servise oturuyor.\n")
	fmt.Fprintf(&b, "Soru: %q\n", strings.TrimSpace(question))
	if len(route.ServiceOptions) == 0 {
		b.WriteString("Katalogda aday yok. KULLANICIYA SÖYLE: servis adını yazsın (Servisler sayfasından kopyalayabilir).\n")
		return b.String(), src, nil
	}
	fmt.Fprintf(&b, "KULLANICIYA SOR: şu servislerden hangisini kastettin? (%d aday): %s\n", len(route.ServiceOptions), strings.Join(route.ServiceOptions, ", "))
	b.WriteString("Bu adlar cevabın altında ÇİP olarak duruyor — tıklaması yeter; listede yoksa adı yazabileceğini de söyle.\n")
	b.WriteString("KURAL: servis adı UYDURMA, yalnız yukarıdaki listeyi say; veri anlatma (henüz seçilmedi).\n")
	return b.String(), src, nil
}

// guidedWindowCompareBundle — v0.10.437 (D6): aynı servisin iki mutlak
// penceredeki RED'i (buildServiceContext → service_summary_5m). Konum
// rotadaki pencerelerin kendi konumu (tarayıcı ofseti).
func (s *Server) guidedWindowCompareBundle(ctx context.Context, emit func(string, any), route *guidedRoute) (string, string, error) {
	if len(route.Windows) != 2 || route.Service == "" {
		return "", "", fmt.Errorf("window_compare: iki pencere ve servis gerekli")
	}
	loc := route.Windows[0].From.Location()
	reds := make([]aiRED, 0, 2)
	for i, w := range route.Windows {
		// v0.10.444 — yalnız RED (service_summary_5m): buildServiceContext
		// pencere başına 8 okuma yapıyordu (exception ham tarama, komşu
		// örnekleme, deploy…) ve 7'si atılıyordu — 24 saatlik iki pencere
		// için iki tam-gün ham spans taraması.
		n := emitGuidedStep(emit, "service_red", fmt.Sprintf(`{"service":%q,"window":%q}`, route.Service, absWindowLabel(w, loc)))
		rows, err := s.store.GetServiceSummary5m(ctx, route.Service, w.From, w.To)
		if err != nil {
			emitGuidedStepResult(emit, n, "service_red", "", err)
			return "", "", err
		}
		red := aggRED(rows, w.To.Sub(w.From).Seconds())
		reds = append(reds, red)
		emitGuidedStepResult(emit, n, "service_red", fmt.Sprintf("pencere %d: %d span", i+1, red.Spans), nil)
	}
	src := fmt.Sprintf("service_summary_5m (iki pencere: %s ↔ %s, %s)", absWindowLabel(route.Windows[0], loc), absWindowLabel(route.Windows[1], loc), loc.String())
	return renderWindowCompareTR(route.Service, route.Windows, reds, loc), src, nil
}
