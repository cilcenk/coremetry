package pipeline

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// span_route_test.go — v0.9.802 regresyonu: `http.route` koşulu
// TÜRETİLMİŞ Span.HTTPRoute alanından eşleşir.
//
// Hata: matchSpan yalnız service.name/name/kind/status_code'u tipli alan
// sayıyordu; 'http.route' default dalına düşüp attr dizisini tarıyordu.
// Ama ingest'te http_route bir attr kopyası DEĞİL: attr yoksa
// http.target'tan, o da yoksa url.path'ten normalize edilerek doldurulan
// türetilmiş bir kolon (otlp/convert.go, v0.9.71). url.path basan
// servislerde attr dizisinde 'http.route' anahtarı HİÇ olmadığı için
// "http.route = /healthz" drop kuralı SESSİZCE hiçbir span'i
// eşleştirmiyordu — operatöre görünen bir hata da yok.
//
// Yollar JENERİK (/healthz, /internal/checkStartup): müşteri yolu depoya
// girmez.

// spanWithRouteAttr — enstrümantasyonu http.route attr'ı basan servis.
// convert.go bu attr'ı AYNEN türetilmiş kolona da yazar, o yüzden ikisi
// birden dolu.
func spanWithRouteAttr(route string) *chstore.Span {
	return &chstore.Span{
		ServiceName: "checkout",
		Name:        "GET " + route,
		Kind:        "server",
		StatusCode:  "ok",
		HTTPRoute:   route,
		AttrKeys:    []string{"http.method", "http.route"},
		AttrValues:  []string{"GET", route},
	}
}

// spanWithDerivedRoute — url.path basan servis: türetilmiş kolon dolu,
// attr dizisinde route anahtarı YOK. Tuzağın tam şekli.
func spanWithDerivedRoute(route, rawPath string) *chstore.Span {
	return &chstore.Span{
		ServiceName: "legacy-gateway",
		Name:        "GET",
		Kind:        "server",
		StatusCode:  "ok",
		HTTPRoute:   route,
		AttrKeys:    []string{"http.method", "url.path"},
		AttrValues:  []string{"GET", rawPath},
	}
}

func TestMatchSpanHTTPRouteUsesDerivedField(t *testing.T) {
	tests := []struct {
		name string
		cond Condition
		span *chstore.Span
		want bool
	}{
		{
			"attr'lı span eşleşir (regresyon: eski davranış korunur)",
			Condition{Key: "http.route", Op: OpEq, Value: "/healthz"},
			spanWithRouteAttr("/healthz"), true,
		},
		{
			"url.path-türetimli span eşleşir (DÜZELTME)",
			Condition{Key: "http.route", Op: OpEq, Value: "/healthz"},
			spanWithDerivedRoute("/healthz", "/healthz"), true,
		},
		{
			"url.path-türetimli, id-soyulmuş şablon eşleşir",
			Condition{Key: "http.route", Op: OpEq, Value: "/internal/checkStartup/:id"},
			spanWithDerivedRoute("/internal/checkStartup/:id", "/internal/checkStartup/8412"), true,
		},
		{
			"route'suz span eşleşmez",
			Condition{Key: "http.route", Op: OpEq, Value: "/healthz"},
			&chstore.Span{ServiceName: "worker", Name: "consume", Kind: "consumer"}, false,
		},
		{
			"boş route boş desene de düşmez (!= ile fail-open olmasın)",
			Condition{Key: "http.route", Op: OpEq, Value: ""},
			spanWithDerivedRoute("/healthz", "/healthz"), false,
		},
		{
			"başka route eşleşmez",
			Condition{Key: "http.route", Op: OpEq, Value: "/healthz"},
			spanWithDerivedRoute("/api/orders", "/api/orders"), false,
		},
		// Operatörün diğer operatörleri de türetilmiş alanı görür.
		{
			"contains türetilmiş alanda çalışır",
			Condition{Key: "http.route", Op: OpContains, Value: "checkStartup"},
			spanWithDerivedRoute("/internal/checkStartup", "/internal/checkStartup"), true,
		},
		{
			"startsWith türetilmiş alanda çalışır",
			Condition{Key: "http.route", Op: OpStartsWith, Value: "/internal/"},
			spanWithDerivedRoute("/internal/checkStartup", "/internal/checkStartup"), true,
		},
		{
			"=~ türetilmiş alanda çalışır (metric_exclusions ile aynı desen dili)",
			Condition{Key: "http.route", Op: OpMatches, Value: "^/healthz"},
			spanWithDerivedRoute("/healthz", "/healthz"), true,
		},
		{
			"=~ ankorsuz: desen yolun ortasında da eşleşir",
			Condition{Key: "http.route", Op: OpMatches, Value: "checkStartup"},
			spanWithDerivedRoute("/internal/checkStartup", "/internal/checkStartup"), true,
		},
		// SINIR: 'attr.' öneki AÇIKÇA ham attr dizisini okur — türetilmiş
		// alan bu yolu kapatmaz.
		{
			"attr.http.route hâlâ ham attr'ı okur (attr'lı span)",
			Condition{Key: "attr.http.route", Op: OpEq, Value: "/healthz"},
			spanWithRouteAttr("/healthz"), true,
		},
		{
			"attr.http.route türetilmiş alanı GÖRMEZ (attr yok)",
			Condition{Key: "attr.http.route", Op: OpEq, Value: "/healthz"},
			spanWithDerivedRoute("/healthz", "/healthz"), false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchSpan(tc.cond, tc.span); got != tc.want {
				t.Errorf("matchSpan(%s %s %q) = %v, beklenen %v",
					tc.cond.Key, tc.cond.Op, tc.cond.Value, got, tc.want)
			}
		})
	}
}

// Diğer anahtarlar ETKİLENMEZ: köprü yalnız 'http.route' anahtarına
// dokundu; tipli alanlar, prefix'li aramalar ve öneksiz-bilinmeyen attr
// fallback'i aynen duruyor.
func TestMatchSpanOtherKeysUnaffected(t *testing.T) {
	sp := &chstore.Span{
		ServiceName: "checkout",
		Name:        "GET /healthz",
		Kind:        "server",
		StatusCode:  "error",
		HTTPRoute:   "/healthz",
		AttrKeys:    []string{"http.method", "tenant"},
		AttrValues:  []string{"GET", "acme"},
		ResKeys:     []string{"k8s.namespace.name"},
		ResValues:   []string{"prod"},
	}
	tests := []struct {
		cond Condition
		want bool
	}{
		{Condition{Key: "service.name", Op: OpEq, Value: "checkout"}, true},
		{Condition{Key: "service.name", Op: OpEq, Value: "/healthz"}, false},
		{Condition{Key: "name", Op: OpEq, Value: "GET /healthz"}, true},
		{Condition{Key: "kind", Op: OpEq, Value: "server"}, true},
		{Condition{Key: "status_code", Op: OpEq, Value: "error"}, true},
		{Condition{Key: "attr.http.method", Op: OpEq, Value: "GET"}, true},
		{Condition{Key: "resource.k8s.namespace.name", Op: OpEq, Value: "prod"}, true},
		// Öneksiz bilinmeyen anahtar hâlâ attr dizisine düşer.
		{Condition{Key: "tenant", Op: OpEq, Value: "acme"}, true},
		{Condition{Key: "tenant", Op: OpEq, Value: "other"}, false},
		// Öneksiz bilinmeyen, hiç olmayan anahtar → boş, eşleşmez.
		{Condition{Key: "nope", Op: OpEq, Value: "x"}, false},
	}
	for _, tc := range tests {
		if got := matchSpan(tc.cond, sp); got != tc.want {
			t.Errorf("matchSpan(%s %s %q) = %v, beklenen %v",
				tc.cond.Key, tc.cond.Op, tc.cond.Value, got, tc.want)
		}
	}
}

// Uçtan uca: drop kuralı url.path-türetimli span'i GERÇEKTEN düşürür ve
// eşleşmeyen span'e dokunmaz. matchSpan doğru olup AcceptSpan'in kuralı
// atladığı bir dünyada da yeşil kalmasın diye motorun kendisinden geçer.
func TestAcceptSpanDropsDerivedRoute(t *testing.T) {
	e := New()
	e.rules = []Rule{{
		ID: "r1", Name: "drop healthz", Kind: KindDrop, Signal: SignalSpans, Enabled: true,
		When: Condition{Key: "http.route", Op: OpEq, Value: "/healthz"},
	}}

	if e.AcceptSpan(spanWithDerivedRoute("/healthz", "/healthz")) {
		t.Error("url.path-türetimli /healthz span'i DÜŞMELİYDİ")
	}
	if e.AcceptSpan(spanWithRouteAttr("/healthz")) {
		t.Error("attr'lı /healthz span'i DÜŞMELİYDİ")
	}
	if !e.AcceptSpan(spanWithDerivedRoute("/api/orders", "/api/orders")) {
		t.Error("eşleşmeyen span KALMALIYDI")
	}
	// Sinyal kapsamı: aynı kural metrik/log tarafını etkilemez.
	e.rules[0].Signal = SignalLogs
	if !e.AcceptSpan(spanWithDerivedRoute("/healthz", "/healthz")) {
		t.Error("logs sinyalli kural span'i düşürmemeliydi")
	}
}
