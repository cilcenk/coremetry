package mcptools

// analysis_test.go — v0.9.1146 (AI Faz 3.3). Üç analiz tool'unun
// sözleşmesi, discovery_test.go'nun dört katmanıyla aynı düzende:
//
//	(a) katalogda kayıtlı + MinRole "" + her property açıklamalı,
//	(b) pencere/tavan semantiği ŞEMADA dürüst (kelepçeyi LLM'e
//	    keşfettirmiyoruz; kabul-edilip-yok-sayılan arg yok),
//	(c) dürüstlük zarfları (truncated / reasons / sparse / yön) SAF
//	    üreticilerde tablo testli,
//	(d) handler'ların gerçekten o üreticilerden VE doğru okuyucudan
//	    döndüğü kaynak pini ile bağlı — saf test tek başına BAĞLANMA
//	    kanıtı değildir (bu depoda yanan ders).

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// analysisToolNames — Faz 3.3'ün üçlüsü.
var analysisToolNames = []string{"get_topology", "get_blast_radius", "get_log_histogram"}

func TestAnalysisToolsRegistered(t *testing.T) {
	tools := ToolList(Deps{}) // Deps{} güvenli: ToolList yalnız closure kurar.
	for _, name := range analysisToolNames {
		t.Run(name, func(t *testing.T) {
			tool := toolByName(t, tools, name)
			// MinRole AÇIKÇA "": üçü de salt-okunur ve REST eşleri kapısız
			// (/api/servicegraph, /api/services/{name}/blast-radius,
			// /api/logs/timeseries). api/mcp_authz_test.go registry
			// genelinde de pinliyor.
			if tool.MinRole != "" {
				t.Fatalf("%s: MinRole=%q — analiz tool'ları viewer tabanında olmalı (REST eşleri kapısız)", name, tool.MinRole)
			}
			if tool.Handler == nil {
				t.Fatalf("%s: Handler nil", name)
			}
			if len(tool.Description) < 120 {
				t.Fatalf("%s: açıklama kontrat değil cümle (%d karakter)", name, len(tool.Description))
			}
			props := schemaProps(t, tool)
			if len(props) == 0 {
				t.Fatalf("%s: hiç property yok", name)
			}
			for pname, p := range props {
				pm, _ := p.(map[string]any)
				if desc, _ := pm["description"].(string); desc == "" {
					t.Fatalf("%s: %q property'sinin açıklaması yok — LLM tahmin eder", name, pname)
				}
			}
			if req, ok := tool.InputSchema["required"]; ok {
				if _, isStr := req.([]string); !isStr {
					t.Fatalf("%s: required[] %T — []string olmalı (v0.9.1050 kanonik şekli)", name, req)
				}
			}
		})
	}
}

// Katalogda YERLEŞİM: analiz tool'ları tüketici/kardeş ailelerinin
// yanında dursun (tools/list sıralı gelir, model komşuları birlikte
// okur).
func TestAnalysisToolsGroupedNearFamilies(t *testing.T) {
	pos := map[string]int{}
	for i, tool := range ToolList(Deps{}) {
		pos[tool.Name] = i
	}
	pairs := []struct{ tool, family string }{
		{"get_topology", "get_service_health"}, // servis kazısı
		{"get_blast_radius", "get_topology"},   // iş bölümü referansı karşılıklı
		{"get_log_histogram", "search_logs"},   // log ailesi
	}
	for _, p := range pairs {
		ti, tok := pos[p.tool]
		fi, fok := pos[p.family]
		if !tok || !fok {
			t.Fatalf("katalogda eksik: %s=%v %s=%v", p.tool, tok, p.family, fok)
		}
		if diff := ti - fi; diff > 3 || diff < -3 {
			t.Errorf("%s, %s'ten %d sıra uzakta — ailesinin yanında dursun", p.tool, p.family, diff)
		}
	}
}

// ─── şema dürüstlüğü ───────────────────────────────────────────

func TestGetTopologySchemaCarriesBucketAndCaps(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_topology")
	props := schemaProps(t, tool)

	// service OPSİYONEL: filo-geneli "en yoğun kenarlar" ana kullanım.
	if req, ok := tool.InputSchema["required"]; ok {
		if rs, _ := req.([]string); len(rs) != 0 {
			t.Errorf("required = %v — service opsiyonel olmalı", rs)
		}
	}
	rs, ok := props["range_s"].(map[string]any)
	if !ok {
		t.Fatal("range_s property eksik")
	}
	if rs["maximum"] != 604800 {
		t.Fatalf("range_s maximum = %v, want 604800", rs["maximum"])
	}
	// 5 dakikalık kova TABANI şemada: model 60 saniye sorup 5 dakika
	// cevap almayı keşfetmesin.
	if d, _ := rs["description"].(string); !strings.Contains(d, "300") || !strings.Contains(d, "5 minute") {
		t.Errorf("range_s açıklaması kova tabanını söylemiyor: %q", d)
	}
	lim, _ := props["limit"].(map[string]any)
	if lim["maximum"] != topoEdgeCap {
		t.Fatalf("limit maximum = %v, want %d", lim["maximum"], topoEdgeCap)
	}
	d := tool.Description
	// Tool'un iki büyük yanlış-okuma riski açıklamada KAPANMALI:
	// (1) yön ayrımı, (2) env'in filtre DEĞİL annotation olması,
	// (3) tek hop, (4) gizli gürültü kalıplarının uygulanmadığı.
	for _, want := range []string{"upstream", "downstream", "ONE hop", "ANNOTATION, not a filter", "NOT applied"} {
		if !strings.Contains(d, want) {
			t.Errorf("açıklama %q geçmiyor — semantik gizli kalır", want)
		}
	}
	if !strings.Contains(d, "5-minute") {
		t.Error("açıklama pencere yuvarlamasını söylemiyor (kova sınırlarına DIŞARI)")
	}
}

func TestGetBlastRadiusSchemaMirrorsRESTCeiling(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_blast_radius")
	props := schemaProps(t, tool)
	req, _ := tool.InputSchema["required"].([]string)
	if len(req) != 1 || req[0] != "service" {
		t.Fatalf("required = %v, want [service]", req)
	}
	rs, _ := props["range_s"].(map[string]any)
	// Canlı ucun (getServiceBlastRadius) 24 saat tavanı: tool ucun
	// ötesine geçen bir pencere VAAT ETMEZ.
	if rs["maximum"] != blastMaxRangeS {
		t.Fatalf("range_s maximum = %v, want %d (REST ucunun tavanı)", rs["maximum"], blastMaxRangeS)
	}
	d := tool.Description
	// YÖN, bu tool'un tek büyük yanlış-okuma riski.
	if !strings.Contains(d, "UPSTREAM") || !strings.Contains(d, "NOT the list of things the") {
		t.Errorf("açıklama yönü ayırmıyor (çağıranlar ≠ bağımlılıklar): %q", d)
	}
	if !strings.Contains(d, "get_topology") {
		t.Error("açıklama iş bölümünü söylemiyor — aşağı-akış get_topology'nin işi")
	}
	if !strings.Contains(d, "had_open_problem_in_window") {
		t.Error("açıklama cascade bayrağının ANLAMINI taşımıyor — alanı veri olarak vermek yetmez")
	}
	// Açıklamadaki SAYI store tavanıyla aynı olmalı. Metinde elle yazılı
	// bir tavan, sabit değişince sessizce YALANA dönüşür ("don't lie in
	// the tool description" kuralının en sık ihlal şekli).
	if !strings.Contains(d, strconv.Itoa(blastCallerCap)+" busiest callers") {
		t.Errorf("açıklamadaki çağıran tavanı blastCallerCap (%d) ile ayrışmış", blastCallerCap)
	}
}

func TestGetLogHistogramSchemaBoundsBucketCount(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_log_histogram")
	props := schemaProps(t, tool)
	if req, ok := tool.InputSchema["required"]; ok {
		if rs, _ := req.([]string); len(rs) != 0 {
			t.Errorf("required = %v — filo-geneli hacim sorusu argümansız çalışmalı", rs)
		}
	}
	// Model GENİŞLİK değil SAYI seçer (v0.9.287: değeri kelepçelemek
	// sayıyı sınırlamıyor). bucket_sec adında bir property olmamalı.
	for _, forbidden := range []string{"bucket_sec", "bucket_s", "interval"} {
		if _, ok := props[forbidden]; ok {
			t.Errorf("%q property'si var — genişlik TÜRETİLİR, çağıran kova SAYISI seçer", forbidden)
		}
	}
	b, ok := props["buckets"].(map[string]any)
	if !ok {
		t.Fatal("buckets property eksik")
	}
	if b["maximum"] != logHistMaxBuckets {
		t.Fatalf("buckets maximum = %v, want %d", b["maximum"], logHistMaxBuckets)
	}
	// Property açıklamasındaki varsayılan + tavan SABİTLERLE aynı olmalı
	// — elle yazılmış bir sayı, sabit değişince yalana dönüşür.
	bd, _ := b["description"].(string)
	for _, want := range []string{
		"Default " + strconv.Itoa(logHistDefaultBuckets),
		"max " + strconv.Itoa(logHistMaxBuckets),
	} {
		if !strings.Contains(bd, want) {
			t.Errorf("buckets açıklaması %q geçmiyor (sabitlerle ayrışmış): %q", want, bd)
		}
	}
	rs, _ := props["range_s"].(map[string]any)
	if rs["maximum"] != 604800 {
		t.Fatalf("range_s maximum = %v, want 604800", rs["maximum"])
	}
	d := tool.Description
	// Dönüş şeklinin taşıyamadığı iki şey açıklamada olmak ZORUNDA:
	// sparse kova ve partial bayrağının YOKLUĞU.
	for _, want := range []string{"OMITTED", "NO partial flag", "search_logs"} {
		if !strings.Contains(d, want) {
			t.Errorf("açıklama %q geçmiyor — model boş kovayı/timeout'u yanlış okur", want)
		}
	}
	// Bant sözlüğü: model "FATAL" bandı uydurmasın.
	if !strings.Contains(d, "FATAL folds into it") {
		t.Error("açıklama bant sözlüğünü öğretmiyor (FATAL → ERROR)")
	}
}

// ─── saf kelepçeler ────────────────────────────────────────────

func TestTopologyWindowS(t *testing.T) {
	cases := []struct {
		name     string
		in, want int
	}{
		{"varsayılan 1 saat (Topology sayfasının varsayılanı)", 0, 3600},
		{"negatif → varsayılan", -60, 3600},
		{"kova altı 5 dakikaya çıkar", 60, 300},
		{"tam kova sınırı", 300, 300},
		{"kova üstü aynen geçer", 301, 301},
		{"6 saat geçer", 21600, 21600},
		{"7 gün sınırı", 604800, 604800},
		{"30 gün kelepçelenir", 30 * 86400, 604800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := topologyWindowS(tc.in); got != tc.want {
				t.Fatalf("topologyWindowS(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestBlastWindowS(t *testing.T) {
	cases := []struct {
		name     string
		in, want int
	}{
		{"varsayılan 1 saat (REST ucuyla aynı)", 0, 3600},
		{"negatif → varsayılan", -1, 3600},
		{"kova altı 5 dakikaya çıkar", 30, 300},
		{"20 dakika aynen", 1200, 1200},
		{"24 saat sınırı", 86400, 86400},
		{"24 saatin üstü kelepçelenir (REST tavanı)", 172800, 86400},
		{"7 gün de 24 saate iner", 604800, 86400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blastWindowS(tc.in); got != tc.want {
				t.Fatalf("blastWindowS(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestLogHistWindowS(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 3600}, {-5, 3600}, {60, 60}, {1800, 1800},
		{604800, 604800}, {30 * 86400, 604800},
	}
	for _, tc := range cases {
		if got := logHistWindowS(tc.in); got != tc.want {
			t.Errorf("logHistWindowS(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// logHistBucketS — (pencere, SAYI) → GENİŞLİK. Birim karışımı sınıfının
// kuralı: saniye girer, saniye çıkar, `buckets` bir sayıdır. Kritik
// invariant sonda: türetilen genişlikle kova sayısı istenen sayıyı
// AŞMAMALI (v0.9.287: aşağı yuvarlayan bölme tam bunu kaçırıyordu).
func TestLogHistBucketS(t *testing.T) {
	cases := []struct {
		name             string
		windowS, buckets int
		want             int
	}{
		{"1 saat / varsayılan 24", 3600, 0, 150},
		{"1 saat / 60 kova", 3600, 60, 60},
		{"30 dakika / varsayılan", 1800, 0, 75},
		{"tavan bölme: 1801 / 24 = 76 değil 75.04 → 76", 1801, 24, 76},
		{"5 dakika / 60 kova", 300, 60, 5},
		{"30 saniye / 60 kova → 1 saniye tabanı", 30, 60, 1},
		{"kova sayısı kelepçelenir (1000 → 60)", 3600, 1000, 60},
		{"tek kova = pencerenin tamamı", 3600, 1, 3600},
		{"negatif sayı → varsayılan", 3600, -3, 150},
		{"7 gün / 60 kova", 604800, 60, 10080},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := logHistBucketS(tc.windowS, tc.buckets)
			if got != tc.want {
				t.Fatalf("logHistBucketS(%d,%d) = %d, want %d", tc.windowS, tc.buckets, got, tc.want)
			}
		})
	}
	// INVARIANT: hiçbir (pencere, sayı) çifti istenen kova sayısını
	// aşan bir genişlik üretmez.
	for _, w := range []int{30, 300, 1800, 3600, 86400, 604800} {
		for _, n := range []int{1, 7, 24, 60, 1000} {
			b := logHistBucketS(w, n)
			eff := n
			if eff > logHistMaxBuckets {
				eff = logHistMaxBuckets
			}
			if count := (w + b - 1) / b; count > eff {
				t.Errorf("pencere %ds, istenen %d kova → genişlik %ds = %d kova (SAYI aşıldı)", w, n, b, count)
			}
		}
	}
}

// mcpFloat — yuvarlama bağlam bütçesi, sonluluk kalkanı ise CEVABIN
// KENDİSİ: encoding/json NaN/±Inf'i reddeder, yani tek bozuk ondalık
// tüm tool çağrısını hataya çevirirdi.
func TestMcpFloat(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"iki hane", 12.3456, 12.35},
		{"yukarı yuvarlama", 0.005, 0.01},
		{"sıfır", 0, 0},
		{"negatif", -1.239, -1.24},
		{"NaN düşer", math.NaN(), 0},
		{"+Inf düşer", math.Inf(1), 0},
		{"-Inf düşer", math.Inf(-1), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpFloat(tc.in); got != tc.want {
				t.Fatalf("mcpFloat(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ─── get_topology saf üreticileri ──────────────────────────────

// Hedef TÜRÜ ham id ÖNEKİNDEN: node_kind consumer kenarında yalan
// söylüyor (kuyruk düğümü PARENT tarafında durup 'service' etiketi
// alıyor — v0.9.1028). Önek node_kind'ı EZMELİ.
func TestTopologyTargetKind(t *testing.T) {
	cases := []struct {
		id, nodeKind, want string
	}{
		{"payments", "service", "service"},
		{"payments", "", "service"},
		{"db:postgresql@10.0.0.1", "db", "database"},
		{"queue:kafka:payment.settled", "queue", "queue"},
		{"ext:stripe", "external", "external"},
		// KRİTİK: önek node_kind'ın yalanını ezer.
		{"queue:kafka:payment.settled", "service", "queue"},
		{"db:h2", "service", "database"},
		{"ext:stripe", "service", "external"},
		// Öneksiz id'de node_kind sözlüğü konuşur.
		{"redis-cache", "kafka", "queue"},
		{"redis-cache", "messaging", "queue"},
		{"weird", "something-new", "internal"},
	}
	for _, tc := range cases {
		if got := topologyTargetKind(tc.id, tc.nodeKind); got != tc.want {
			t.Errorf("topologyTargetKind(%q,%q) = %q, want %q", tc.id, tc.nodeKind, got, tc.want)
		}
	}
}

func TestTopologyEdgeRowsOrderCapAndSanitising(t *testing.T) {
	in := []chstore.ServiceTopologyEdge{
		{ParentService: "b", ChildNode: "x", NodeKind: "service", Calls: 10, Protocol: "http"},
		{ParentService: "a", ChildNode: "y", NodeKind: "service", Calls: 10, Protocol: "http"},
		{ParentService: "hot", ChildNode: "z", NodeKind: "service", Calls: 99, Errors: 3,
			ErrorRate: 3.0303030303, AvgMs: 1.23456, P99Ms: math.Inf(1),
			TopLabels: []string{"l1", "l2", "l3", "l4", "l5"}, DistinctLabels: 42},
	}
	rows, trimmed := topologyEdgeRows(in, 10)
	if trimmed {
		t.Fatal("tavan altındaki liste trimmed olmamalı")
	}
	if len(rows) != 3 || rows[0].Source != "hot" {
		t.Fatalf("calls DESC bozuk: %+v", rows)
	}
	// Eşit calls'ta TAM tiebreak (v0.8.324): sıra oturumlar arası oynamasın.
	if rows[1].Source != "a" || rows[2].Source != "b" {
		t.Fatalf("eşit calls tiebreak'i kaynak adına göre olmalı: %v / %v", rows[1].Source, rows[2].Source)
	}
	// Etiket tavanı + görülmeyen etiket sayısı.
	if len(rows[0].TopLabels) != topoLabelCap {
		t.Fatalf("top_labels %d, want %d", len(rows[0].TopLabels), topoLabelCap)
	}
	if rows[0].DistinctLabels != 42 {
		t.Fatalf("distinct_labels kayboldu: %d", rows[0].DistinctLabels)
	}
	// Ondalıklar yuvarlanır, +Inf DÜŞER (yoksa json.Marshal tüm çağrıyı hataya çevirir).
	if rows[0].ErrorRatePct != 3.03 || rows[0].AvgMs != 1.23 || rows[0].P99Ms != 0 {
		t.Fatalf("sayı hijyeni: rate=%v avg=%v p99=%v", rows[0].ErrorRatePct, rows[0].AvgMs, rows[0].P99Ms)
	}
	// Tavan uygulanır ve GİRDİ MUTASYONA UĞRAMAZ (çağıran len(edges) ile
	// sessiz tavanı ayrıca kontrol ediyor).
	capped, trimmed2 := topologyEdgeRows(in, 1)
	if len(capped) != 1 || !trimmed2 {
		t.Fatalf("limit=1 → len=%d trimmed=%v", len(capped), trimmed2)
	}
	if in[0].ParentService != "b" || len(in) != 3 {
		t.Fatalf("girdi dilimi değişti: %+v", in)
	}
}

func TestSplitTopologyEdgesByDirection(t *testing.T) {
	rows := []topologyEdgeRow{
		{Source: "gateway", Target: "checkout"},       // upstream
		{Source: "checkout", Target: "db:postgresql"}, // downstream
		{Source: "checkout", Target: "payments"},      // downstream
		{Source: "queue:kafka:x", Target: "checkout"}, // upstream (kuyruk → tüketici)
		{Source: "unrelated", Target: "other"},        // hiçbiri
	}
	up, down, other := splitTopologyEdges(rows, "checkout")
	if len(up) != 2 || up[0].Source != "gateway" || up[1].Source != "queue:kafka:x" {
		t.Fatalf("upstream = %+v", up)
	}
	if len(down) != 2 || down[0].Target != "db:postgresql" {
		t.Fatalf("downstream = %+v", down)
	}
	if len(other) != 1 || other[0].Source != "unrelated" {
		t.Fatalf("other = %+v (odağa dokunmayan kenar sessizce kaybolmamalı)", other)
	}
}

func TestTopologyPayloadShapes(t *testing.T) {
	rows := []topologyEdgeRow{
		{Source: "gateway", Target: "checkout", Calls: 5},
		{Source: "checkout", Target: "db:h2", TargetKind: "database", Calls: 3},
	}

	t.Run("filo kapsamı", func(t *testing.T) {
		body := topologyPayload("", 3600, rows, false)
		if body["scope"] != "estate" {
			t.Fatalf("scope = %v", body["scope"])
		}
		if _, ok := body["edges"]; !ok {
			t.Fatal("filo yolunda edges olmalı")
		}
		if _, ok := body["upstream"]; ok {
			t.Fatal("odak yoksa yön kovaları OLMAMALI (yön anlamı yok)")
		}
		if body["window_bucket_s"] != topoBucketS {
			t.Fatalf("window_bucket_s = %v — pencere granülü ifşa edilmeli", body["window_bucket_s"])
		}
	})

	t.Run("odaklı", func(t *testing.T) {
		body := topologyPayload("checkout", 1800, rows, false)
		if body["scope"] != "service" || body["service"] != "checkout" {
			t.Fatalf("odak echo yok: %v", body)
		}
		if body["hops"] != topoFocusHops {
			t.Fatalf("hops = %v, want %d (tek hop sözleşmesi gövdede)", body["hops"], topoFocusHops)
		}
		if body["upstream_count"] != 1 || body["downstream_count"] != 1 {
			t.Fatalf("yön sayıları: %v / %v", body["upstream_count"], body["downstream_count"])
		}
		if _, ok := body["unrelated_edges"]; ok {
			t.Fatal("odağa dokunmayan kenar yokken unrelated_edges TAŞINMAMALI")
		}
	})

	t.Run("boş sonuç hata değil", func(t *testing.T) {
		body := topologyPayload("checkout", 3600, nil, false)
		reasons, ok := body["reasons"].([]string)
		if !ok || len(reasons) < 3 {
			t.Fatalf("reasons = %v — boş graf dürüst kanıtla dönmeli", body["reasons"])
		}
		joined := strings.ToLower(strings.Join(reasons, " | "))
		for _, want := range []string{"list_services", "window", "entry service"} {
			if !strings.Contains(joined, want) {
				t.Errorf("nedenler %q geçmiyor: %s", want, joined)
			}
		}
		if note, _ := body["note"].(string); !strings.Contains(note, "do NOT invent") {
			t.Errorf("boş graf notu uydurma yasağını taşımıyor: %q", note)
		}
	})

	t.Run("kesme ifşa edilir", func(t *testing.T) {
		body := topologyPayload("", 3600, rows, true)
		if body["truncated"] != true {
			t.Fatal("truncated=true olmalı")
		}
		if note, _ := body["note"].(string); !strings.Contains(note, "busiest") {
			t.Errorf("kesme notu eksik: %q", note)
		}
	})
}

// ─── get_blast_radius saf üreticisi ────────────────────────────

func TestBlastRadiusPayload(t *testing.T) {
	br := chstore.BlastRadius{
		Service:           "checkout",
		WindowSec:         3600,
		CascadingCallers:  1,
		TotalRPS:          1.23456,
		TotalErrorsPerSec: math.NaN(),
		Callers: []chstore.BlastRadiusCaller{
			{Service: "gateway", Calls: 900, Errors: 9, RPS: 0.2500001, ErrorRate: 1.0, HasOpenProblem: true},
			{Service: "mobile-bff", Calls: 100, RPS: 0.0277777},
		},
	}
	body := blastRadiusPayload(br, 3600)

	// YÖN gövdede AÇIK: bu tool'un tek büyük yanlış-okuma riski.
	if body["direction"] != "upstream-callers" {
		t.Fatalf("direction = %v", body["direction"])
	}
	if body["window_s"] != 3600 || body["window_bucket_s"] != topoBucketS {
		t.Fatalf("pencere zarfı: %v / %v", body["window_s"], body["window_bucket_s"])
	}
	callers, ok := body["callers"].([]blastCallerRow)
	if !ok || len(callers) != 2 {
		t.Fatalf("callers = %#v", body["callers"])
	}
	// Cascade bayrağının ZAMAN KİPİ alan adında (v0.9.1047 düzeltmesi).
	if !callers[0].HadOpenProblemInWindow {
		t.Fatal("cascade bayrağı taşınmıyor")
	}
	if callers[0].RPS != 0.25 || callers[1].RPS != 0.03 {
		t.Fatalf("rps yuvarlanmadı: %v / %v", callers[0].RPS, callers[1].RPS)
	}
	// NaN düşer, cevap AYAKTA kalır.
	if body["total_errors_per_s"] != 0.0 {
		t.Fatalf("total_errors_per_s = %v — sonlu olmayan değer 0'a düşmeli", body["total_errors_per_s"])
	}
	if body["truncated"] != false {
		t.Fatal("iki çağıran tavana dayanmaz")
	}
	if next, _ := body["next"].(string); !strings.Contains(next, "get_topology") {
		t.Errorf("next iş bölümünü söylemeli (aşağı-akış get_topology): %q", next)
	}

	t.Run("tavan ifşa edilir", func(t *testing.T) {
		full := chstore.BlastRadius{Service: "hub"}
		for i := 0; i < blastCallerCap; i++ {
			full.Callers = append(full.Callers, chstore.BlastRadiusCaller{Service: "c", Calls: 1})
		}
		got := blastRadiusPayload(full, 3600)
		if got["truncated"] != true {
			t.Fatal("store LIMIT'ine dayanan sonuç truncated=true olmalı")
		}
		if note, _ := got["note"].(string); !strings.Contains(note, "LOWER BOUND") {
			t.Errorf("kesme notu total_rps'in alt sınır olduğunu söylemeli: %q", note)
		}
	})

	t.Run("çağıran yok = hata değil", func(t *testing.T) {
		got := blastRadiusPayload(chstore.BlastRadius{Service: "gateway"}, 1800)
		reasons, ok := got["reasons"].([]string)
		if !ok || len(reasons) < 3 {
			t.Fatalf("reasons = %v", got["reasons"])
		}
		joined := strings.ToLower(strings.Join(reasons, " | "))
		// GİRİŞ servisi ihtimali ilk sırada olmalı — filonun kenarındaki
		// gateway için doğru cevap tam olarak bu.
		if !strings.Contains(joined, "entry service") {
			t.Errorf("nedenler giriş-servisi hâlini söylemiyor: %s", joined)
		}
		if note, _ := got["note"].(string); !strings.Contains(note, "do NOT invent") {
			t.Errorf("boş liste notu uydurma yasağını taşımıyor: %q", note)
		}
	})
}

// ─── get_log_histogram saf üreticileri ─────────────────────────

func TestLogHistSeriesRowsCanonicalOrderAndTotals(t *testing.T) {
	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	pt := func(min int, v int64) logstore.LogPoint {
		return logstore.LogPoint{T: base.Add(time.Duration(min) * time.Minute).UnixNano(), V: v}
	}
	// Giriş sırası KASITLI olarak karışık (CH ilk-görülme sırasında
	// döner, ES bant sırasında) — aynı soruya iki backend'de farklı
	// sıralı cevap vermek ayrı bir sınıf olarak yanmış.
	in := []logstore.LogSeries{
		{Name: "INFO", Points: []logstore.LogPoint{pt(0, 100), pt(5, 50)}},
		{Name: "NOTICE", Points: []logstore.LogPoint{pt(0, 1)}}, // sözlük dışı
		{Name: "ERROR", Points: []logstore.LogPoint{pt(0, 2), pt(5, 9), pt(10, 4)}},
		{Name: "DEBUG", Points: []logstore.LogPoint{pt(0, 0)}}, // tamamen boş
		{Name: "WARN", Points: []logstore.LogPoint{pt(5, 7)}},
	}
	rows, grand := logHistSeriesRows(in)

	wantOrder := []string{"ERROR", "WARN", "INFO", "NOTICE"}
	if len(rows) != len(wantOrder) {
		t.Fatalf("seri sayısı %d, want %d (boş bant düşmeli): %+v", len(rows), len(wantOrder), rows)
	}
	for i, w := range wantOrder {
		if rows[i].Band != w {
			t.Fatalf("sıra[%d] = %s, want %s (kanonik bant sırası, bilinmeyen SONA)", i, rows[i].Band, w)
		}
	}
	if rows[0].Total != 15 || grand != 15+7+150+1 {
		t.Fatalf("toplamlar: seri=%d genel=%d", rows[0].Total, grand)
	}
	// Zirve kova: modelin "surge ne zaman başladı" sorusunu tek bakışta
	// cevaplaması için.
	if rows[0].Peak == nil || rows[0].Peak.V != 9 {
		t.Fatalf("peak = %+v, want v=9", rows[0].Peak)
	}
	if rows[0].Peak.T != base.Add(5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("peak damgası ISO olmalı ve kova BAŞLANGICI: %q", rows[0].Peak.T)
	}
	// Sıfır değerli nokta gövdeye GİRMEZ (sparse sözleşmesi).
	for _, r := range rows {
		for _, p := range r.Points {
			if p.V <= 0 {
				t.Fatalf("%s bandında sıfır nokta taşınmış: %+v", r.Band, p)
			}
		}
	}
	// Girdi mutasyona uğramaz.
	if in[0].Name != "INFO" {
		t.Fatalf("girdi dilimi yeniden sıralandı: %s", in[0].Name)
	}
}

func TestLogHistogramPayload(t *testing.T) {
	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	series := []logstore.LogSeries{
		{Name: "ERROR", Points: []logstore.LogPoint{{T: base.UnixNano(), V: 4}}},
	}
	body := logHistogramPayload("checkout", "level:error", "elasticsearch", 3600, 150, series)
	if body["window_s"] != 3600 || body["bucket_s"] != 150 {
		t.Fatalf("pencere/kova echo: %v / %v", body["window_s"], body["bucket_s"])
	}
	if body["backend"] != "elasticsearch" {
		t.Fatalf("backend echo = %v — hangi maliyet yolunun koştuğu görünmeli", body["backend"])
	}
	if body["service"] != "checkout" || body["query"] != "level:error" {
		t.Fatalf("filtre echo yok: %v", body)
	}
	// Sparse sözleşmesi gövdede: eksik kova "veri yok" DEĞİL "sıfır
	// eşleşme" demek, ve model bunu alan olarak görmeli.
	if body["empty_buckets_omitted"] != true {
		t.Fatal("sparse sözleşmesi gövdede ifşa edilmeli (empty_buckets_omitted)")
	}
	totals, ok := body["band_totals"].(map[string]int64)
	if !ok || totals["ERROR"] != 4 || body["total"] != int64(4) {
		t.Fatalf("toplamlar: %v / %v", body["band_totals"], body["total"])
	}
	if _, ok := body["reasons"]; ok {
		t.Fatal("dolu histogramda reasons TAŞINMAMALI")
	}

	t.Run("boş = hata değil ama sessiz de değil", func(t *testing.T) {
		empty := logHistogramPayload("checkout", "", "clickhouse", 1800, 75, nil)
		reasons, ok := empty["reasons"].([]string)
		if !ok || len(reasons) < 3 {
			t.Fatalf("reasons = %v", empty["reasons"])
		}
		joined := strings.ToLower(strings.Join(reasons, " | "))
		// Boş ile YAVAŞ aynı şekle düşüyor (partial bayrağı yok) →
		// doğrulama yolu nedenlerde OLMALI.
		if !strings.Contains(joined, "search_logs") {
			t.Errorf("nedenler doğrulama yolunu söylemiyor: %s", joined)
		}
		if !strings.Contains(joined, "service name") {
			t.Errorf("servis-adı eşleşmemesi hâli yok (harici log backend'inde en sık sebep): %s", joined)
		}
		if note, _ := empty["note"].(string); !strings.Contains(note, "zero errors") {
			t.Errorf("boş histogram notu 'sıfır hata = sağlıklı' çıkarımını yasaklamıyor: %q", note)
		}
	})

	t.Run("servis/sorgu boşsa alan taşınmaz", func(t *testing.T) {
		fleet := logHistogramPayload("", "", "clickhouse", 3600, 150, series)
		if _, ok := fleet["service"]; ok {
			t.Error("boş service alanı gövdeye girmemeli (model onu filtre sanır)")
		}
		if _, ok := fleet["query"]; ok {
			t.Error("boş query alanı gövdeye girmemeli")
		}
	})
}

// ─── kaynak pini (BAĞLANMA) ────────────────────────────────────

// Handler'lar gövdeyi SAF üreticiden döndürmeli — saf testler zarfı
// doğrular ama handler map'i satır içinde kurup zarfı düşürürse hepsi
// yeşil kalır.
func TestAnalysisHandlersReturnViaPurePayloads(t *testing.T) {
	src := analysisSource(t)
	for _, want := range []string{
		"return topologyPayload(service, windowS, rows, trimmed), nil",
		"return blastRadiusPayload(br, windowS), nil",
		"return logHistogramPayload(service, query, d.LogStore.Backend(), windowS, bucketS, series), nil",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("handler saf üreticiden dönmüyor — bekleniyor: %s", want)
		}
	}
}

// MUTASYON PİNİ — odaklı okuma ODAK-KAPSAMLI okuyucuyu kullanmalı.
// Filo okuyucusuyla (ReadServiceTopologyAgg) odaklamak SESSİZCE
// çalışır: cevap döner, ama 20k kenarı aşan kurulumda odağın sessiz
// bağımlılıkları LIMIT penceresinden düşer (v0.9.366'nın kapattığı bug).
func TestGetTopologyUsesFocusScopedReader(t *testing.T) {
	src := analysisSource(t)
	i := strings.Index(src, `if service != "" {`)
	if i < 0 {
		t.Fatal("odak dalı bulunamadı")
	}
	branch := src[i:]
	iFocus := strings.Index(branch, "ReadServiceTopologyAggForFocus(")
	iAll := strings.Index(branch, "ReadServiceTopologyAgg(ctx")
	if iFocus < 0 {
		t.Fatal("odaklı okuma ReadServiceTopologyAggForFocus kullanmıyor")
	}
	if iAll < 0 {
		t.Fatal("filo dalı ReadServiceTopologyAgg kullanmıyor")
	}
	if iFocus > iAll {
		t.Error("odak dalı FİLO okuyucusuna bağlanmış — sessizce çalışır ama odağın kenarları LIMIT penceresinden düşebilir (v0.9.366)")
	}
	if !strings.Contains(branch, "topoFocusHops") {
		t.Error("hop derinliği sabit olmalı (topoFocusHops) — yön ayrımı yalnız 1 hop'ta TAM tutar")
	}
}

// MUTASYON PİNİ — histogram çağrısı TÜRETİLEN kova genişliğini ve
// "severity" kırılımını geçmeli. bucketS yerine sabit bir değer (REST
// ucunun 30'u gibi) geçilirse kova SAYISI sınırsız kalır (v0.9.287);
// groupBy boşalırsa tek `_total` serisi döner ve açıklamanın vaat
// ettiği bant kırılımı sessizce kaybolur.
func TestGetLogHistogramPassesDerivedBucketAndSeverity(t *testing.T) {
	src := analysisSource(t)
	i := strings.Index(src, "d.LogStore.Histogram(ctx")
	if i < 0 {
		t.Fatal("Histogram çağrısı bulunamadı — tool başka bir okumaya mı bağlandı?")
	}
	call := src[i:]
	end := strings.Index(call, "\n\t\t\tif err != nil")
	if end > 0 {
		call = call[:end]
	}
	if !strings.Contains(call, `bucketS, "severity")`) {
		t.Errorf("Histogram çağrısı türetilmiş kova + severity kırılımı geçmiyor: %q", call)
	}
	if strings.Contains(call, "Env:") {
		t.Error("logstore.Filter'a Env geçilmiş — bu dönüş şekli 'env uygulanmadı' bayrağını TAŞIYAMAZ (v0.9.288 sınıfı)")
	}
}

// MUTASYON PİNİ — blast radius PENCERESİ çağıran tarafında kurulur ve
// store'a from/to olarak geçer. `time.Now()`e çapalı bir süre geçmek
// v0.9.1047'nin düzelttiği bug'ın kendisiydi (çözülmüş problemin
// penceresi yerine SON N dakika okunuyordu).
func TestGetBlastRadiusPassesWindowNotDuration(t *testing.T) {
	src := analysisSource(t)
	i := strings.Index(src, "GetServiceBlastRadius(ctx")
	if i < 0 {
		t.Fatal("GetServiceBlastRadius çağrısı bulunamadı")
	}
	line := src[i:]
	if end := strings.Index(line, "\n"); end > 0 {
		line = line[:end]
	}
	if !strings.Contains(line, "from, to") {
		t.Errorf("blast radius okuması pencere (from, to) geçmiyor: %q", line)
	}
}

// analysisSource — analysis.go'nun YORUMSUZ kaynağı. Yorumları
// ayıklamak zorunlu: gerekçe yorumları aranan dizeleri içeriyor ve
// ayıklamayan bir tarayıcı bu depoda KÖR koştu (kapı yorumu "tüketici"
// sanıyordu).
func analysisSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("analysis.go")
	if err != nil {
		t.Fatalf("analysis.go okunamadı: %v", err)
	}
	return stripLineComments(string(b))
}
