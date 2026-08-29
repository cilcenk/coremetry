package api

// v0.10.146 — dashboard bundle: panel-başı cache anahtarı + tekil uçla
// yakınsama (kaynak taraması).
//
// Sözleşme: dashPanelKey yanıtı değiştiren HER girdiyi taşır (v0.5.187
// sınıfı çapraz zehirlenme yok), yanıtı DEĞİŞTİRMEYEN farkları (filtre
// JSON'unda boşluk, aynı 30 s kovasındaki from/to) tek girdide toplar.
// Kaynak taraması: bundle spanMetric dalı tam seriyi okur (top-N YOK —
// others katlaması), cachedJSON'dan geçer, ve handler api.go'da değil bu
// dosyada yaşar.

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestDashPanelKey_EveryInputDistinct(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(time.Hour)
	base := bundleReq{ID: "p1", Type: "spanMetric", Agg: "p99", Field: "duration_ms",
		GroupBy: []string{"peer"}, Step: 15, Filters: json.RawMessage(`[{"key":"kind","op":"=","value":"client"}]`), DSL: `kind in ["client"]`}
	baseKey := dashPanelKey("", base, from, to)

	variants := map[string]func(q *bundleReq){
		"type":    func(q *bundleReq) { q.Type = "metric" },
		"name":    func(q *bundleReq) { q.Name = "jvm.memory.used" },
		"service": func(q *bundleReq) { q.Service = "checkout" },
		"agg":     func(q *bundleReq) { q.Agg = "p95" },
		"field":   func(q *bundleReq) { q.Field = "" },
		"groupBy": func(q *bundleReq) { q.GroupBy = []string{"service.name"} },
		"groupBy order": func(q *bundleReq) {
			q.GroupBy = []string{"service.name", "peer"}
		},
		"step":       func(q *bundleReq) { q.Step = 60 },
		"filters":    func(q *bundleReq) { q.Filters = json.RawMessage(`[{"key":"kind","op":"=","value":"producer"}]`) },
		"no filters": func(q *bundleReq) { q.Filters = nil },
		"dsl":        func(q *bundleReq) { q.DSL = `kind in ["producer"]` },
	}
	seen := map[string]string{baseKey: "base"}
	for name, mut := range variants {
		q := base
		q.GroupBy = append([]string(nil), base.GroupBy...)
		mut(&q)
		k := dashPanelKey("", q, from, to)
		if prev, dup := seen[k]; dup {
			t.Errorf("variant %q collides with %q: %s", name, prev, k)
		}
		seen[k] = name
	}
	// ID anahtara GİRMEZ: aynı sorgu iki panelde aynı girdiyi paylaşır.
	q := base
	q.ID = "p2"
	if dashPanelKey("", q, from, to) != baseKey {
		t.Errorf("panel ID must not enter the key (same query in two panels shares one entry)")
	}
	// Kaynak damgası (metric dalı) anahtara girer.
	if dashPanelKey("clickhouse|rule1|d1", base, from, to) == baseKey ||
		dashPanelKey("victoriametrics|rule1|d1", base, from, to) == dashPanelKey("clickhouse|rule1|d1", base, from, to) {
		t.Errorf("source tag must enter the key")
	}
	// Pencere: farklı kova → farklı anahtar.
	if dashPanelKey("", base, from.Add(time.Minute), to.Add(time.Minute)) == baseKey {
		t.Errorf("a window one minute later must not share the key")
	}
}

func TestDashPanelKey_NormalisesNonSemanticDifferences(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(time.Hour)
	a := bundleReq{Type: "spanMetric", Agg: "rate", Filters: json.RawMessage(`[{"key":"kind","op":"=","value":"client"}]`)}
	b := a
	b.Filters = json.RawMessage(" [ {\"key\": \"kind\",\n \"op\": \"=\", \"value\": \"client\"} ] ")
	if dashPanelKey("", a, from, to) != dashPanelKey("", b, from, to) {
		t.Errorf("filter JSON whitespace must not split the cache entry")
	}
	// `null` filtre = filtre yok.
	c := a
	c.Filters = json.RawMessage(`null`)
	d := a
	d.Filters = nil
	if dashPanelKey("", c, from, to) != dashPanelKey("", d, from, to) {
		t.Errorf("filters:null and absent filters must share the entry")
	}
	// `[]` filtre = filtre yok (parseFilters ikisini de boş derler).
	e := a
	e.Filters = json.RawMessage(`[]`)
	if dashPanelKey("", e, from, to) != dashPanelKey("", d, from, to) {
		t.Errorf("filters:[] and absent filters must share the entry")
	}
	// metric dalı Field okumaz → anahtara girmez.
	m1 := bundleReq{Type: "metric", Name: "jvm.memory.used", Agg: "avg", Field: "duration_ms"}
	m2 := m1
	m2.Field = ""
	if dashPanelKey("ch||0", m1, from, to) != dashPanelKey("ch||0", m2, from, to) {
		t.Errorf("Field must not split metric-branch entries (the branch never reads it)")
	}
	// Uzunluk-önekli parçalar: ayırıcı sahteciliği aynı ön-görüntüyü üretemez.
	f1 := bundleReq{Type: "spanMetric", Name: "a\x00b", Service: "c"}
	f2 := bundleReq{Type: "spanMetric", Name: "a", Service: "b\x00c"}
	if dashPanelKey("", f1, from, to) == dashPanelKey("", f2, from, to) {
		t.Errorf("NUL inside a value must not shift the part boundary (length-prefixed parts)")
	}
	// Aynı 30 s kovasındaki from/to tek girdi (FE her tick'te yeniden hesaplar).
	if dashPanelKey("", a, from.Add(7*time.Second), to.Add(7*time.Second)) != dashPanelKey("", a, from, to) {
		t.Errorf("from/to inside the same 30s bucket must share the entry")
	}
	// Anahtar şekli: okunur önek + tür + hash (stats/invalidation önekleri için).
	if k := dashPanelKey("", a, from, to); !regexp.MustCompile(`^dash-panel:v1:spanMetric:[0-9a-f]{1,16}$`).MatchString(k) {
		t.Errorf("unexpected key shape %q", k)
	}
}

// Kaynak taraması — bundle tekil uçlarla AYNI store çağrılarını yapar ve
// cache çekirdeğinden geçer. Yorum soyucu şart: dosya başlığı eski
// çağrının adını tarih anlatmak için kullanıyor.
func TestDashboardBundle_ConvergesWithSingleEndpoints(t *testing.T) {
	raw, err := os.ReadFile("dashboards_data.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))
	for _, marker := range []string{
		"func (s *Server) dashboardsData",
		"func (s *Server) bundleSlot",
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("comment stripping ate real code — %q missing, scan proves nothing", marker)
		}
	}
	// Top-N bilinçli olarak YOK: dashboard'ın "others" katlaması (foldTopN)
	// kuyruğu tam seriden toplar; kırpılmış girdi kuyruk kütlesini sessizce
	// kaybederdi. Kuyruk ön-toplamları gelene dek tam seri.
	if !regexp.MustCompile(`s\.store\.QuerySpanMetric\(`).MatchString(src) {
		t.Errorf("bundle spanMetric slot must read the FULL series (QuerySpanMetric) — the others-fold needs the whole tail")
	}
	if regexp.MustCompile(`s\.store\.QuerySpanMetricTopN\(`).MatchString(src) {
		t.Errorf("bundle must not trim to top-N: foldTopN's others line would silently lose tail mass (see dashboards_data.go header)")
	}
	if !regexp.MustCompile(`s\.cachedJSON\(`).MatchString(src) {
		t.Errorf("bundle slots must go through cachedJSON (per-panel SWR cache)")
	}
	if !regexp.MustCompile(`queryMetricNoted\(`).MatchString(src) {
		t.Errorf("bundle metric slot must use queryMetricNoted (single /api/metrics/query contract)")
	}
	// Handler api.go'dan taşındı — geri sızmasın (api.go BÜYÜMEYECEK).
	apiRaw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stripGoComments(string(apiRaw)), "func (s *Server) dashboardsData") {
		t.Errorf("dashboardsData is declared in api.go again — it lives in dashboards_data.go")
	}
}

// Hata slot'u cache dışı ve boş sonuç `series: []` — bundleSlot marshal
// şekli (FE PanelDataOverride sözleşmesi: series===undefined = "henüz
// bundle'lanmadı", [] = bundle'landı ve boş).
func TestBundleSlot_EmptySeriesIsAnArrayNotAbsent(t *testing.T) {
	b, err := json.Marshal(bundleSlot{Series: nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"series":null}` {
		t.Fatalf("nil series marshals as %s — handler must substitute an empty slice", b)
	}
	b, _ = json.Marshal(bundleSlot{Series: []chstore.SpanMetricSeries{}})
	if string(b) != `{"series":[]}` {
		t.Fatalf("empty series must marshal as [] (bundled-and-empty), got %s", b)
	}
	// Kaynak taraması: handler nil dilimi boş dilime çevirir.
	raw, err := os.ReadFile("dashboards_data.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stripGoComments(string(raw)), "slot.Series = []chstore.SpanMetricSeries{}") {
		t.Errorf("bundleSlot must substitute an empty slice for nil series")
	}
}
