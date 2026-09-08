package api

// trace_link_identity.go — v0.10.566: trace'in DIŞ LİNK KİMLİĞİ.
//
// Operatör kuralı (kesinleşti):
//
//  1. Trace'in loglarının GÖVDE METNİNDE request_id varsa dış link onunla
//     üretilir. Yapılandırılmış alan YOK — `Attributes` hiçbir log
//     backend'inde dolmuyor (ES yalnız ResourceAttributes yazıyor), bu
//     yüzden kimlik gövdeden çözülür (reqid.Find).
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
	linkIdentityLogsPerTrace = 50
	// linkIdentityAttrMax — birleştirilmiş attribute tavanı (bağlam
	// bütçesi; şablon en çok birkaç anahtar ister).
	linkIdentityAttrMax = 200
	// linkIdentityWindowPad — trace penceresinin iki ucuna eklenen pay:
	// ingest gecikmesi/saat kayması yüzünden logu ıskalamamak için.
	linkIdentityWindowPad = time.Minute
	// linkIdentityIDMax — trace/span id için bayt tavanı. Cache anahtarı
	// TÜM girdileri taşıdığı için sınırsız girdi = sınırsız anahtar.
	linkIdentityIDMax = 64
)

// Source değerleri — istemci rozeti.
const (
	linkIdentitySourceLog  = "log"  // kimlik log gövdesinden çözüldü
	linkIdentitySourceSpan = "span" // kimlik yok; şablon attribute yoluna düşer
	linkIdentitySourceNone = "none" // trace'te span yok
)

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
}

func (s *Server) registerTraceLinkIdentityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/traces/{id}/link-identity", s.getTraceLinkIdentity)
}

// traceLinkIdentityCacheKey — SAF: TÜM girdiler anahtarda (v0.5.187
// sınıfı). tz de girdi: saat dilimi kimliğin gömülü zamanını ±3 saat
// kaydırır, yani AYNI trace için BAŞKA bir cevap üretebilir.
func traceLinkIdentityCacheKey(traceID, spanID, tz string) string {
	return fmt.Sprintf("trace-link-id:v1:%s:%s:%s", traceID, spanID, tz)
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
	tz := ""
	if s.store != nil {
		tz = s.reqidTZSetting(r.Context())
	}
	s.serveCached(w, r, traceLinkIdentityCacheKey(id, span, tz), 30*time.Second, func(ctx context.Context) (any, error) {
		spans, err := s.traceLinkSpans(ctx, id)
		if err != nil {
			return nil, err
		}
		return s.resolveTraceLinkIdentity(ctx, id, span, spans, tz), nil
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

// ── Çözümleme ───────────────────────────────────────────────────────────────

// resolveTraceLinkIdentity — span'ler ELDE, log tarafı burada çözülür.
// CH okuması çağıranda (traceLinkSpans) kaldığı için bu fonksiyon sahte
// bir logstore ile uçtan uca test edilebilir.
func (s *Server) resolveTraceLinkIdentity(ctx context.Context, traceID, selected string, spans []chstore.SpanRow, tz string) traceLinkIdentity {
	out := traceLinkIdentity{
		TraceID:    traceID,
		Attrs:      map[string]string{},
		Candidates: []string{},
		Source:     linkIdentitySourceNone,
	}
	if out.TZ = strings.TrimSpace(tz); out.TZ == "" {
		out.TZ = reqid.DefaultTZ
	}
	ordered := orderTraceSpans(spans, selected)
	if len(ordered) == 0 {
		out.Note = "trace'te span yok — kimlik çözümlenemedi"
		return out
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
		return out
	}

	loc := reqid.Location(tz)
	from, to := traceLinkWindow(spans)
	distinct := map[string]bool{}
	// scan — bir sayfadaki kayıtları gezer; İLK bulan kazanır ama sayfanın
	// KALANI da taranır: farklı kimlik sayımı bedava (sayfa zaten elde) ve
	// "requestId dolu, distinct 0" gibi yalan bir alan üretmeyiz.
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
			id, ok := reqid.Find(rec.Body, loc)
			if !ok {
				continue
			}
			distinct[id.Raw] = true
			if !found {
				found = true
				winID = id.Raw
				winSpan = rec.SpanID
				if winSpan == "" {
					winSpan = fallbackSpan
				}
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
			return out
		}
		if id, spanID, ok := scan(page, sp.SpanID); ok {
			out.RequestID, out.SpanID, out.Source = id, spanID, linkIdentitySourceLog
			out.DistinctRequestIDs = len(distinct)
			out.Note = linkIdentityNote(out, len(ordered))
			return out
		}
	}
	// span_id taşımayan loglar için TEK trace-geneli geçiş.
	page, err := logstore.LogsForTrace(ctx, s.logs, traceID, from, to, linkIdentityLogsPerTrace)
	if err != nil {
		out.Partial = true
		out.Note = "log okuması başarısız (" + err.Error() + ") — kimlik span attribute'larından"
		return out
	}
	if id, spanID, ok := scan(page, ""); ok {
		out.RequestID, out.SpanID, out.Source = id, spanID, linkIdentitySourceLog
	}
	out.DistinctRequestIDs = len(distinct)
	out.Note = linkIdentityNote(out, len(ordered))
	return out
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
