package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// regex_and_test.go — v0.9.797: `=~` operatörü + Rule.And.
//
// İkisi de metric_exclusions köprüsü için eklendi (bkz.
// internal/api/metric_exclusions.go): dışlama kuralı "şu METRİĞİN şu
// ROUTE deseni" demek zorunda ve v0.9.796'nın tek-koşullu, regex'siz
// dili bunu YAZAMIYORDU. ConvertMetrics'e ikinci bir drop motoru
// koymak yerine BU motor genişletildi — tek motor, tek sayaç.
//
// Yollar JENERİK: müşteri/kurum yolu depoya girmez.

// dpWithRoute — http.route attr'ı taşıyan bir metrik datapoint'i.
func dpWithRoute(metric, route string) *chstore.MetricPoint {
	return &chstore.MetricPoint{
		Metric:     metric,
		AttrKeys:   []string{"http.method", "http.route"},
		AttrValues: []string{"GET", route},
	}
}

func TestMatchOpMatchesIsUnanchoredRE2(t *testing.T) {
	tests := []struct {
		pattern, got string
		want         bool
	}{
		// ANKORSUZ: desen yolun herhangi bir yerinde eşleşir. CH match()
		// ile aynı semantik — okuma filtresi ile ingest drop'unun AYNI
		// kümeyi seçmesi buna bağlı.
		{"/health", "/v1/health/checkStartup", true},
		{"^/health", "/v1/health/checkStartup", false},
		{"^/health", "/health/checkStartup", true},
		{"/health$", "/api/health", true},
		{"/health", "/api/orders", false},
		// Boş değer (route attr'ı olmayan datapoint) eşleşmez → satır KALIR.
		{"^/health", "", false},
		// Bozuk desen HİÇBİR ŞEYİ eşleştirmez (fail-closed): fail-open
		// olsaydı bozuk bir desen bütün datapoint'leri düşürürdü.
		{"([a-", "/health", false},
	}
	for _, tc := range tests {
		if got := matchOp(OpMatches, tc.got, tc.pattern); got != tc.want {
			t.Errorf("matchOp(=~, %q, %q) = %v, beklenen %v", tc.got, tc.pattern, got, tc.want)
		}
	}
}

// Derlenmiş regex önbelleği: aynı desen iki kez derlenmez ve sonuç
// çağrılar arası KARARLI (sıcak yolda datapoint başına tek sync.Map
// okuması).
func TestCachedRegexStable(t *testing.T) {
	first := cachedRegex("^/health")
	if first == nil {
		t.Fatal("geçerli desen nil döndü")
	}
	if second := cachedRegex("^/health"); second != first {
		t.Error("aynı desen iki farklı regex örneği üretti — önbellek çalışmıyor")
	}
	if cachedRegex("([a-") != nil {
		t.Error("bozuk desen nil dönmeliydi")
	}
}

// Rule.And — metric_exclusions köprüsünün ürettiği şekil. `and` DÜŞERSE
// tek bir metrik için kurulan dışlama BÜTÜN metriklerin o route'unu
// yazılmaz yapar (istenmeyen veri kaybı).
func TestAcceptMetricWithAndConditions(t *testing.T) {
	rule := Rule{
		ID: "metric-excl-1", Name: "metric-excl", Kind: KindDrop, Signal: SignalMetrics, Enabled: true,
		When: Condition{Key: "http.route", Op: OpMatches, Value: "^/health"},
		And:  []Condition{{Key: "metric", Op: OpEq, Value: "http.server.duration"}},
	}
	e := &Engine{rules: []Rule{rule}}

	tests := []struct {
		name          string
		metric, route string
		wantKeep      bool
	}{
		{"hedef metrik + eşleşen route → düşer", "http.server.duration", "/health/checkStartup", false},
		{"hedef metrik + başka route → kalır", "http.server.duration", "/api/orders", true},
		{"başka metrik + eşleşen route → KALIR", "db.client.duration", "/health/checkStartup", true},
		{"route attr'ı yok → kalır", "http.server.duration", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.AcceptMetric(dpWithRoute(tc.metric, tc.route)); got != tc.wantKeep {
				t.Errorf("AcceptMetric = %v, beklenen %v", got, tc.wantKeep)
			}
		})
	}
}

// Boş And = v0.9.796 davranışı bayt-bayt: tek predicate, ek kısıt yok.
func TestEmptyAndIsUnconstrained(t *testing.T) {
	if !matchAll(nil, func(Condition) bool { return false }) {
		t.Error("boş And listesi TRUE dönmeli (koşul yok = kısıt yok)")
	}
	rule := Rule{
		ID: "m1", Kind: KindDrop, Signal: SignalMetrics, Enabled: true,
		When: Condition{Key: "metric", Op: OpStartsWith, Value: "debug."},
	}
	e := &Engine{rules: []Rule{rule}}
	if e.AcceptMetric(&chstore.MetricPoint{Metric: "debug.heap"}) {
		t.Error("And'siz kural eskisi gibi düşürmeli")
	}
}

// And span ve log yollarında da geçerli — sinyale göre sessizce
// uygulanMAyan bir alan tuzak olurdu.
func TestAndAppliesToSpansAndLogs(t *testing.T) {
	spanRule := Rule{
		ID: "s1", Kind: KindDrop, Signal: SignalSpans, Enabled: true,
		When: Condition{Key: "service.name", Op: OpEq, Value: "api-gateway"},
		And:  []Condition{{Key: "name", Op: OpMatches, Value: "^GET /health"}},
	}
	e := &Engine{rules: []Rule{spanRule}}
	if e.AcceptSpan(&chstore.Span{ServiceName: "api-gateway", Name: "GET /health/checkStartup"}) {
		t.Error("span: iki koşul da sağlandı, düşmeliydi")
	}
	if !e.AcceptSpan(&chstore.Span{ServiceName: "api-gateway", Name: "GET /api/orders"}) {
		t.Error("span: ikinci koşul sağlanmadı, kalmalıydı")
	}

	logRule := Rule{
		ID: "l1", Kind: KindDrop, Signal: SignalLogs, Enabled: true,
		When: Condition{Key: "service.name", Op: OpEq, Value: "api-gateway"},
		And:  []Condition{{Key: "severity_text", Op: OpEq, Value: "DEBUG"}},
	}
	e2 := &Engine{rules: []Rule{logRule}}
	if e2.AcceptLog(&chstore.Log{ServiceName: "api-gateway", SeverityText: "DEBUG"}) {
		t.Error("log: iki koşul da sağlandı, düşmeliydi")
	}
	if !e2.AcceptLog(&chstore.Log{ServiceName: "api-gateway", SeverityText: "INFO"}) {
		t.Error("log: ikinci koşul sağlanmadı, kalmalıydı")
	}
}

// ── Kayıt kapısı ──────────────────────────────────────────────────────

type memStore struct{ raw []byte }

func (m *memStore) GetPipelineRulesRaw(context.Context) ([]byte, error) { return m.raw, nil }
func (m *memStore) PutPipelineRulesRaw(_ context.Context, raw []byte) error {
	m.raw = raw
	return nil
}

// Bozuk RE2 KAYIT anında reddedilir. Kabul edilseydi kural sıcak yolda
// "hiçbir şeyi eşleştirmeyen" sessiz bir kural olurdu: operatör drop
// kuralını kurar, hiçbir şey düşmez, sebep görünmez.
func TestUpsertRejectsBadPattern(t *testing.T) {
	e := New()
	st := &memStore{}
	bad := Rule{
		Name: "bozuk", Kind: KindDrop, Signal: SignalMetrics, Enabled: true,
		When: Condition{Key: "http.route", Op: OpMatches, Value: "([a-"},
	}
	if _, err := e.Upsert(context.Background(), st, bad); err == nil {
		t.Error("bozuk RE2 deseni kabul edildi")
	}
	badAnd := Rule{
		Name: "bozuk and", Kind: KindDrop, Signal: SignalMetrics, Enabled: true,
		When: Condition{Key: "metric", Op: OpEq, Value: "m1"},
		And:  []Condition{{Key: "http.route", Op: OpMatches, Value: "*bad"}},
	}
	if _, err := e.Upsert(context.Background(), st, badAnd); err == nil {
		t.Error("bozuk And deseni kabul edildi")
	}
	// Geçerli kural kaydedilir ve And blob'a girer (kalıcılık: `and`
	// serialize edilmezse ikiz yeniden yüklendiğinde METRİK KOŞULUNU
	// KAYBEDER ve bütün metriklerden düşürmeye başlar).
	good := Rule{
		Name: "iyi", Kind: KindDrop, Signal: SignalMetrics, Enabled: true,
		When: Condition{Key: "http.route", Op: OpMatches, Value: "^/health"},
		And:  []Condition{{Key: "metric", Op: OpEq, Value: "http.server.duration"}},
	}
	if _, err := e.Upsert(context.Background(), st, good); err != nil {
		t.Fatalf("geçerli kural reddedildi: %v", err)
	}
	var back []Rule
	if err := json.Unmarshal(st.raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || len(back[0].And) != 1 || back[0].And[0].Value != "http.server.duration" {
		t.Fatalf("And koşulu kalıcılaşmadı: %+v", back)
	}
	// Ve yeniden yüklendiğinde davranış korunuyor.
	e2 := New()
	if err := e2.LoadPersisted(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if e2.AcceptMetric(dpWithRoute("http.server.duration", "/health/checkStartup")) {
		t.Error("yeniden yüklenen kural düşürmedi")
	}
	if !e2.AcceptMetric(dpWithRoute("db.client.duration", "/health/checkStartup")) {
		t.Error("yeniden yüklenen kural YANLIŞ metrikten düşürdü — And kaybolmuş")
	}
}
