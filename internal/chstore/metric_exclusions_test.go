// metric_exclusions_test.go — v0.9.797 route dışlama kurallarının SAF
// çekirdekleri. Canlı CH gerekmez.
//
// MÜŞTERİ/KURUM YOLU YOK: yollar jenerik (`/health/checkStartup`,
// `/api/orders`) — no_customer_identifiers_test'in koruduğu kural
// testlerde de geçerli.
package chstore

import (
	"strings"
	"testing"
	"time"
)

func mustCompile(t *testing.T, rules ...MetricExclusionRule) *CompiledMetricExclusions {
	t.Helper()
	c, err := CompileMetricExclusions(MetricExclusions{Rules: rules})
	if err != nil {
		t.Fatalf("derleme başarısız: %v", err)
	}
	return c
}

// ───────────────────────── eşleyici ─────────────────────────

func TestCompileMetricExclusionsValidation(t *testing.T) {
	tests := []struct {
		name    string
		rule    MetricExclusionRule
		wantErr string
	}{
		{"geçerli", MetricExclusionRule{Metric: "http.server.duration", Pattern: "/health"}, ""},
		{"attrKey boş → http.route'a normalize", MetricExclusionRule{Metric: "*", Pattern: "^/health"}, ""},
		{"metric boş", MetricExclusionRule{Pattern: "/health"}, "metric boş"},
		{"pattern boş", MetricExclusionRule{Metric: "*"}, "pattern boş"},
		{"bozuk RE2", MetricExclusionRule{Metric: "*", Pattern: "([a-"}, "geçersiz RE2"},
		{"desteklenmeyen attrKey", MetricExclusionRule{Metric: "*", AttrKey: "http.method", Pattern: "GET"}, "attrKey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileMetricExclusions(MetricExclusions{Rules: []MetricExclusionRule{tc.rule}})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("beklenmedik hata: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("hata bekleniyordu (%s), nil döndü", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("hata %q, %q içermeli", err, tc.wantErr)
			}
		})
	}
}

// Kural seçimi: tam ad / '*' / çoklu kural. Desen ANKORSUZ.
func TestRoutePatternsSelection(t *testing.T) {
	c := mustCompile(t,
		MetricExclusionRule{Metric: "http.server.duration", Pattern: "^/health"},
		MetricExclusionRule{Metric: "*", Pattern: "/probe"},
		MetricExclusionRule{Metric: "db.client.duration", Pattern: "^/internal"},
	)
	tests := []struct {
		metric string
		want   []string
	}{
		// Tam adlı kural ÖNCE, '*' sonra — deterministik sıra (SQL bundan üretiliyor).
		{"http.server.duration", []string{"^/health", "/probe"}},
		{"db.client.duration", []string{"^/internal", "/probe"}},
		// Hiç tam eşleşme yok → yalnız '*'.
		{"jvm.memory.used", []string{"/probe"}},
	}
	for _, tc := range tests {
		got := c.RoutePatterns(tc.metric)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: %v, beklenen %v", tc.metric, got, tc.want)
		}
	}
	// Kuralsız set → nil (çağıran SQL'e hiçbir şey eklemez).
	if got := mustCompile(t).RoutePatterns("x"); got != nil {
		t.Errorf("kuralsız sette desen döndü: %v", got)
	}
	// nil işaretçi de güvenli — Store{} kuran testler bu daldan geçer.
	var nilSet *CompiledMetricExclusions
	if got := nilSet.RoutePatterns("x"); got != nil {
		t.Errorf("nil sette desen döndü: %v", got)
	}
	if !nilSet.Empty() || nilSet.Digest() != "0" || nilSet.AnyDropAtIngest() {
		t.Error("nil set boş/0/drop-yok olmalı")
	}
}

// dropAtIngest YALNIZ işaretli kurallar için: işaretsiz bir okuma
// filtresi kuralının ingest'e sızması, operatörün açıkça kapalı
// bıraktığı çekboxu görmezden gelmek olurdu.
func TestDropAtIngestHonoursCheckbox(t *testing.T) {
	c := mustCompile(t,
		MetricExclusionRule{Metric: "http.server.duration", Pattern: "^/health", DropAtIngest: true},
		MetricExclusionRule{Metric: "http.server.duration", Pattern: "^/metrics"}, // okuma-yalnız
		MetricExclusionRule{Metric: "*", Pattern: "/probe", DropAtIngest: true},
	)
	tests := []struct {
		metric, route string
		want          bool
	}{
		{"http.server.duration", "/health/checkStartup", true}, // işaretli + ankorsuz ön ek
		{"http.server.duration", "/metrics", false},            // işaretsiz kural düşürmez
		{"http.server.duration", "/api/orders", false},         // eşleşme yok
		{"jvm.gc.duration", "/probe/live", true},               // '*' kuralı her metriğe
		{"jvm.gc.duration", "/health/checkStartup", false},     // tam adlı kural başka metriğe geçmez
		{"http.server.duration", "", false},                    // route'suz datapoint KALIR
	}
	for _, tc := range tests {
		if got := c.DropAtIngest(tc.metric, tc.route); got != tc.want {
			t.Errorf("DropAtIngest(%q, %q) = %v, beklenen %v", tc.metric, tc.route, got, tc.want)
		}
	}
	if !c.AnyDropAtIngest() {
		t.Error("AnyDropAtIngest false döndü, işaretli kural var")
	}
	// Hiç işaretli kural yoksa sıcak yol kapısı kapalı kalmalı.
	if mustCompile(t, MetricExclusionRule{Metric: "*", Pattern: "/x"}).AnyDropAtIngest() {
		t.Error("işaretsiz kuralda AnyDropAtIngest true döndü")
	}
}

// ───────────────────────── önbellek özeti ─────────────────────────
//
// v0.5.187 sınıfı: farklı kural setleri farklı anahtar üretmeli, aynı
// set (sırası değişse de) aynı anahtarı.

func TestExclusionDigestInvariants(t *testing.T) {
	a := MetricExclusionRule{Metric: "m1", Pattern: "^/health"}
	b := MetricExclusionRule{Metric: "m1", Pattern: "^/probe"}

	if got := mustCompile(t).Digest(); got != "0" {
		t.Errorf("boş set özeti %q, beklenen \"0\"", got)
	}
	// Farklı desen → farklı özet (aynı UZUNLUK: len() çöküşü kapısı).
	if mustCompile(t, a).Digest() == mustCompile(t, b).Digest() {
		t.Error("iki farklı 1-kurallı set aynı özeti üretti — len-only çöküş")
	}
	// Sıra fark etmez (küme semantiği).
	if mustCompile(t, a, b).Digest() != mustCompile(t, b, a).Digest() {
		t.Error("sıra özeti değiştirdi — sort adımı düştü")
	}
	// Kararlı.
	if mustCompile(t, a, b).Digest() != mustCompile(t, a, b).Digest() {
		t.Error("özet kararsız")
	}
	// dropAtIngest bayrağı özete girer: aynı desen ingest'te düşerken
	// düşmezken FARKLI bir dünya (rollup zamanla temizlenir).
	withDrop := a
	withDrop.DropAtIngest = true
	if mustCompile(t, a).Digest() == mustCompile(t, withDrop).Digest() {
		t.Error("dropAtIngest özeti değiştirmedi")
	}
}

// ───────────────────────── SQL pini ─────────────────────────

// SIFIR ETKİ: kural YOKKEN üretilen SQL ve argümanlar, dışlama
// özelliğinden ÖNCEKİYLE bayt-bayt aynı olmalı. Bu pin olmadan
// "kapalıyken de bir şey değiştirdi mi" sorusunun cevabı okuma gerektirir.
func TestBuildMetricQuerySQLUnchangedWithoutRules(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	f := MetricQueryFilter{
		Name: "http.server.duration", Service: "api-gateway",
		Aggregation: "avg", GroupBy: []string{"http.route"},
		From: now.Add(-time.Hour), To: now, StepSeconds: 60,
	}
	base, baseArgs, err := buildMetricQuerySQL(f, now, "histogram", "delta", nil)
	if err != nil {
		t.Fatal(err)
	}
	empty, emptyArgs, err := buildMetricQuerySQL(f, now, "histogram", "delta", mustCompile(t))
	if err != nil {
		t.Fatal(err)
	}
	if base != empty {
		t.Errorf("boş kural seti SQL'i değiştirdi:\n--- nil ---\n%s\n--- boş ---\n%s", base, empty)
	}
	if len(baseArgs) != len(emptyArgs) {
		t.Errorf("boş kural seti argüman sayısını değiştirdi: %d → %d", len(baseArgs), len(emptyArgs))
	}
	// BAŞKA bir metriğe kural yazmak da bu sorguyu değiştirmemeli.
	other := mustCompile(t, MetricExclusionRule{Metric: "db.client.duration", Pattern: "^/health"})
	sql, args, err := buildMetricQuerySQL(f, now, "histogram", "delta", other)
	if err != nil {
		t.Fatal(err)
	}
	if sql != base || len(args) != len(baseArgs) {
		t.Error("başka metriğin kuralı bu sorgunun SQL'ini değiştirdi")
	}
}

// NOT match koşulu BIND-ARG'lı üretilmeli: desen operatörden geliyor,
// SQL'e gömülmesi hem enjeksiyon yüzeyi hem plan-cache kirliliği.
func TestBuildMetricQuerySQLInjectsBoundNotMatch(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	f := MetricQueryFilter{
		Name: "http.server.duration", Service: "api-gateway",
		Aggregation: "avg", From: now.Add(-time.Hour), To: now, StepSeconds: 60,
	}
	pattern := "^/health/checkStartup"
	ex := mustCompile(t,
		MetricExclusionRule{Metric: "http.server.duration", Pattern: pattern},
		MetricExclusionRule{Metric: "*", Pattern: "/probe"},
	)
	sql, args, err := buildMetricQuerySQL(f, now, "histogram", "delta", ex)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(sql, "NOT match("); n != 2 {
		t.Errorf("NOT match koşulu %d kez, beklenen 2:\n%s", n, sql)
	}
	if strings.Contains(sql, pattern) {
		t.Errorf("DESEN SQL'E GÖMÜLMÜŞ (bind-arg olmalı):\n%s", sql)
	}
	// Desen ve attr anahtarı argümanlarda, doğru sırada.
	var seenKey, seenPat bool
	for _, a := range args {
		if s, ok := a.(string); ok {
			if s == MetricExclusionAttrKey {
				seenKey = true
			}
			if s == pattern {
				seenPat = true
			}
		}
	}
	if !seenKey || !seenPat {
		t.Errorf("attr anahtarı / desen argümanlarda yok: %v", args)
	}
	// GRUPSUZ sorgu da filtrelenir — "Toplam" çizgisinin temizliği buna bağlı.
	if !strings.Contains(sql, "[]::Array(String)") {
		t.Error("test grupsuz sorgu kurmalıydı (fixture bozuldu)")
	}
}

// Route tier'da route bir KOLON: NOT koşulu doğrudan eklenir, tier AÇIK
// kalır (attr'sız kardeşlerinden ayrılan nokta).
func TestBuildRollupRouteSQLExclusions(t *testing.T) {
	base := buildRollupRouteSQL("rollup_metrics_route_5m", "sum(value_sum)", nil)
	if strings.Contains(base, "NOT match") {
		t.Error("desen yokken NOT koşulu eklendi — sıfır-etki pini kırıldı")
	}
	withExcl := buildRollupRouteSQL("rollup_metrics_route_5m", "sum(value_sum)", []string{"^/health", "/probe"})
	if n := strings.Count(withExcl, "AND NOT match(route, ?)"); n != 2 {
		t.Errorf("NOT match(route, ?) %d kez, beklenen 2:\n%s", n, withExcl)
	}
	// Koşullar ts <= ? SONRASINDA: argüman sırası sözleşmesi
	// (queryMetricRollupRoute desenleri listenin sonuna ekliyor).
	if strings.Index(withExcl, "NOT match") < strings.Index(withExcl, "ts <= ?") {
		t.Error("dışlama koşulu zaman sınırından ÖNCE — argüman sırası bozulur")
	}
	if strings.Contains(withExcl, "'^/health'") {
		t.Error("desen SQL'e gömülmüş — bind-arg olmalı")
	}
}

// ROLLUP DÜRÜSTLÜK KAPISI: attr taşımayan kademeler kural aktifken
// KAPALI (satırlar route boyutunda zaten katlanmış → dışlanan route'un
// katkısı ayrıştırılamaz).
func TestAttrlessRollupPlansBlockedByExclusion(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	f := MetricQueryFilter{
		Name: "http.server.duration", Service: "api-gateway",
		Aggregation: "avg", StepSeconds: 60,
		From: now.Add(-time.Hour), To: now,
	}
	// Taban: kural YOKKEN kademe açılıyor (yoksa test hiçbir şey kanıtlamaz).
	if _, ok := metricRollupPlan(f, "gauge", "", now, nil); !ok {
		t.Fatal("taban plan açılmadı — fixture bozuk")
	}
	if _, ok := metricRollupHistPlan(f, "histogram", "delta", now, nil); !ok {
		t.Fatal("taban histogram planı açılmadı — fixture bozuk")
	}

	hit := mustCompile(t, MetricExclusionRule{Metric: "http.server.duration", Pattern: "^/health"})
	if _, ok := metricRollupPlan(f, "gauge", "", now, hit); ok {
		t.Error("kurallı metrikte aile-C kademesi AÇIK kaldı — kirli sayı")
	}
	if _, ok := metricRollupHistPlan(f, "histogram", "delta", now, hit); ok {
		t.Error("kurallı metrikte histogram kademesi AÇIK kaldı — kirli yüzdelik")
	}
	// '*' kuralı da kapatır.
	star := mustCompile(t, MetricExclusionRule{Metric: "*", Pattern: "/probe"})
	if _, ok := metricRollupPlan(f, "gauge", "", now, star); ok {
		t.Error("'*' kuralı aile-C kademesini kapatmadı")
	}
	// BAŞKA metriğin kuralı bu metriği ETKİLEMEZ — kapı gereğinden geniş
	// olsaydı bir kural bütün rollup okumalarını ham yola düşürürdü.
	other := mustCompile(t, MetricExclusionRule{Metric: "db.client.duration", Pattern: "^/health"})
	if _, ok := metricRollupPlan(f, "gauge", "", now, other); !ok {
		t.Error("ilgisiz kural aile-C kademesini kapattı — gereksiz ham tarama")
	}
	if _, ok := metricRollupHistPlan(f, "histogram", "delta", now, other); !ok {
		t.Error("ilgisiz kural histogram kademesini kapattı")
	}
}
