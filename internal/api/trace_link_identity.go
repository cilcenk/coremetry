package api

// trace_link_identity.go — v0.10.566: trace'in DIŞ LİNK KİMLİĞİ.
//
// Operatör kuralı (kesinleşti):
//
//  1. Trace'in loglarının GÖVDE METNİNDE request_id varsa dış link onunla
//     üretilir. v0.10.578'e kadar buradaki yorum "Attributes hiçbir log
//     backend'inde dolmuyor" diyordu ve bu YANLIŞTI: ES flatten() ile
//     (elasticsearch.go), CH arraysToMap ile (repo.go) DOLDURUYOR.
//     Operatör prod'da `attributes.request_id` dolu bir kayıt gösterdi ve
//     çözücü onu bulamıyordu. Sıra artık: YAPILANDIRILMIŞ alan → gövde
//     metni (reqid.Find). Gövde taraması yedek olarak duruyor, çünkü bazı
//     servisler kimliği yalnız metin içinde basıyor.
//  2. request_id yoksa mevcut yol sürer: span attribute'larından
//     function_id + channel_code (şablon neyi isterse) — bu yüzden yanıt
//     `attrs` haritasını HER ZAMAN taşır.
//  3. Trace'te BİRDEN FAZLA request_id / function_id olabilir. Kazanan
//     SPAN ÖNCELİĞİNE göre seçilir: seçili span → İLK HATALI span → root
//     span → kalanlar. "İlk gördüğüm" değil, "operatörün baktığı".
//  4. Tarih kuralı DEĞİŞMEZ: şablondaki {{time:FMT}} trace zamanıdır; log
//     kaydının kendi damgası kimlik penceresine karışmaz.
//
// Maliyet: ES 10B doc/gün. Bu yüzden log okuması İKİ tavana hapsedildi —
// en çok ilk 5 aday span (span başına 20 kayıt) ve gerekirse TEK bir
// trace-geneli geçiş (50 kayıt). Pencere trace'in kendi zamanı ±1 dk.
//
// v0.10.568 — KAZANAN ARTIK GİZLİ DEĞİL. Operatör (2026-09-08):
// "Farklı function_id'ler alt span'lerde ama aynı trace'te olabilir…
// kullanıcıya hangi function_id'ye gitmek istersin diye seçenek
// verelim." Kural (4. madde) AYNEN duruyor — link hâlâ kazananla
// üretiliyor — ama yanıt artık `identities` ile TÜM adayları taşıyor ve
// `used` bayrağı bugünkü linkin hangisini kullandığını söylüyor. Seçim
// UI'ı ayrı dilim; bu dosya VERİYİ üretir.
//
// Rota salt-okunur: rol kapısı YOK, /api/traces/{id} ile aynı duruş.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/reqid"
)

func init() { registerRoutesExtra("trace-link-identity", (*Server).registerTraceLinkIdentityRoutes) }

const (
	// linkIdentitySpanProbe — log araması yapılan aday span tavanı.
	// Öncelik sırası anlamlı olduğu için ilk 5 span pratikte "seçili +
	// hatalı + root + iki komşu" demek; ötesi ES maliyeti.
	linkIdentitySpanProbe = 5
	// linkIdentityLogsPerSpan — span başına okunan log kaydı.
	linkIdentityLogsPerSpan = 20
	// linkIdentityLogsPerTrace — span_id taşımayan logları da gören TEK
	// trace-geneli geçiş.
	//
	// v0.10.569: 50 → 150 (operatör: "request_id logta herhangi bir yerde
	// varsa bulacak değil mi"). Konuşkan bir trace'te kimlik 60. satırda
	// geçiyorsa 50 tavanı onu kaçırıyordu. Bu TEK bir ES turu; tavanı
	// üçe katlamanın maliyeti aynı sorgunun sayfa boyutu, ek tur değil.
	linkIdentityLogsPerTrace = 150
	// linkIdentityAttrMax — birleştirilmiş attribute tavanı (bağlam
	// bütçesi; şablon en çok birkaç anahtar ister).
	linkIdentityAttrMax = 200
	// linkIdentityWindowPad — trace penceresinin iki ucuna eklenen pay:
	// ingest gecikmesi/saat kayması yüzünden logu ıskalamamak için.
	linkIdentityWindowPad = time.Minute
	// linkIdentityIDMax — trace/span id için bayt tavanı. Cache anahtarı
	// TÜM girdileri taşıdığı için sınırsız girdi = sınırsız anahtar.
	linkIdentityIDMax = 64
	// linkIdentityCandidateMax — v0.10.568: yanıttaki aday tavanı. Bir
	// menü 10 satırdan sonra seçim aracı olmaktan çıkar; kesilen sayı
	// Note'ta DÜRÜSTÇE söylenir.
	linkIdentityCandidateMax = 10
	// linkIdentityKeysMax — `keys` sorgusundaki anahtar tavanı. Şablonun
	// Requires listesi pratikte 1-2 anahtar; 5 bol pay.
	linkIdentityKeysMax = 5
	// linkIdentityKeyLenMax — tek bir anahtar adı için bayt tavanı.
	linkIdentityKeyLenMax = 64
)

// Rol değerleri — adayın trace'teki YERİ (orderTraceSpans basamakları).
// İstemci bunu rozet olarak gösterir: operatör "hangi function_id" diye
// sorulduğunda hangisinin hatalı/kök/baktığı span olduğunu görmeli.
const (
	linkIdentityRoleSelected = "selected"
	linkIdentityRoleError    = "error"
	linkIdentityRoleRoot     = "root"
	linkIdentityRoleSpan     = "span"
)

// linkIdentityKeyRequestID — log gövdesinden çözülen kimliğin aday
// anahtarı. Span attribute anahtarlarıyla AYNI ad alanında yaşar, bu
// yüzden sabit: (Key, Value) tekilleştirmesi ona da uygulanır.
const linkIdentityKeyRequestID = "request_id"

// Source değerleri — istemci rozeti.
const (
	linkIdentitySourceLog  = "log"  // kimlik log gövdesinden çözüldü
	linkIdentitySourceSpan = "span" // kimlik yok; şablon attribute yoluna düşer
	linkIdentitySourceNone = "none" // trace'te span yok
)

// traceLinkCandidate — v0.10.568: operatörün SEÇEBİLECEĞİ bir kimlik.
//
// Bir trace birden çok isteği taşıyabilir (farklı request_id) ve alt
// span'ler farklı function_id'ler tutabilir. v0.10.566 kuralı kazananı
// seçiyor; bu tip kazananı SEÇENEKLERİN İÇİNDE gösteriyor.
type traceLinkCandidate struct {
	Value    string `json:"value"`
	Key      string `json:"key"`    // "request_id" | istenen attribute anahtarı
	Source   string `json:"source"` // "log" | "span"
	SpanID   string `json:"spanId,omitempty"`
	SpanName string `json:"spanName,omitempty"`
	Service  string `json:"service,omitempty"`
	// Role — "selected" | "error" | "root" | "span". Span'i bilinmeyen
	// (trace-geneli geçişte bulunmuş) bir kimlik "span" sayılır: rol
	// UYDURULMAZ.
	Role    string `json:"role"`
	IsError bool   `json:"isError,omitempty"`
	// Loose — v0.10.569: biçim doğrulanamadı (gevşek eşleşme).
	Loose bool `json:"loose,omitempty"`
	// Used — bu değer BUGÜNKÜ linkte kullanılıyor mu (anahtar başına ilk
	// aday + çözülen request_id). Birden çok anahtar varsa birden çok
	// true olabilir; alan adı "seçili" değil "kullanılan", çünkü seçim
	// operatörün.
	Used bool `json:"used,omitempty"`
}

// traceLinkIdentity — /api/traces/{id}/link-identity yanıtı.
type traceLinkIdentity struct {
	TraceID   string `json:"traceId"`
	RequestID string `json:"requestId,omitempty"`
	Source    string `json:"source"`
	// SpanID — kimliği VEREN span (log kaydının span_id'si). Trace-geneli
	// geçişte bulunduysa boş olabilir: kayıt span'e bağlı değildi.
	SpanID string `json:"spanId,omitempty"`
	// Attrs — span önceliğiyle birleştirilmiş attribute'lar; ilk DOLU
	// değer kazanır (lib/externalLinks.ts collectLinkCtx ile aynı kural,
	// yalnız sırası "root önce" değil "operatörün baktığı span önce").
	Attrs map[string]string `json:"attrs"`
	// Candidates — değerlendirilen span sırası (öncelik sırasıyla, log
	// araması tavanı kadar). Operatör "neden bu kimlik" diye sorduğunda
	// cevabı budur.
	Candidates []string `json:"candidates"`
	// Identities — v0.10.568: seçilebilir kimlikler, öncelik sırasıyla.
	// ASLA nil (istemci dizi bekliyor); kimlik çözülemese bile span
	// attribute adaylarını taşır.
	Identities []traceLinkCandidate `json:"identities"`
	// DistinctRequestIDs — okunan log kayıtlarında görülen FARKLI kimlik
	// sayısı. Dürüstlük alanı: 1'den büyükse trace birden çok isteği
	// taşıyor ve seçim ÖNCELİKLE yapıldı, "tek doğru" olduğu için değil.
	DistinctRequestIDs int `json:"distinctRequestIds"`
	// Partial — log tarafı okunamadı (yavaş/erişilemez backend). Yanıt
	// yine 200: attribute yolu çalışır, ama "kimlik yok" demek yerine
	// "bakamadım" diyoruz.
	Partial bool   `json:"partial,omitempty"`
	Note    string `json:"note"`
	// TZ — v0.10.567 (operatör kararı 2026-09-08: "Europe/Istanbul olsun").
	// Dış link şablonundaki {{time:FMT}} / {{endTime:FMT}} bu dilimde
	// biçimlenir. AYARIN ADI taşınır, çözülmüş Location DEĞİL: sunucuda
	// tzdata yoksa reqid.Location "+03" sabit dilimine düşer ve o ad
	// tarayıcının Intl'ine verilemez — ad taşırsak tarayıcı kendi
	// tzdata'sıyla doğru biçimler.
	TZ string `json:"tz"`
	// RequestIDLoose — v0.10.569: kullanılan request_id GEVŞEK eşleşmeyle
	// bulundu (biçim doğrulanamadı). Link üretilir, ama düğme ipucu bunu
	// söyler: "buldum" ile "doğruladım" ayrı şeyler.
	RequestIDLoose bool `json:"requestIdLoose,omitempty"`
}

func (s *Server) registerTraceLinkIdentityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/traces/{id}/link-identity", s.getTraceLinkIdentity)
}

// traceLinkIdentityCacheKey — SAF: TÜM girdiler anahtarda (v0.5.187
// sınıfı). tz de girdi: saat dilimi kimliğin gömülü zamanını ±3 saat
// kaydırır, yani AYNI trace için BAŞKA bir cevap üretebilir.
//
// v0.10.568: keys de girdi — farklı anahtar kümesi FARKLI `identities`
// üretir, aynı gövdeyi alması tam v0.5.187 sınıfı bir zehirlenme olurdu.
// keys SIRALANMAZ: aday listesindeki anahtar sırası istenen sıradır,
// yani ["a","b"] ile ["b","a"] farklı gövde döndürür — küme değil dizi.
// fnvStr her parçadan sonra NUL yazar, "a","bc" ile "ab","c" çakışmaz.
// Sürüm v1→v2: gövde şekli değişti, bayat girdiler `identities`siz.
func traceLinkIdentityCacheKey(traceID, spanID, tz string, keys []string) string {
	return fmt.Sprintf("trace-link-id:v2:%s:%s:%s:%s", traceID, spanID, tz, fnvStr(keys...))
}

// linkIdentityKeyOK — SAF: attribute anahtarı hijyeni ([A-Za-z0-9_.-]).
func linkIdentityKeyOK(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// parseLinkIdentityKeys — SAF: `keys` sorgu parametresi → anahtar dizisi.
//
// Anahtar ADLARI ÜRÜNE GÖMÜLMEZ: istemci bunları yapılandırılmış link
// şablonlarının Requires listesinden türetip gönderir (function_id /
// channel_code gibi adlar müşteriye özgü, depoya girmez). Bu yüzden
// burada beyaz liste değil HİJYEN var — serbest metin hem cache
// anahtarına hem yanıta giriyor.
//
// Boşluklar kırpılır, boş parçalar (sondaki virgül) atlanır, tekrarlar
// SIRAYI bozmadan tekilleşir. Boş/parametresiz = nil: aday listesi
// yalnız request_id'lerden oluşur.
func parseLinkIdentityKeys(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		k := strings.TrimSpace(part)
		if k == "" {
			continue
		}
		if len(k) > linkIdentityKeyLenMax {
			// Değer YANKILANMAZ: doğrulanmamış, sınırsız uzunlukta.
			return nil, fmt.Errorf("keys: attribute key too long (≤%d)", linkIdentityKeyLenMax)
		}
		if !linkIdentityKeyOK(k) {
			return nil, fmt.Errorf("keys: %q invalid (allowed: A-Za-z0-9_.-)", k)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
		if len(out) > linkIdentityKeysMax {
			return nil, fmt.Errorf("keys: at most %d attribute keys", linkIdentityKeysMax)
		}
	}
	return out, nil
}

// linkIdentityIDOK — trace/span id hijyeni: boş ya da onaltılık, tavan
// altında. Cache anahtarına ve log sorgusuna giden tek serbest girdi bu.
func linkIdentityIDOK(v string) bool {
	if len(v) > linkIdentityIDMax {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

func (s *Server) getTraceLinkIdentity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || !linkIdentityIDOK(id) {
		writeJSONError(w, http.StatusBadRequest, "trace id required (hex, ≤64)")
		return
	}
	// span — seçili span (opsiyonel). Öncelik sırasının BİRİNCİ basamağı.
	span := r.URL.Query().Get("span")
	if !linkIdentityIDOK(span) {
		writeJSONError(w, http.StatusBadRequest, "span must be hex, ≤64")
		return
	}
	// keys — istemcinin şablon Requires'ından türettiği attribute
	// anahtarları (v0.10.568). Yoksa aday listesi yalnız request_id.
	keys, kerr := parseLinkIdentityKeys(r.URL.Query().Get("keys"))
	if kerr != nil {
		writeJSONError(w, http.StatusBadRequest, kerr.Error())
		return
	}
	tz := ""
	if s.store != nil {
		tz = s.reqidTZSetting(r.Context())
	}
	s.serveCached(w, r, traceLinkIdentityCacheKey(id, span, tz, keys), 30*time.Second, func(ctx context.Context) (any, error) {
		spans, err := s.traceLinkSpans(ctx, id)
		if err != nil {
			return nil, err
		}
		return s.resolveTraceLinkIdentity(ctx, id, span, spans, tz, keys), nil
	})
}

// traceLinkSpans — trace'in span'leri PAYLAŞILAN çözümleyiciden
// (trace_resolve.go: önce Tempo, sonra ClickHouse).
//
// Doğrudan CH okuması BİLE BİLE yapılmıyor: v0.9.632 olayında Tempo-only
// bir trace waterfall'ı çiziyor ama aynı trace'i CH'den soran yüzey
// "yok" diyordu. Bu uç için o hata "dış link düğmesi Tempo trace'inde
// ölü" demek olurdu. TestTraceSurfacesUseSharedResolver kuralı çiviliyor.
//
// Store yoksa (API rolü kapalı kurulum / testler) boş dilim: çözümleyici
// dürüstçe "none" der, panik yok.
func (s *Server) traceLinkSpans(ctx context.Context, id string) ([]chstore.SpanRow, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	spans, _, err := s.resolveTraceSpans(ctx, id)
	return spans, err
}

// ── SAF çekirdekler ─────────────────────────────────────────────────────────

// orderTraceSpans — SAF: kazanan span önceliği.
//
//	seçili span (varsa ve trace'te ise) → İLK HATALI span → root span →
//	kalanlar (StartTime artan)
//
// Taban sıra StartTime artan, eşitlikte SpanID — CH satır sırası
// garantili değil, cevap ise deterministik olmak ZORUNDA (aynı trace iki
// açılışta iki farklı link üretmesin). Aynı span iki kez dönmez.
func orderTraceSpans(spans []chstore.SpanRow, selected string) []chstore.SpanRow {
	if len(spans) == 0 {
		return nil
	}
	base := append([]chstore.SpanRow(nil), spans...)
	sort.SliceStable(base, func(i, j int) bool {
		if base[i].StartTime != base[j].StartTime {
			return base[i].StartTime < base[j].StartTime
		}
		return base[i].SpanID < base[j].SpanID
	})
	out := make([]chstore.SpanRow, 0, len(base))
	taken := make([]bool, len(base))
	seenID := map[string]bool{}
	push := func(i int) {
		if taken[i] {
			return
		}
		taken[i] = true
		if id := base[i].SpanID; id != "" {
			if seenID[id] {
				return // aynı span_id iki satırda: tekilleştir
			}
			seenID[id] = true
		}
		out = append(out, base[i])
	}
	// 1) Seçili span — operatörün baktığı satır. Trace'te yoksa atlanır.
	if selected != "" {
		for i := range base {
			if base[i].SpanID == selected {
				push(i)
				break
			}
		}
	}
	// 2) İlk hatalı span (en erken). Bir arıza incelemesinde kimliği
	//    taşıyan istek neredeyse her zaman budur.
	for i := range base {
		if base[i].StatusCode == "error" {
			push(i)
			break
		}
	}
	// 3) Root (varsa).
	//
	// "Root yoksa en erken span" için AYRI bir basamak YOK: taban sıra
	// StartTime artan olduğu için 4. basamak zaten en erken atanmamış
	// span'i alır. Ayrı bir `push(0)` ölü koddu (mutasyon testi ısırmadı)
	// ve okuyana var olmayan bir kural anlatırdı.
	for i := range base {
		if base[i].ParentSpanID == "" {
			push(i)
			break
		}
	}
	// 4) Kalanlar (taban sıra: StartTime artan → root yoksa en erken).
	for i := range base {
		push(i)
	}
	return out
}

// mergeSpanAttrs — SAF: sırayla gez, ilk BOŞ OLMAYAN değer kazanır.
// Anahtarlar span içinde sıralı gezilir: tavan dolduğunda HANGİ
// anahtarların girdiği map gezinme sırasına kalmasın (deterministik).
func mergeSpanAttrs(ordered []chstore.SpanRow) map[string]string {
	out := make(map[string]string)
	for _, sp := range ordered {
		if len(out) >= linkIdentityAttrMax {
			return out
		}
		keys := make([]string, 0, len(sp.Attributes))
		for k := range sp.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := sp.Attributes[k]
			if v == "" {
				continue
			}
			if _, dup := out[k]; dup {
				continue
			}
			if len(out) >= linkIdentityAttrMax {
				return out
			}
			out[k] = v
		}
	}
	return out
}

// traceLinkWindow — SAF: log aramasının zaman penceresi.
// min(StartTime) − pad … max(StartTime + DurationMs) + pad.
// EndTime kolonuna DEĞİL süreye dayanır (kolon her yolda dolu değil) ve
// birim karışmaz: StartTime ns, DurationMs → ns (v0.6.36 dersi).
func traceLinkWindow(spans []chstore.SpanRow) (time.Time, time.Time) {
	if len(spans) == 0 {
		return time.Time{}, time.Time{}
	}
	minNs, maxNs := spans[0].StartTime, spans[0].StartTime
	for _, sp := range spans {
		if sp.StartTime < minNs {
			minNs = sp.StartTime
		}
		end := sp.StartTime + int64(sp.DurationMs*1e6)
		if end > maxNs {
			maxNs = end
		}
	}
	return time.Unix(0, minNs).Add(-linkIdentityWindowPad), time.Unix(0, maxNs).Add(linkIdentityWindowPad)
}

// linkIdentityLogHit — SAF veri: log çözümlemesinde görülen bir kimlik ve
// geldiği kayıt span'i (kayıt span taşımıyorsa sorgulanan span, o da
// yoksa boş).
// linkIdentityLogAttrKeys — kimliğin yapılandırılmış log alanındaki bilinen
// yazımları. Şablonun istediği anahtarlar (?keys=) bunlardan ÖNCE denenir:
// operatör bir şablona `function_id` yazdıysa istediği odur, request_id değil.
var linkIdentityLogAttrKeys = []string{"request_id", "requestId", "request.id"}

// identityFromLogAttrs — SAF: log kaydının attribute haritasından kimlik.
// Dönüş: değer, ANAHTAR ADI (uydurulmaz, olduğu gibi), katı-biçim mi, bulundu mu.
//
// Biçimi tutmayan değer DÜŞÜRÜLMEZ: alan adı zaten `request_id` ise değer
// kimliktir; yalnız katı-olmayan diye işaretlenir ve arayüz "BİÇİM
// DOĞRULANAMADI" der. Kaybetmek, şüpheyle göstermekten kötüdür.
func identityFromLogAttrs(attrs map[string]string, keys []string, loc *time.Location) (string, string, bool, bool) {
	if len(attrs) == 0 {
		return "", "", false, false
	}
	try := func(k string) (string, string, bool, bool) {
		v := strings.TrimSpace(attrs[k])
		if v == "" {
			return "", "", false, false
		}
		if _, ok := reqid.Parse(v, loc); ok {
			return v, k, true, true
		}
		return v, k, false, true
	}
	for _, k := range keys {
		if v, kk, strict, ok := try(k); ok {
			return v, kk, strict, true
		}
	}
	for _, k := range linkIdentityLogAttrKeys {
		if v, kk, strict, ok := try(k); ok {
			return v, kk, strict, true
		}
	}
	return "", "", false, false
}

type linkIdentityLogHit struct {
	Value  string
	SpanID string
	// Loose — v0.10.569: token kimlik ŞEKLİNDE ama sabit biçime UYMUYOR
	// (reqid.FindLooseToken). Link için değer yeterli, ama "çözümledim"
	// İDDİA ETMİYORUZ — operatör bunu görmeli.
	Loose bool
	// Key — v0.10.572: token'ı taşıyan JSON alanının GERÇEK adı
	// ("BsaRequestId"). Tarayıcı alan adına bakmaz, şekli tanır; ama
	// operatörün logunda gördüğü ad neyse menü onu yazmalı. Boş =
	// gövde JSON değil ya da ad çözülemedi.
	Key string
}

// jsonKeyMaxLen — geriye doğru anahtar taraması sınırı.
const jsonKeyMaxLen = 64

// jsonKeyForToken — SAF: gövdede token'ı taşıyan JSON alan adı.
//
// v0.10.572 (operatör: "bazen requestid BsaRequestId olarak logta yazıyor").
// Kimlik ARAMASI ad-bağımsız kalır (şekil tanınır); bu yalnız ETİKET içindir.
// Desen token'dan GERİYE: `"<anahtar>"` boşluk `:` boşluk `"` token.
// Uymuyorsa "" — ad UYDURULMAZ, çağıran nötr etikete düşer.
func jsonKeyForToken(body, token string) string {
	if token == "" {
		return ""
	}
	i := strings.Index(body, token)
	if i <= 0 || body[i-1] != '"' {
		return "" // değer tırnak içinde değil (düz metin log)
	}
	p := i - 2
	skipSpace := func(p int) int {
		for p >= 0 && (body[p] == ' ' || body[p] == '\t') {
			p--
		}
		return p
	}
	if p = skipSpace(p); p < 0 || body[p] != ':' {
		return ""
	}
	if p = skipSpace(p - 1); p < 0 || body[p] != '"' {
		return ""
	}
	end := p
	start := -1
	for k := 0; p > 0 && k <= jsonKeyMaxLen; k, p = k+1, p-1 {
		if body[p-1] == '"' {
			start = p
			break
		}
	}
	if start < 0 || start >= end {
		return ""
	}
	key := body[start:end]
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return ""
		}
	}
	return key
}

// linkIdentitySpanRoles — SAF: `ordered` ile AYNI uzunlukta rol dilimi.
//
// orderTraceSpans'in basamaklarını okur (seçili → ilk hatalı → root →
// kalanlar) ama KONUMA değil VERİYE bakar: "ilk hatalı" ve "root" en
// erken (StartTime, SpanID) satırdır — tıpkı orderTraceSpans'in taban
// sırasında olduğu gibi. Konuma bakan bir uygulama, seçili span araya
// girdiğinde bir satır kayardı.
//
// Öncelik ATAMA SIRASINDA yaşıyor: root önce yazılır, hatalı üstüne,
// seçili en son — yani hem hatalı hem root olan bir span "error",
// seçilmiş olan her şeye rağmen "selected".
func linkIdentitySpanRoles(ordered []chstore.SpanRow, selected string) []string {
	roles := make([]string, len(ordered))
	earlier := func(a, b chstore.SpanRow) bool {
		if a.StartTime != b.StartTime {
			return a.StartTime < b.StartTime
		}
		return a.SpanID < b.SpanID
	}
	errIdx, rootIdx := -1, -1
	for i, sp := range ordered {
		roles[i] = linkIdentityRoleSpan
		if sp.StatusCode == "error" && (errIdx < 0 || earlier(sp, ordered[errIdx])) {
			errIdx = i
		}
		if sp.ParentSpanID == "" && (rootIdx < 0 || earlier(sp, ordered[rootIdx])) {
			rootIdx = i
		}
	}
	if rootIdx >= 0 {
		roles[rootIdx] = linkIdentityRoleRoot
	}
	if errIdx >= 0 {
		roles[errIdx] = linkIdentityRoleError
	}
	if selected != "" {
		for i := range ordered {
			if ordered[i].SpanID == selected {
				roles[i] = linkIdentityRoleSelected
				break
			}
		}
	}
	return roles
}

// buildLinkIdentityCandidates — SAF: operatörün SEÇEBİLECEĞİ kimlikler +
// tavan yüzünden gösterilmeyen aday sayısı.
//
// Sıra = öncelik. Önce request_id'ler (v0.10.566 kuralı request_id'yi
// önceler), sonra span attribute'ları orderTraceSpans sırasıyla. Span
// tarafında probe tavanı YOK: attribute'lar zaten bellekte, ES turu
// değil — 5 span probu LOG maliyeti içindi, aday listesi için değil.
//
// Tekilleştirme (Key, Value) ikilisinde ve İLK görülen kazanır: aynı
// function_id on span'de tekrar ediyorsa operatöre on kez sorulmaz, ama
// aynı değeri taşıyan FARKLI anahtarlar ayrı seçenektir.
//
// Used = "bugünkü link bunu kullanıyor", "seçili" DEĞİL — seçim
// operatörün. Anahtar başına TEK bayrak: attribute yolunda mergeSpanAttrs
// ilk DOLU değeri alır (bu da spans-dış/keys-iç gezinme yüzünden
// listedeki ilk adaydır), request_id yolunda çözülen kimlik.
func buildLinkIdentityCandidates(ordered []chstore.SpanRow, selected string, hits []linkIdentityLogHit, keys []string, usedRequestID string) ([]traceLinkCandidate, int) {
	out := make([]traceLinkCandidate, 0, linkIdentityCandidateMax)
	dropped := 0
	seen := make(map[string]bool)
	roles := linkIdentitySpanRoles(ordered, selected)
	// idx — span_id → ordered dizini (rol/ad/servis için). Boş span_id
	// anahtar olmaz: "bilinmiyor" ile "boş id'li satır" karışmasın.
	idx := make(map[string]int, len(ordered))
	for i, sp := range ordered {
		if sp.SpanID == "" {
			continue
		}
		if _, dup := idx[sp.SpanID]; !dup {
			idx[sp.SpanID] = i
		}
	}
	add := func(c traceLinkCandidate) {
		if c.Key == "" || c.Value == "" {
			return
		}
		dk := c.Key + "\x00" + c.Value
		if seen[dk] {
			return
		}
		seen[dk] = true
		// Tavan aşıldığında da tekilleştirme sürer: "+N" DÜRÜST bir sayı,
		// aynı değerin tekrarı değil.
		if len(out) >= linkIdentityCandidateMax {
			dropped++
			return
		}
		out = append(out, c)
	}
	// 1) request_id adayları — log gövdesinden çözülenler.
	for _, h := range hits {
		// v0.10.572 — logdaki GERÇEK alan adı ("BsaRequestId"); yoksa nötr ad.
		key := h.Key
		if key == "" {
			key = linkIdentityKeyRequestID
		}
		c := traceLinkCandidate{
			Value:  h.Value,
			Key:    key,
			Source: linkIdentitySourceLog,
			Role:   linkIdentityRoleSpan,
		}
		c.Loose = h.Loose
		if i, ok := idx[h.SpanID]; ok {
			sp := ordered[i]
			c.SpanID, c.SpanName, c.Service = sp.SpanID, sp.Name, sp.ServiceName
			c.Role, c.IsError = roles[i], sp.StatusCode == "error"
		}
		add(c)
	}
	// 2) span attribute adayları — span dışta, anahtar içte. Bu sıra
	//    mergeSpanAttrs ile AYNI kuralı üretir (ilk dolu değer kazanır),
	//    yani anahtar başına ilk aday = bugünkü linkin kullandığı değer.
	for i, sp := range ordered {
		for _, k := range keys {
			v := sp.Attributes[k]
			if v == "" {
				continue
			}
			add(traceLinkCandidate{
				Value: v, Key: k, Source: linkIdentitySourceSpan,
				SpanID: sp.SpanID, SpanName: sp.Name, Service: sp.ServiceName,
				Role: roles[i], IsError: sp.StatusCode == "error",
			})
		}
	}
	// 3) Used — anahtar başına TEK. Varsayılan ilk aday; request_id'de
	//    çözülen kimlik varsa O işaretlenir (bayrak "kullanılan"ı
	//    göstermeli, listedeki konumu değil).
	usedAt := make(map[string]int, len(out))
	for i, c := range out {
		if _, ok := usedAt[c.Key]; !ok {
			usedAt[c.Key] = i
		}
	}
	if usedRequestID != "" {
		for i, c := range out {
			if c.Key == linkIdentityKeyRequestID && c.Value == usedRequestID {
				usedAt[c.Key] = i
				break
			}
		}
	}
	for _, i := range usedAt {
		out[i].Used = true
	}
	return out, dropped
}

// linkIdentityDroppedNote — SAF: tavana çarpan aday sayısını nota ekler.
// "Bulamadım" ile "gösteremedim" ayrı şeyler (linkIdentityNote doktrini).
func linkIdentityDroppedNote(note string, dropped int) string {
	if dropped <= 0 {
		return note
	}
	if note == "" {
		return fmt.Sprintf("+%d aday gösterilmiyor", dropped)
	}
	return fmt.Sprintf("%s; +%d aday gösterilmiyor", note, dropped)
}

// ── Çözümleme ───────────────────────────────────────────────────────────────

// resolveTraceLinkIdentity — span'ler ELDE, log tarafı burada çözülür.
// CH okuması çağıranda (traceLinkSpans) kaldığı için bu fonksiyon sahte
// bir logstore ile uçtan uca test edilebilir.
func (s *Server) resolveTraceLinkIdentity(ctx context.Context, traceID, selected string, spans []chstore.SpanRow, tz string, keys []string) traceLinkIdentity {
	out := traceLinkIdentity{
		TraceID:    traceID,
		Attrs:      map[string]string{},
		Candidates: []string{},
		Identities: []traceLinkCandidate{},
		Source:     linkIdentitySourceNone,
	}
	if out.TZ = strings.TrimSpace(tz); out.TZ == "" {
		out.TZ = reqid.DefaultTZ
	}
	ordered := orderTraceSpans(spans, selected)
	// hits — log çözümlemesinde görülen FARKLI kimlikler, GÖRÜLME
	// sırasıyla (map değil dilim: sıra cevabın parçası).
	var hits []linkIdentityLogHit
	seenHit := make(map[string]bool)
	// v0.10.569 — GEVŞEK adaylar: katı tarayıcı hiçbir şey bulamazsa
	// kullanılır. Aynı gezinmede toplanır, EK ES TURU YOK.
	var looseHits []linkIdentityLogHit
	seenLoose := make(map[string]bool)
	// finish — HER dönüş noktası aday listesini taşır (v0.10.568).
	// Kimlik çözülemese de (log arka ucu yok / okuma düştü) operatörün
	// seçebileceği span attribute'ları vardır; boş bir menü göstermek
	// "aday yok" yalanı olurdu.
	finish := func(o traceLinkIdentity) traceLinkIdentity {
		ids, dropped := buildLinkIdentityCandidates(ordered, selected, hits, keys, o.RequestID)
		o.Identities = ids
		o.Note = linkIdentityDroppedNote(o.Note, dropped)
		return o
	}
	if len(ordered) == 0 {
		out.Note = "trace'te span yok — kimlik çözümlenemedi"
		return finish(out)
	}
	out.Attrs = mergeSpanAttrs(ordered)
	probe := ordered
	if len(probe) > linkIdentitySpanProbe {
		probe = probe[:linkIdentitySpanProbe]
	}
	for _, sp := range probe {
		out.Candidates = append(out.Candidates, sp.SpanID)
	}
	// Kimlik yoksa varsayılan cevap: attribute yolu (mevcut davranış).
	out.Source = linkIdentitySourceSpan
	if s == nil || s.logs == nil {
		out.Note = "log arka ucu yapılandırılmamış — kimlik span attribute'larından"
		return finish(out)
	}

	loc := reqid.Location(tz)
	from, to := traceLinkWindow(spans)
	// scan — bir sayfadaki kayıtları gezer; İLK bulan kazanır ama sayfanın
	// KALANI da taranır: farklı kimlik sayımı bedava (sayfa zaten elde) ve
	// "requestId dolu, distinct 0" gibi yalan bir alan üretmeyiz. v0.10.568:
	// görülen her FARKLI kimlik `hits`e de yazılır — aday listesi bu.
	scan := func(page *logstore.Page, fallbackSpan string) (string, string, bool) {
		if page == nil {
			return "", "", false
		}
		var winID, winSpan string
		found := false
		for _, rec := range page.Logs {
			if rec == nil {
				continue
			}
			// v0.10.578 — YAPILANDIRILMIŞ alan gövdeden ÖNCE gelir: metin
			// taraması bir tahmindir, alan adı bir sözleşmedir.
			if v, k, strict, ok := identityFromLogAttrs(rec.Attributes, keys, loc); ok {
				aSpan := rec.SpanID
				if aSpan == "" {
					aSpan = fallbackSpan
				}
				if strict {
					if !seenHit[v] {
						seenHit[v] = true
						hits = append(hits, linkIdentityLogHit{Value: v, SpanID: aSpan, Key: k})
					}
					if !found {
						found = true
						winID, winSpan = v, aSpan
					}
				} else if !seenLoose[v] {
					seenLoose[v] = true
					looseHits = append(looseHits, linkIdentityLogHit{Value: v, SpanID: aSpan, Loose: true, Key: k})
				}
				continue
			}
			id, ok := reqid.Find(rec.Body, loc)
			if !ok {
				// v0.10.569 — katı biçim tutmadı: kimliğe BENZEYEN token'ı
				// (47-64 alnum, ≥33 rakam) yedeğe yaz. Kazanan seçilmez,
				// yalnız hiçbir katı eşleşme çıkmazsa kullanılır.
				if tok, lok := reqid.FindLooseToken(rec.Body); lok && !seenLoose[tok] {
					seenLoose[tok] = true
					lSpan := rec.SpanID
					if lSpan == "" {
						lSpan = fallbackSpan
					}
					looseHits = append(looseHits, linkIdentityLogHit{Value: tok, SpanID: lSpan, Loose: true, Key: jsonKeyForToken(rec.Body, tok)})
				}
				continue
			}
			recSpan := rec.SpanID
			if recSpan == "" {
				recSpan = fallbackSpan
			}
			if !seenHit[id.Raw] {
				seenHit[id.Raw] = true
				hits = append(hits, linkIdentityLogHit{Value: id.Raw, SpanID: recSpan, Key: jsonKeyForToken(rec.Body, id.Raw)})
			}
			if !found {
				found = true
				winID, winSpan = id.Raw, recSpan
			}
		}
		return winID, winSpan, found
	}

	for _, sp := range probe {
		if sp.SpanID == "" {
			continue
		}
		page, err := logstore.LogsForSpan(ctx, s.logs, traceID, sp.SpanID, from, to, linkIdentityLogsPerSpan)
		if err != nil {
			// Log tarafı düştü: fatal DEĞİL. Attribute yolu ayakta,
			// ama "kimlik yok" demiyoruz — "bakamadım" diyoruz.
			out.Partial = true
			out.Note = "log okuması başarısız (" + err.Error() + ") — kimlik span attribute'larından"
			return finish(out)
		}
		if id, spanID, ok := scan(page, sp.SpanID); ok {
			out.RequestID, out.SpanID, out.Source = id, spanID, linkIdentitySourceLog
			out.DistinctRequestIDs = len(hits)
			out.Note = linkIdentityNote(out, len(ordered))
			return finish(out)
		}
	}
	// span_id taşımayan loglar için TEK trace-geneli geçiş.
	page, err := logstore.LogsForTrace(ctx, s.logs, traceID, from, to, linkIdentityLogsPerTrace)
	if err != nil {
		out.Partial = true
		out.Note = "log okuması başarısız (" + err.Error() + ") — kimlik span attribute'larından"
		return finish(out)
	}
	if id, spanID, ok := scan(page, ""); ok {
		out.RequestID, out.SpanID, out.Source = id, spanID, linkIdentitySourceLog
	}
	// v0.10.569 — katı tarayıcı boş döndüyse gevşek yedek. Sıra korunur:
	// gevşek aday ASLA katı bir eşleşmenin önüne geçmez.
	if out.Source != linkIdentitySourceLog && len(looseHits) > 0 {
		out.RequestID, out.SpanID = looseHits[0].Value, looseHits[0].SpanID
		out.Source, out.RequestIDLoose = linkIdentitySourceLog, true
		hits = append(hits, looseHits...)
	}
	out.DistinctRequestIDs = len(hits)
	out.Note = linkIdentityNote(out, len(ordered))
	return finish(out)
}

// linkIdentityNote — SAF: operatöre "neden bu kimlik" cevabı.
func linkIdentityNote(id traceLinkIdentity, spanCount int) string {
	var b strings.Builder
	if id.Source != linkIdentitySourceLog {
		// "Bulamadım" ile "hepsine bakmadım" AYRI şeyler: tavana çarpan
		// bir trace'te operatör aramanın kısmi olduğunu bilmeli.
		b.WriteString("loglarda request_id bulunamadı — kimlik span attribute'larından (function_id/channel_code)")
		if spanCount > linkIdentitySpanProbe {
			fmt.Fprintf(&b, "; %d span'in ilk %d adayı tarandı", spanCount, linkIdentitySpanProbe)
		}
		return b.String()
	}
	b.WriteString("request_id log gövdesinden çözüldü")
	if id.RequestIDLoose {
		// "Buldum" ile "doğruladım" AYRI: gevşek eşleşme linki üretir ama
		// biçim iddiası taşımaz.
		b.WriteString(" — BİÇİM DOĞRULANAMADI (gevşek eşleşme)")
	}
	if id.SpanID != "" {
		b.WriteString(" (span " + id.SpanID + ")")
	}
	if id.DistinctRequestIDs > 1 {
		fmt.Fprintf(&b, "; okunan kayıtlarda %d farklı kimlik var, span önceliğiyle seçildi", id.DistinctRequestIDs)
	}
	if spanCount > linkIdentitySpanProbe {
		fmt.Fprintf(&b, "; %d span'in ilk %d adayı tarandı", spanCount, linkIdentitySpanProbe)
	}
	return b.String()
}
