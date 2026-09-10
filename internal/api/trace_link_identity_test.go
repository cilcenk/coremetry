package api

// trace_link_identity_test.go — v0.10.566 + v0.10.568 sözleşmesi
// (trace_link_identity.go başlığı).
//
// TÜM DEĞERLER SENTETİK: fonksiyon kodu/müşteri numarası/kurum adı
// depoya girmez (reqid ve correlation_link doktrini).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/reqid"
)

// ── Sentetik kimlikler ──────────────────────────────────────────────────────

const (
	// [Fonksiyon 7][Kanal 6][AltKod 4][Müşteri 10][Tarih 8][Zaman 9][Salt ≥3]
	ridA = "ABCD001" + "059931" + "0513" + "0000000042" + "20260817" + "093440812" + "086"
	ridB = "ABCD001" + "059931" + "0513" + "0000000042" + "20260817" + "093441915" + "087"
)

// ── Sahte logstore ──────────────────────────────────────────────────────────

// scriptLogStore — span_id'ye göre sayfa döndürür; "" anahtarı
// trace-geneli (LogsForTrace) geçişin cevabıdır. Gömülü arayüz,
// dokunulmaması gereken metotlarda panic'ler.
type scriptLogStore struct {
	logstore.Store
	bySpan map[string][]*logstore.LogRecord
	err    error
	calls  []string // Search çağrılarının span_id sırası (kanıt)
	limits []int    // v0.10.569 — istenen sayfa boyutu (trace geçişi tavanı kanıtı)
}

func (f *scriptLogStore) Search(_ context.Context, flt logstore.Filter) (*logstore.Page, error) {
	f.calls = append(f.calls, flt.SpanID)
	f.limits = append(f.limits, flt.Limit)
	if f.err != nil {
		return nil, f.err
	}
	recs := f.bySpan[flt.SpanID]
	return &logstore.Page{Total: len(recs), Logs: recs}, nil
}
func (f *scriptLogStore) Backend() string { return "test" }

func lidRec(spanID, body string) *logstore.LogRecord {
	return &logstore.LogRecord{TraceID: "abc", SpanID: spanID, Body: body}
}

// span — okunabilir kurucu (ns damgaları).
func lidSpan(id, parent string, startNs int64, status string, attrs map[string]string) chstore.SpanRow {
	return chstore.SpanRow{
		TraceID: "abc", SpanID: id, ParentSpanID: parent,
		StartTime: startNs, DurationMs: 5, StatusCode: status, Attributes: attrs,
	}
}

func lidSpanIDs(spans []chstore.SpanRow) string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.SpanID
	}
	return strings.Join(out, ",")
}

// ── orderTraceSpans ─────────────────────────────────────────────────────────

func TestOrderTraceSpans(t *testing.T) {
	// r kök, a/b/c çocuklar; b hatalı. Girdi sırası BİLEREK karışık:
	// CH satır sırası garantili değil, çıktı deterministik olmalı.
	mixed := []chstore.SpanRow{
		lidSpan("c", "r", 300, "ok", nil),
		lidSpan("b", "r", 200, "error", nil),
		lidSpan("r", "", 100, "ok", nil),
		lidSpan("a", "r", 150, "ok", nil),
	}
	cases := []struct {
		name     string
		spans    []chstore.SpanRow
		selected string
		want     string
	}{
		{"seçili yok → hatalı, root, kalanlar", mixed, "", "b,r,a,c"},
		{"seçili var → en başa", mixed, "c", "c,b,r,a"},
		{"seçili trace'te yok → yok sayılır", mixed, "zzz", "b,r,a,c"},
		{"seçili zaten hatalı span → tekilleşir", mixed, "b", "b,r,a,c"},
		{
			"hatalı span yok → root, sonra zaman sırası",
			[]chstore.SpanRow{lidSpan("y", "r", 200, "ok", nil), lidSpan("r", "", 100, "ok", nil), lidSpan("x", "r", 150, "ok", nil)},
			"", "r,x,y",
		},
		{
			"root yok (kesilmiş trace) → en erken span",
			[]chstore.SpanRow{lidSpan("y", "p", 200, "ok", nil), lidSpan("x", "p", 150, "ok", nil)},
			"", "x,y",
		},
		{
			"aynı span_id iki satırda → tekilleştir",
			[]chstore.SpanRow{lidSpan("r", "", 100, "ok", nil), lidSpan("r", "", 100, "ok", nil), lidSpan("x", "r", 150, "ok", nil)},
			"", "r,x",
		},
		{
			"eşit StartTime → SpanID ile deterministik",
			[]chstore.SpanRow{lidSpan("b", "r", 100, "ok", nil), lidSpan("a", "r", 100, "ok", nil), lidSpan("r", "", 100, "ok", nil)},
			"", "r,a,b",
		},
		{"boş girdi", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lidSpanIDs(orderTraceSpans(c.spans, c.selected))
			if got != c.want {
				t.Fatalf("sıra = %q, beklenen %q", got, c.want)
			}
			// Tekillik: hiçbir span iki kez dönmez.
			seen := map[string]bool{}
			for _, s := range orderTraceSpans(c.spans, c.selected) {
				if seen[s.SpanID] {
					t.Fatalf("span %q iki kez döndü: %q", s.SpanID, got)
				}
				seen[s.SpanID] = true
			}
		})
	}
	// Girdi dilimi MUTASYONA UĞRAMAZ (çağıran GetTrace sonucunu paylaşıyor).
	before := lidSpanIDs(mixed)
	_ = orderTraceSpans(mixed, "c")
	if after := lidSpanIDs(mixed); after != before {
		t.Fatalf("girdi dilimi yerinde sıralandı: %q → %q", before, after)
	}
}

// ── mergeSpanAttrs ──────────────────────────────────────────────────────────

func TestMergeSpanAttrs(t *testing.T) {
	ordered := []chstore.SpanRow{
		lidSpan("b", "r", 200, "error", map[string]string{"function_id": "F-ERR", "channel_code": ""}),
		lidSpan("r", "", 100, "ok", map[string]string{"function_id": "F-ROOT", "channel_code": "CH1", "only_root": "R"}),
	}
	got := mergeSpanAttrs(ordered)
	// İlk DOLU değer kazanır: hatalı span önde olduğu için function_id
	// ondan gelir; boş bıraktığı channel_code root'tan dolar.
	if got["function_id"] != "F-ERR" {
		t.Fatalf("öncelikli span'in değeri kazanmalı: %q", got["function_id"])
	}
	if got["channel_code"] != "CH1" {
		t.Fatalf("boş değer kazanmamalı, sonraki span dolduruyor: %q", got["channel_code"])
	}
	if got["only_root"] != "R" {
		t.Fatalf("yalnız sonraki span'de olan anahtar kaybolmamalı: %v", got)
	}

	// Tavan: 200 anahtar. Aşan span'ler haritayı büyütemez ve HANGİ
	// anahtarların girdiği deterministik (span içinde sıralı gezilir).
	big := map[string]string{}
	for i := 0; i < 300; i++ {
		big[lidKeyN(i)] = "v"
	}
	capped := mergeSpanAttrs([]chstore.SpanRow{lidSpan("x", "", 1, "ok", big)})
	if len(capped) != linkIdentityAttrMax {
		t.Fatalf("tavan %d, oysa %d", linkIdentityAttrMax, len(capped))
	}
	again := mergeSpanAttrs([]chstore.SpanRow{lidSpan("x", "", 1, "ok", big)})
	for k := range capped {
		if _, ok := again[k]; !ok {
			t.Fatalf("tavan altındaki anahtar kümesi deterministik değil (%q kayboldu)", k)
		}
	}
}

// keyN — sıralanabilir sentetik anahtar (k000..k299).
func lidKeyN(i int) string {
	d := []byte{'k', byte('0' + i/100), byte('0' + (i/10)%10), byte('0' + i%10)}
	return string(d)
}

// ── traceLinkWindow ─────────────────────────────────────────────────────────

func TestTraceLinkWindow(t *testing.T) {
	// 1e9 ns = 1s; süre ms → ns dönüşümü karışmamalı (v0.6.36 dersi).
	spans := []chstore.SpanRow{
		{StartTime: 2_000_000_000, DurationMs: 1},     // biter 2.001s
		{StartTime: 1_000_000_000, DurationMs: 5_000}, // biter 6s ← en geç
		{StartTime: 3_000_000_000, DurationMs: 0},     // süresiz
	}
	from, to := traceLinkWindow(spans)
	wantFrom := int64(1_000_000_000) - int64(linkIdentityWindowPad)
	wantTo := int64(6_000_000_000) + int64(linkIdentityWindowPad)
	if from.UnixNano() != wantFrom || to.UnixNano() != wantTo {
		t.Fatalf("pencere = [%d,%d], beklenen [%d,%d]", from.UnixNano(), to.UnixNano(), wantFrom, wantTo)
	}
	if f, to2 := traceLinkWindow(nil); !f.IsZero() || !to2.IsZero() {
		t.Fatalf("boş trace → sıfır pencere: %v %v", f, to2)
	}
}

// ── resolveTraceLinkIdentity ────────────────────────────────────────────────

func linkIdentitySpans() []chstore.SpanRow {
	return []chstore.SpanRow{
		lidSpan("r", "", 100, "ok", map[string]string{"function_id": "F-ROOT"}),
		lidSpan("b", "r", 200, "error", map[string]string{"channel_code": "CH1"}),
	}
}

func TestResolveTraceLinkIdentity_LogWins(t *testing.T) {
	// Hatalı span "b" öncelik sırasında ÖNDE: kimliği o veriyor.
	s := &Server{logs: &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		// nil kayıt: sağlıksız bir arka uç dilimde boşluk bırakırsa
		// çözümleyici panic'lemez, atlar.
		"b": {nil, lidRec("b", "islem tamam id="+ridA)},
		"r": {lidRec("r", "kok span id="+ridB)},
	}}}
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.Source != linkIdentitySourceLog || got.RequestID != ridA || got.SpanID != "b" {
		t.Fatalf("log gövdesindeki kimlik kazanmalı: %+v", got)
	}
	if got.Candidates[0] != "b" {
		t.Fatalf("aday sırası hatalı span ile başlamalı: %v", got.Candidates)
	}
	// Attribute yolu KAPANMAZ: şablon hâlâ function_id isteyebilir.
	if got.Attrs["function_id"] != "F-ROOT" || got.Attrs["channel_code"] != "CH1" {
		t.Fatalf("attrs kimlik bulunsa da dolu olmalı: %v", got.Attrs)
	}
	if got.Partial {
		t.Fatalf("sağlıklı okuma partial olmamalı: %+v", got)
	}
	// Seçili span kuralı: operatör "r" satırındaysa kimlik ondan gelir.
	sel := s.resolveTraceLinkIdentity(context.Background(), "abc", "r", linkIdentitySpans(), "", nil)
	if sel.RequestID != ridB || sel.SpanID != "r" {
		t.Fatalf("seçili span önceliği uygulanmadı: %+v", sel)
	}
}

func TestResolveTraceLinkIdentity_DistinctCount(t *testing.T) {
	// Aynı span'in sayfasında İKİ farklı kimlik: ilki kazanır ama sayım
	// dürüst kalır ve not bunu söyler.
	s := &Server{logs: &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"b": {lidRec("b", "id="+ridA), lidRec("b", "id="+ridB), lidRec("b", "id="+ridA)},
	}}}
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.RequestID != ridA {
		t.Fatalf("ilk bulan kazanmalı: %q", got.RequestID)
	}
	if got.DistinctRequestIDs != 2 {
		t.Fatalf("farklı kimlik sayısı = %d, beklenen 2", got.DistinctRequestIDs)
	}
	if !strings.Contains(got.Note, "2 farklı kimlik") {
		t.Fatalf("not çokluğu söylemeli: %q", got.Note)
	}
}

func TestResolveTraceLinkIdentity_TraceWidePass(t *testing.T) {
	// Span'e bağlı log YOK; kimlik yalnız trace-geneli geçişte, span_id'siz
	// bir kayıtta. SpanID boş kalır (uydurulmaz).
	f := &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"": {lidRec("", "gateway id="+ridA)},
	}}
	got := (&Server{logs: f}).resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.Source != linkIdentitySourceLog || got.RequestID != ridA {
		t.Fatalf("trace-geneli geçiş kimliği bulmalı: %+v", got)
	}
	if got.SpanID != "" {
		t.Fatalf("span_id'siz kayıt için span uydurulmamalı: %q", got.SpanID)
	}
	// Maliyet tavanı: 2 span probu + TEK trace geçişi.
	if len(f.calls) != 3 || f.calls[2] != "" {
		t.Fatalf("çağrı sırası/sayısı = %v", f.calls)
	}
}

// Maliyet tavanı: trace kaç span taşırsa taşısın log araması ilk
// linkIdentitySpanProbe adayla sınırlı (+ TEK trace-geneli geçiş).
func TestResolveTraceLinkIdentity_ProbeCap(t *testing.T) {
	var spans []chstore.SpanRow
	for i := 0; i < 9; i++ {
		spans = append(spans, lidSpan(string(rune('a'+i)), "r", int64(100+i), "ok", nil))
	}
	spans = append(spans, lidSpan("r", "", 50, "ok", nil))
	f := &scriptLogStore{}
	got := (&Server{logs: f}).resolveTraceLinkIdentity(context.Background(), "abc", "", spans, "", nil)
	if len(got.Candidates) != linkIdentitySpanProbe {
		t.Fatalf("aday sayısı = %d, tavan %d", len(got.Candidates), linkIdentitySpanProbe)
	}
	if len(f.calls) != linkIdentitySpanProbe+1 {
		t.Fatalf("log çağrısı = %d (%v), beklenen %d span + 1 trace geçişi",
			len(f.calls), f.calls, linkIdentitySpanProbe)
	}
	if !strings.Contains(got.Note, "ilk 5 adayı tarandı") {
		t.Fatalf("not tavanı söylemeli: %q", got.Note)
	}
}

func TestResolveTraceLinkIdentity_NoIDFallsBackToSpan(t *testing.T) {
	s := &Server{logs: &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"b": {lidRec("b", "kimliksiz satır")},
	}}}
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.Source != linkIdentitySourceSpan || got.RequestID != "" {
		t.Fatalf("kimlik yoksa attribute yoluna düşmeli: %+v", got)
	}
	if got.Attrs["function_id"] != "F-ROOT" {
		t.Fatalf("attrs dolu olmalı: %v", got.Attrs)
	}
	if got.Partial {
		t.Fatalf("başarılı ama boş okuma partial DEĞİL: %+v", got)
	}
	if !strings.Contains(got.Note, "bulunamadı") {
		t.Fatalf("not dürüst olmalı: %q", got.Note)
	}
}

func TestResolveTraceLinkIdentity_LogErrorIsPartial(t *testing.T) {
	f := &scriptLogStore{err: errors.New("es down")}
	s := &Server{logs: f}
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	// Hata HIZLI düşer: arka uç zaten yere serilmişken kalan aday
	// span'leri + trace geçişini denemek boş yere ES turu demek.
	if len(f.calls) != 1 {
		t.Fatalf("log hatasında %d çağrı yapıldı, 1 beklenir: %v", len(f.calls), f.calls)
	}
	if got.Source != linkIdentitySourceSpan || !got.Partial {
		t.Fatalf("log hatası fatal değil ama partial: %+v", got)
	}
	if got.Attrs["function_id"] != "F-ROOT" {
		t.Fatalf("attribute yolu ayakta kalmalı: %v", got.Attrs)
	}
	if strings.Contains(got.Note, "bulunamadı") || !strings.Contains(got.Note, "başarısız") {
		t.Fatalf("\"bakamadım\" ile \"yok\" ayrışmalı: %q", got.Note)
	}
}

func TestResolveTraceLinkIdentity_NoLogBackendAndNoSpans(t *testing.T) {
	// logstore yok → span yolu, log adımları hiç koşmaz.
	got := (&Server{}).resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.Source != linkIdentitySourceSpan || got.Partial {
		t.Fatalf("log arka ucu yoksa span yolu (partial değil): %+v", got)
	}
	// Span yok → none, ve attrs/candidates boş AMA nil değil (istemci
	// map/array bekliyor).
	none := (&Server{logs: &scriptLogStore{}}).resolveTraceLinkIdentity(context.Background(), "abc", "", nil, "", nil)
	if none.Source != linkIdentitySourceNone || none.Note == "" {
		t.Fatalf("span yok → none + dürüst not: %+v", none)
	}
	if none.Attrs == nil || none.Candidates == nil {
		t.Fatalf("boş yanıtta bile attrs/candidates nil olmamalı: %+v", none)
	}
}

// ── Cache anahtarı ──────────────────────────────────────────────────────────

// v0.5.187 sınıfı: anahtar TÜM girdileri taşır. tz'nin girdi olması şart —
// saat dilimi kimliğin gömülü zamanını kaydırır, yani AYNI trace için
// BAŞKA bir cevap üretir.
// v0.10.568: keys de girdi — farklı anahtar kümesi farklı `identities`
// üretir, aynı gövdeyi alması cross-poisoning olurdu. SIRA da girdi:
// ["a","b"] ile ["b","a"] aday sırasını değiştirir.
func TestTraceLinkIdentityCacheKey(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range []string{
		traceLinkIdentityCacheKey("t1", "", "", nil),
		traceLinkIdentityCacheKey("t2", "", "", nil),
		traceLinkIdentityCacheKey("t1", "s1", "", nil),
		traceLinkIdentityCacheKey("t1", "s2", "", nil),
		traceLinkIdentityCacheKey("t1", "", "Europe/Istanbul", nil),
		traceLinkIdentityCacheKey("t1", "s1", "Europe/Istanbul", nil),
		traceLinkIdentityCacheKey("t1", "", "", []string{"a"}),
		traceLinkIdentityCacheKey("t1", "", "", []string{"b"}),
		traceLinkIdentityCacheKey("t1", "", "", []string{"a", "b"}),
		traceLinkIdentityCacheKey("t1", "", "", []string{"b", "a"}),
		// Sınır birleşmesi: "a","bc" ile "ab","c" AYNI olamaz (fnvStr her
		// parçadan sonra NUL yazar) — v0.5.187 sınıfının kardeşi.
		traceLinkIdentityCacheKey("t1", "", "", []string{"a", "bc"}),
		traceLinkIdentityCacheKey("t1", "", "", []string{"ab", "c"}),
	} {
		if seen[k] {
			t.Fatalf("anahtar çakıştı: %q", k)
		}
		seen[k] = true
	}
	// Aynı girdi → aynı anahtar (kararlılık).
	if k1, k2 := traceLinkIdentityCacheKey("t1", "s1", "tz", []string{"a", "b"}), traceLinkIdentityCacheKey("t1", "s1", "tz", []string{"a", "b"}); k1 != k2 {
		t.Fatal("anahtar kararsız")
	}
}

// ── Handler + rota ──────────────────────────────────────────────────────────

func TestGetTraceLinkIdentity_Handler(t *testing.T) {
	s := &Server{logs: &scriptLogStore{}, cache: &fakeCache{}, l1: newL1Cache(8), stats: newCacheStats()}
	mux := s.buildMux() // rota GERÇEKTEN kayıtlı mı (defter üzerinden)

	// store yok → span yok → none. Yol: rota → serveCached → GetTrace
	// kapısı → çözümleyici → JSON.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/traces/abc123/link-identity", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, gövde: %s", w.Code, w.Body.String())
	}
	var got traceLinkIdentity
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v — gövde: %s", err, w.Body.String())
	}
	if got.TraceID != "abc123" || got.Source != linkIdentitySourceNone {
		t.Fatalf("beklenen none yanıtı: %+v", got)
	}

	// Girdi hijyeni: anahtar ve log sorgusu serbest metin taşımaz.
	for _, path := range []string{
		"/api/traces/abc123/link-identity?span=" + strings.Repeat("a", 65),
		"/api/traces/abc123/link-identity?span=not-hex",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatalf("%s → status %d, beklenen 400", path, w.Code)
		}
	}
}

// Kablolama kapısı (v0.9.1334 dersi: saf çekirdek yeşil ama çağrıldığı
// pinli değil). Pencere HANDLER GÖVDESİNE hapsedilir — komşu fonksiyonun
// kodu kanıt sayılmasın.
func TestTraceLinkIdentityHandlerWiring(t *testing.T) {
	src, err := os.ReadFile("trace_link_identity.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(string(src), "getTraceLinkIdentity")
	if body == "" {
		t.Fatal("handler gövdesi bulunamadı")
	}
	for _, want := range []string{
		`traceLinkIdentityCacheKey(id, span, tz, keys)`, // anahtar TÜM girdilerle
		`s.serveCached(`,            // hot read cache'i atlanmaz
		`s.traceLinkSpans(ctx, id)`, // spanlar CH'den
		`s.resolveTraceLinkIdentity(ctx, id, span, spans, tz, keys)`, // seçili span + tz + keys taşınır
		`parseLinkIdentityKeys(r.URL.Query().Get("keys"))`,           // keys DOĞRULANIR (400)
		`s.reqidTZSetting(`, // tz ayardan
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handler gövdesinde %q yok — kablolama kopmuş:\n%s", want, body)
		}
	}
	// Spanlar PAYLAŞILAN çözümleyiciden gelmeli (v0.9.632): Tempo-only bir
	// trace'te dış link düğmesi ölü kalmasın.
	fetch := funcBody(string(src), "traceLinkSpans")
	if !strings.Contains(fetch, "s.resolveTraceSpans(ctx, id)") {
		t.Errorf("traceLinkSpans paylaşılan çözümleyiciyi kullanmalı:\n%s", fetch)
	}
	// Rota kaydı deftere gider; api.go büyümez (v0.10.247).
	if !strings.Contains(string(src), `registerRoutesExtra("trace-link-identity"`) {
		t.Error("rota defterden kaydolmalı")
	}
	api, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(api), "link-identity") {
		t.Error("api.go link-identity rotasını tanımamalı (kayıt route_registry defterinde)")
	}
}

// v0.10.567 — dilim ADI taşınır (çözülmüş Location değil): sunucuda tzdata
// yoksa reqid.Location "+03"a düşer ve o ad tarayıcının Intl'ine verilemez.
// Boş ayar → Europe/Istanbul.
func TestResolveTraceLinkIdentityCarriesTZ(t *testing.T) {
	s := &Server{}
	if got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), " Europe/Berlin ", nil); got.TZ != "Europe/Berlin" {
		t.Fatalf("ayar dilimi taşınmalı (trim): %q", got.TZ)
	}
	if got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil); got.TZ != reqid.DefaultTZ {
		t.Fatalf("boş ayar → %s, geldi %q", reqid.DefaultTZ, got.TZ)
	}
	// Span yokken de dilim dolu: FE her hâlde bir dilim görmeli.
	if got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", nil, "", nil); got.TZ != reqid.DefaultTZ {
		t.Fatalf("span yokken de dilim dolu olmalı: %+v", got)
	}
}

// ── v0.10.568 — kimlik adayları ─────────────────────────────────────────────
//
// Operatör (2026-09-08): "Farklı function_id'ler alt span'lerde ama aynı
// trace'te olabilir… hangisine gitmek istersin diye seçenek verelim."
// v0.10.566 kuralı kazananı SESSİZCE seçiyordu; buradaki testler kuralın
// AYNI kaldığını ama tüm adayların GÖRÜNÜR olduğunu çiviler.

// lidNamedSpan — ad/servis taşıyan span (aday satırı bunları gösterir).
func lidNamedSpan(id, parent string, startNs int64, status, name, svc string, attrs map[string]string) chstore.SpanRow {
	sp := lidSpan(id, parent, startNs, status, attrs)
	sp.Name, sp.ServiceName = name, svc
	return sp
}

// lidCandStr — aday satırının okunabilir özeti (assert biçimi).
func lidCandStr(c traceLinkCandidate) string {
	out := fmt.Sprintf("%s=%s|%s|%s|%s", c.Key, c.Value, c.Source, c.Role, c.SpanID)
	if c.IsError {
		out += "|err"
	}
	if c.Used {
		out += "|used"
	}
	return out
}

func lidCandStrs(cs []traceLinkCandidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = lidCandStr(c)
	}
	return out
}

// ── parseLinkIdentityKeys ───────────────────────────────────────────────────

// Anahtar ADLARI ürüne gömülmez (istemci şablonun Requires'ından
// türetir), bu yüzden burada beyaz liste değil HİJYEN sınanır: serbest
// metin hem cache anahtarına hem yanıta giriyor.
func TestParseLinkIdentityKeys(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{"boş → nil", "", nil, false},
		{"yalnız boşluk → nil", "   ", nil, false},
		{"tek anahtar", "fn", []string{"fn"}, false},
		{"boşluklar kırpılır", " fn , ch ", []string{"fn", "ch"}, false},
		{"boş parçalar atlanır", "fn,,ch,", []string{"fn", "ch"}, false},
		{"tekrar tekilleşir, SIRA korunur", "ch,fn,ch", []string{"ch", "fn"}, false},
		{"nokta/tire/alt çizgi serbest", "a.b-c_d,x9", []string{"a.b-c_d", "x9"}, false},
		{"tavan sınırında (5)", "a,b,c,d,e", []string{"a", "b", "c", "d", "e"}, false},
		{"tavan aşımı (6) → hata", "a,b,c,d,e,f", nil, true},
		{"tekrarlar tavanı doldurmaz", "a,a,b,b,c,c,d,d,e,e", []string{"a", "b", "c", "d", "e"}, false},
		{"boşluklu anahtar → hata", "a b", nil, true},
		{"iki nokta üst üste → hata (cache anahtarı ayracı)", "a:b", nil, true},
		{"yüzde/format kaçışı → hata", "a%s", nil, true},
		{"slash → hata", "a/b", nil, true},
		{"64 karakter sınırda", strings.Repeat("k", 64), []string{strings.Repeat("k", 64)}, false},
		{"65 karakter → hata", strings.Repeat("k", 65), nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseLinkIdentityKeys(c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, beklenen hata = %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("keys = %v, beklenen %v", got, c.want)
			}
		})
	}
	// Hata metni DOĞRULANMAMIŞ uzun girdiyi yankılamaz (yanıt gövdesine
	// sınırsız metin basılmaz).
	if _, err := parseLinkIdentityKeys(strings.Repeat("z", 500)); err == nil {
		t.Fatal("uzun anahtar hata vermeli")
	} else if strings.Contains(err.Error(), strings.Repeat("z", 100)) {
		t.Fatalf("hata metni girdiyi yankılamamalı: %q", err.Error())
	}
}

// ── linkIdentitySpanRoles ───────────────────────────────────────────────────

func TestLinkIdentitySpanRoles(t *testing.T) {
	cases := []struct {
		name     string
		spans    []chstore.SpanRow
		selected string
		want     string // ordered sırasıyla "spanID:rol" listesi
	}{
		{
			"hatalı + root + kalanlar",
			[]chstore.SpanRow{lidSpan("r", "", 100, "ok", nil), lidSpan("b", "r", 200, "error", nil), lidSpan("c", "r", 300, "ok", nil)},
			"", "b:error,r:root,c:span",
		},
		{
			"seçili her şeyi ezer",
			[]chstore.SpanRow{lidSpan("r", "", 100, "ok", nil), lidSpan("b", "r", 200, "error", nil)},
			"b", "b:selected,r:root",
		},
		{
			"root hem de hatalıysa error kazanır",
			[]chstore.SpanRow{lidSpan("r", "", 100, "error", nil), lidSpan("c", "r", 200, "ok", nil)},
			"", "r:error,c:span",
		},
		{
			"iki hatalı → yalnız EN ERKEN error",
			[]chstore.SpanRow{lidSpan("r", "", 100, "ok", nil), lidSpan("b", "r", 200, "error", nil), lidSpan("d", "r", 300, "error", nil)},
			"", "b:error,r:root,d:span",
		},
		{
			"root yok (kesilmiş trace) → root rolü kimseye verilmez",
			[]chstore.SpanRow{lidSpan("x", "p", 150, "ok", nil), lidSpan("y", "p", 200, "ok", nil)},
			"", "x:span,y:span",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ordered := orderTraceSpans(c.spans, c.selected)
			roles := linkIdentitySpanRoles(ordered, c.selected)
			if len(roles) != len(ordered) {
				t.Fatalf("rol dilimi uzunluğu %d, span %d", len(roles), len(ordered))
			}
			parts := make([]string, len(ordered))
			for i, sp := range ordered {
				parts[i] = sp.SpanID + ":" + roles[i]
			}
			if got := strings.Join(parts, ","); got != c.want {
				t.Fatalf("roller = %q, beklenen %q", got, c.want)
			}
		})
	}
}

// ── buildLinkIdentityCandidates ─────────────────────────────────────────────

func TestBuildLinkIdentityCandidates(t *testing.T) {
	// r kök(ok, fn=F-ROOT), b hatalı(fn=F-ERR, ch=CH1), c(fn=F-ROOT tekrar).
	base := []chstore.SpanRow{
		lidNamedSpan("r", "", 100, "ok", "GET /", "edge", map[string]string{"fn": "F-ROOT"}),
		lidNamedSpan("b", "r", 200, "error", "POST /pay", "pay", map[string]string{"fn": "F-ERR", "ch": "CH1"}),
		lidNamedSpan("c", "r", 300, "ok", "GET /x", "edge", map[string]string{"fn": "F-ROOT"}),
	}
	cases := []struct {
		name        string
		spans       []chstore.SpanRow
		selected    string
		hits        []linkIdentityLogHit
		keys        []string
		usedReqID   string
		want        []string
		wantDropped int
	}{
		{
			name:  "request_id ÖNCE, sonra span attribute'ları (öncelik sırası)",
			spans: base, hits: []linkIdentityLogHit{{Value: ridA, SpanID: "b"}},
			keys: []string{"fn"}, usedReqID: ridA,
			want: []string{
				"request_id=" + ridA + "|log|error|b|err|used",
				"fn=F-ERR|span|error|b|err|used",
				"fn=F-ROOT|span|root|r",
			},
		},
		{
			name:  "aynı (Key,Value) tekilleşir — c'nin F-ROOT'u ikinci kez sorulmaz",
			spans: base, keys: []string{"fn"},
			want: []string{"fn=F-ERR|span|error|b|err|used", "fn=F-ROOT|span|root|r"},
		},
		{
			name:  "çok anahtar → anahtar başına BİR used",
			spans: base, keys: []string{"fn", "ch"},
			want: []string{
				"fn=F-ERR|span|error|b|err|used",
				"ch=CH1|span|error|b|err|used",
				"fn=F-ROOT|span|root|r",
			},
		},
		{
			name:  "anahtar sırası cevabın parçası (ch önce istendi)",
			spans: base, keys: []string{"ch", "fn"},
			want: []string{
				"ch=CH1|span|error|b|err|used",
				"fn=F-ERR|span|error|b|err|used",
				"fn=F-ROOT|span|root|r",
			},
		},
		{
			name:  "keys boş → yalnız request_id adayları",
			spans: base, hits: []linkIdentityLogHit{{Value: ridA, SpanID: "b"}, {Value: ridB, SpanID: "r"}},
			usedReqID: ridA,
			want: []string{
				"request_id=" + ridA + "|log|error|b|err|used",
				"request_id=" + ridB + "|log|root|r",
			},
		},
		{
			name:  "seçili span rolü adaya taşınır",
			spans: base, selected: "c", keys: []string{"fn"},
			// used seçili span'den gelir: mergeSpanAttrs de öyle yapar
			// (aynı kural, tek kaynak).
			want: []string{"fn=F-ROOT|span|selected|c|used", "fn=F-ERR|span|error|b|err"},
		},
		{
			name:  "span'i bilinmeyen kimlik → rol UYDURULMAZ",
			spans: base, hits: []linkIdentityLogHit{{Value: ridA}},
			want: []string{"request_id=" + ridA + "|log|span||used"},
		},
		{
			name:  "trace'te olmayan span_id → ad/servis uydurulmaz",
			spans: base, hits: []linkIdentityLogHit{{Value: ridA, SpanID: "zzz"}},
			want: []string{"request_id=" + ridA + "|log|span||used"},
		},
		{
			name:  "used = ÇÖZÜLEN kimlik, listedeki ilk değil",
			spans: base, hits: []linkIdentityLogHit{{Value: ridB, SpanID: "r"}, {Value: ridA, SpanID: "b"}},
			usedReqID: ridA,
			want: []string{
				"request_id=" + ridB + "|log|root|r",
				"request_id=" + ridA + "|log|error|b|err|used",
			},
		},
		{
			name:  "çözülen kimlik yoksa ilk aday used",
			spans: base, hits: []linkIdentityLogHit{{Value: ridB, SpanID: "r"}, {Value: ridA, SpanID: "b"}},
			want: []string{
				"request_id=" + ridB + "|log|root|r|used",
				"request_id=" + ridA + "|log|error|b|err",
			},
		},
		{
			name:  "boş değer aday olmaz",
			spans: []chstore.SpanRow{lidNamedSpan("r", "", 100, "ok", "GET /", "edge", map[string]string{"fn": ""})},
			keys:  []string{"fn"}, want: nil,
		},
		{
			name:  "istenmeyen anahtar sızmaz",
			spans: base, keys: []string{"ch"},
			want: []string{"ch=CH1|span|error|b|err|used"},
		},
		{
			name:  "span yok → boş liste",
			spans: nil, keys: []string{"fn"}, want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ordered := orderTraceSpans(c.spans, c.selected)
			got, dropped := buildLinkIdentityCandidates(ordered, c.selected, c.hits, c.keys, c.usedReqID)
			if got == nil {
				t.Fatal("aday listesi nil olmamalı (istemci dizi bekliyor)")
			}
			if strings.Join(lidCandStrs(got), "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("adaylar =\n%s\nbeklenen =\n%s", strings.Join(lidCandStrs(got), "\n"), strings.Join(c.want, "\n"))
			}
			if dropped != c.wantDropped {
				t.Fatalf("dropped = %d, beklenen %d", dropped, c.wantDropped)
			}
			// Anahtar başına EN ÇOK bir used (bayrak "kullanılan"ı gösterir).
			usedPerKey := map[string]int{}
			for _, k := range got {
				if k.Used {
					usedPerKey[k.Key]++
				}
			}
			for k, n := range usedPerKey {
				if n > 1 {
					t.Fatalf("%q anahtarında %d used bayrağı var", k, n)
				}
			}
		})
	}
}

// Tavan: menü 10 satırdan sonra seçim aracı olmaktan çıkar; kesilen sayı
// DÜRÜSTÇE raporlanır ve tekilleştirme tavandan sonra da sürer.
func TestBuildLinkIdentityCandidates_Cap(t *testing.T) {
	var hits []linkIdentityLogHit
	for i := 0; i < 13; i++ {
		hits = append(hits, linkIdentityLogHit{Value: fmt.Sprintf("RID-%02d", i)})
	}
	// Tekrarlar tavanı yemez, "+N" sayısını da şişirmez.
	hits = append(hits, linkIdentityLogHit{Value: "RID-00"})
	got, dropped := buildLinkIdentityCandidates(nil, "", hits, nil, "")
	if len(got) != linkIdentityCandidateMax {
		t.Fatalf("aday sayısı = %d, tavan %d", len(got), linkIdentityCandidateMax)
	}
	if dropped != 3 {
		t.Fatalf("dropped = %d, beklenen 3", dropped)
	}
	if note := linkIdentityDroppedNote("kimlik çözüldü", dropped); note != "kimlik çözüldü; +3 aday gösterilmiyor" {
		t.Fatalf("not = %q", note)
	}
	if note := linkIdentityDroppedNote("", 2); note != "+2 aday gösterilmiyor" {
		t.Fatalf("boş not = %q", note)
	}
	if note := linkIdentityDroppedNote("x", 0); note != "x" {
		t.Fatalf("kesilme yokken not değişmemeli: %q", note)
	}
}

// ── resolve → identities kablolaması ────────────────────────────────────────

func TestResolveTraceLinkIdentity_Identities(t *testing.T) {
	s := &Server{logs: &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"b": {lidRec("b", "islem tamam id="+ridA)},
	}}}
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", []string{"function_id", "channel_code"})
	if len(got.Identities) == 0 {
		t.Fatalf("aday listesi boş: %+v", got)
	}
	// request_id ÖNCE ve çözülen kimlikle aynı.
	first := got.Identities[0]
	if first.Key != linkIdentityKeyRequestID || first.Value != got.RequestID || !first.Used {
		t.Fatalf("ilk aday çözülen request_id olmalı: %+v", first)
	}
	// Mevcut alanlar DEĞİŞMEDİ.
	if got.Source != linkIdentitySourceLog || got.RequestID != ridA || got.SpanID != "b" {
		t.Fatalf("v0.10.566 davranışı korunmalı: %+v", got)
	}
	// Anahtar başına used aday, bugünkü linkin kullandığı değerdir
	// (mergeSpanAttrs = ilk dolu değer). Sözleşmenin ta kendisi.
	for _, c := range got.Identities {
		if c.Used && c.Source == linkIdentitySourceSpan && got.Attrs[c.Key] != c.Value {
			t.Fatalf("used aday attrs ile ayrıştı: %s=%s, attrs=%q", c.Key, c.Value, got.Attrs[c.Key])
		}
	}
	// keys istenmemişse span adayı üretilmez.
	bare := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	for _, c := range bare.Identities {
		if c.Source != linkIdentitySourceLog {
			t.Fatalf("keys yokken span adayı sızdı: %+v", c)
		}
	}
}

// Aday listesi log PROBE tavanına takılmaz: attribute'lar zaten bellekte,
// ES turu değil. 5 span probu LOG maliyeti içindi.
func TestResolveTraceLinkIdentity_IdentitiesIgnoreProbeCap(t *testing.T) {
	var spans []chstore.SpanRow
	for i := 0; i < 9; i++ {
		spans = append(spans, lidSpan(string(rune('a'+i)), "r", int64(100+i), "ok",
			map[string]string{"fn": fmt.Sprintf("F%d", i)}))
	}
	spans = append(spans, lidSpan("r", "", 50, "ok", map[string]string{"fn": "F-ROOT"}))
	f := &scriptLogStore{}
	got := (&Server{logs: f}).resolveTraceLinkIdentity(context.Background(), "abc", "", spans, "", []string{"fn"})
	// Log maliyeti DEĞİŞMEDİ: 5 span probu + 1 trace geçişi.
	if len(f.calls) != linkIdentitySpanProbe+1 || len(got.Candidates) != linkIdentitySpanProbe {
		t.Fatalf("log maliyeti değişmiş: calls=%v candidates=%v", f.calls, got.Candidates)
	}
	// Ama aday listesi 10 span'in hepsini gördü (tavan tam 10).
	if len(got.Identities) != linkIdentityCandidateMax {
		t.Fatalf("aday sayısı = %d, beklenen %d", len(got.Identities), linkIdentityCandidateMax)
	}
	seen := map[string]bool{}
	for _, c := range got.Identities {
		seen[c.Value] = true
	}
	if !seen["F8"] {
		t.Fatalf("probe tavanının ötesindeki span adaya girmeli: %v", lidCandStrs(got.Identities))
	}
	if got.Identities[0].Value != "F-ROOT" || !got.Identities[0].Used {
		t.Fatalf("root önce ve used olmalı: %+v", got.Identities[0])
	}
}

// Kimlik çözülemeyen/kesilen yollarda da aday listesi taşınır: boş menü
// "aday yok" yalanı olurdu.
func TestResolveTraceLinkIdentity_IdentitiesOnEveryPath(t *testing.T) {
	keys := []string{"function_id"}
	cases := []struct {
		name string
		srv  *Server
		want bool // span adayı beklenir mi
	}{
		{"log arka ucu yok", &Server{}, true},
		{"log okuması düştü (partial)", &Server{logs: &scriptLogStore{err: errors.New("es down")}}, true},
		{"kimlik yok → attribute yolu", &Server{logs: &scriptLogStore{}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.srv.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", keys)
			if got.Identities == nil {
				t.Fatal("identities nil olmamalı")
			}
			if c.want && len(got.Identities) == 0 {
				t.Fatalf("span adayları taşınmalı: %+v", got)
			}
			if len(got.Identities) > 0 && got.Identities[0].Value != "F-ROOT" {
				t.Fatalf("aday değeri beklenmedik: %v", lidCandStrs(got.Identities))
			}
		})
	}
	// Span yok → boş AMA nil değil (JSON'da [] olmalı).
	none := (&Server{logs: &scriptLogStore{}}).resolveTraceLinkIdentity(context.Background(), "abc", "", nil, "", keys)
	if none.Identities == nil || len(none.Identities) != 0 {
		t.Fatalf("span yokken boş dilim beklenir: %+v", none.Identities)
	}
}

// ── Handler: keys doğrulaması + JSON şekli ──────────────────────────────────

func TestGetTraceLinkIdentity_Keys(t *testing.T) {
	s := &Server{logs: &scriptLogStore{}, cache: &fakeCache{}, l1: newL1Cache(8), stats: newCacheStats()}
	mux := s.buildMux()

	for _, path := range []string{
		"/api/traces/abc123/link-identity?keys=a,b,c,d,e,f", // 6 anahtar
		"/api/traces/abc123/link-identity?keys=" + strings.Repeat("k", 65),
		"/api/traces/abc123/link-identity?keys=bad%20key",
		"/api/traces/abc123/link-identity?keys=a:b",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatalf("%s → status %d, beklenen 400", path, w.Code)
		}
	}

	// Geçerli keys → 200 ve identities JSON'da DİZİ (null değil).
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/traces/abc123/link-identity?keys=function_id,channel_code", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, gövde: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"identities":[]`) {
		t.Fatalf("identities dizi olmalı (null değil): %s", w.Body.String())
	}
	var got traceLinkIdentity
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Identities == nil {
		t.Fatalf("identities nil: %s", w.Body.String())
	}
}

// v0.10.569 — operatör: "request_id logta herhangi bir yerde varsa bulacak
// değil mi". İki iyileştirme: (1) katı biçim tutmazsa GEVŞEK ikinci tur
// (kimliğe benzeyen token; link üretilir ama biçim İDDİA EDİLMEZ),
// (2) trace-geneli geçiş 50 → 150 satır.
//
// ridLoose: ridA'nın şekli ama takvim dışı tarih (13. ay, 45. gün) — 47
// karakter, ≥33 rakam, yani FindLooseToken yakalar, Parse yakalamaz.
const ridLoose = "ABCD001" + "059931" + "0513" + "0000000042" + "20261345" + "093440812" + "086"

func TestResolveTraceLinkIdentity_LooseFallback(t *testing.T) {
	f := &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"b": {lidRec("b", `{"msg":"basarisiz","islem":"`+ridLoose+`"}`)},
	}}
	got := (&Server{logs: f}).resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", []string{"function_id"})
	if got.Source != linkIdentitySourceLog || got.RequestID != ridLoose {
		t.Fatalf("gevşek token linke girmeli: %+v", got)
	}
	if !got.RequestIDLoose {
		t.Fatal("gevşek eşleşme İLAN EDİLMELİ (buldum ≠ doğruladım)")
	}
	if !strings.Contains(got.Note, "BİÇİM DOĞRULANAMADI") {
		t.Fatalf("not gevşekliği söylemeli: %q", got.Note)
	}
	var loose *traceLinkCandidate
	for i := range got.Identities {
		if got.Identities[i].Source == linkIdentitySourceLog {
			loose = &got.Identities[i]
			break
		}
	}
	if loose == nil || !loose.Loose {
		t.Fatalf("aday gevşek işaretlenmeli: %+v", got.Identities)
	}
	// v0.10.572 — anahtar logdaki GERÇEK alan adı ("islem"), uydurulmuş
	// "request_id" değil.
	if loose.Key != "islem" {
		t.Fatalf("aday anahtarı log alan adı olmalı: %q", loose.Key)
	}
	// Attribute yolu kapanmaz.
	if got.Attrs["function_id"] != "F-ROOT" {
		t.Fatalf("attrs dolu kalmalı: %v", got.Attrs)
	}
	// Trace geçişi 150 satır ister (kimlik "herhangi bir yerde" olabilir).
	if len(f.limits) == 0 || f.limits[len(f.limits)-1] != linkIdentityLogsPerTrace || linkIdentityLogsPerTrace != 150 {
		t.Fatalf("trace geçişi tavanı: %v (sabit %d)", f.limits, linkIdentityLogsPerTrace)
	}
}

func TestResolveTraceLinkIdentity_StrictBeatsLoose(t *testing.T) {
	// Gevşek token ÖNDEKİ span'de, katı olan arkadaki: katı yine kazanır.
	got := (&Server{logs: &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"b": {lidRec("b", "bozuk "+ridLoose)},
		"r": {lidRec("r", "saglam "+ridA)},
	}}}).resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.RequestID != ridA || got.RequestIDLoose {
		t.Fatalf("katı eşleşme gevşeğin önüne geçmeli: %+v", got)
	}
}

// v0.10.572 — operatör: "bazen requestid BsaRequestId olarak logta yazıyor".
// ARAMA ad-bağımsız (şekil tanınır); bu yalnız ETİKET: token'ı taşıyan JSON
// alanının gerçek adı okunur, çözülemezse ad UYDURULMAZ.
func TestJSONKeyForToken(t *testing.T) {
	const tok = "BKRM032060203MGie0018328324206090816550266120"
	cases := []struct{ name, body, want string }{
		{"bitişik", `{"BsaRequestId":"` + tok + `","CustomerNumber":"1"}`, "BsaRequestId"},
		{"boşluklu", `{ "BsaRequestId" : "` + tok + `" }`, "BsaRequestId"},
		{"snake", `{"request_id":"` + tok + `"}`, "request_id"},
		{"nokta/tire", `{"bsa.request-id":"` + tok + `"}`, "bsa.request-id"},
		{"düz metin", "islem tamam id=" + tok, ""},
		{"tırnaksız değer", `{"k":` + tok + `}`, ""},
		{"iki nokta yok", `{"k" "` + tok + `"}`, ""},
		{"anahtarda boşluk", `{"iki kelime":"` + tok + `"}`, ""},
		{"token yok", `{"k":"v"}`, ""},
		{"başta", tok, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jsonKeyForToken(c.body, tok); got != c.want {
				t.Fatalf("jsonKeyForToken = %q, beklenen %q", got, c.want)
			}
		})
	}
	if jsonKeyForToken(`{"k":"x"}`, "") != "" {
		t.Fatal("boş token → boş ad")
	}
}

// Katı yolda da gerçek alan adı taşınır.
func TestResolveTraceLinkIdentity_LogKeyIsRealFieldName(t *testing.T) {
	got := (&Server{logs: &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"b": {lidRec("b", `{"BsaRequestId":"`+ridA+`","SessionId":"x"}`)},
	}}}).resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.RequestID != ridA {
		t.Fatalf("kimlik bulunmalı: %+v", got)
	}
	for _, c := range got.Identities {
		if c.Source == linkIdentitySourceLog {
			if c.Key != "BsaRequestId" {
				t.Fatalf("anahtar logdaki ad olmalı: %q", c.Key)
			}
			return
		}
	}
	t.Fatal("log adayı yok")
}
