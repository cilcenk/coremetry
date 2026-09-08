package api

// trace_link_identity_test.go — v0.10.566 sözleşmesi
// (trace_link_identity.go başlığı).
//
// TÜM DEĞERLER SENTETİK: fonksiyon kodu/müşteri numarası/kurum adı
// depoya girmez (reqid ve correlation_link doktrini).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
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
}

func (f *scriptLogStore) Search(_ context.Context, flt logstore.Filter) (*logstore.Page, error) {
	f.calls = append(f.calls, flt.SpanID)
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
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "")
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
	sel := s.resolveTraceLinkIdentity(context.Background(), "abc", "r", linkIdentitySpans(), "")
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
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "")
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
	got := (&Server{logs: f}).resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "")
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
	got := (&Server{logs: f}).resolveTraceLinkIdentity(context.Background(), "abc", "", spans, "")
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
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "")
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
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "")
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
	got := (&Server{}).resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "")
	if got.Source != linkIdentitySourceSpan || got.Partial {
		t.Fatalf("log arka ucu yoksa span yolu (partial değil): %+v", got)
	}
	// Span yok → none, ve attrs/candidates boş AMA nil değil (istemci
	// map/array bekliyor).
	none := (&Server{logs: &scriptLogStore{}}).resolveTraceLinkIdentity(context.Background(), "abc", "", nil, "")
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
func TestTraceLinkIdentityCacheKey(t *testing.T) {
	keys := map[string]bool{}
	for _, k := range []string{
		traceLinkIdentityCacheKey("t1", "", ""),
		traceLinkIdentityCacheKey("t2", "", ""),
		traceLinkIdentityCacheKey("t1", "s1", ""),
		traceLinkIdentityCacheKey("t1", "s2", ""),
		traceLinkIdentityCacheKey("t1", "", "Europe/Istanbul"),
		traceLinkIdentityCacheKey("t1", "s1", "Europe/Istanbul"),
	} {
		if keys[k] {
			t.Fatalf("anahtar çakıştı: %q", k)
		}
		keys[k] = true
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
		`traceLinkIdentityCacheKey(id, span, tz)`, // anahtar TÜM girdilerle
		`s.serveCached(`,            // hot read cache'i atlanmaz
		`s.traceLinkSpans(ctx, id)`, // spanlar CH'den
		`s.resolveTraceLinkIdentity(ctx, id, span, spans, tz)`, // seçili span + tz taşınır
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
