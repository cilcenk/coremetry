package insight

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// contract_test.go — v0.9.1129 (AI Faz 2.1) sözleşme pinleri.
//
// Kartın sözleşmesinde iki şey SESSİZ kırılır ve ikisi de burada
// pinlendi: (1) nil dilim → JSON `null` → FE'de `.map` çöker (v0.9.836
// sınıfı), (2) doğrulanmamış chart spec → FE bilinmeyen agg'i sessizce
// 'rate'e düşürür, yani operatör YANLIŞ seriyi doğru sanır.

func TestNormalizeNeverEmitsNullSlices(t *testing.T) {
	var r Response
	r.Normalize()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"signals":[]`, `"links":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("gövde %s taşımıyor: %s", want, got)
		}
	}
	if strings.Contains(got, "null") {
		t.Errorf("gövdede null var — FE .map/.filter çağırıyor: %s", got)
	}
}

func TestWithoutProseKeepsEverythingElse(t *testing.T) {
	r := Response{
		Prose:      "uzun anlatı",
		Signals:    []Signal{{Kind: SignalProblem, Label: "Şiddet", Value: "kritik"}},
		Links:      []Link{{Label: "Servis", Href: "/service?name=a"}},
		Charts:     []ChartSpec{{Service: "a", Agg: "p95", RangeS: 600}},
		ExchangeID: "xid",
		Model:      "gemma4",
		Truncated:  true,
	}
	f := r.WithoutProse()
	if f.Prose != "" {
		t.Errorf("signals çerçevesi prose taşıyor: %q", f.Prose)
	}
	if len(f.Signals) != 1 || len(f.Links) != 1 || len(f.Charts) != 1 {
		t.Errorf("deterministik yarı kayboldu: %+v", f)
	}
	if f.ExchangeID != "xid" || f.Model != "gemma4" || !f.Truncated {
		t.Errorf("meta alanları kayboldu: %+v", f)
	}
	// Kaynak değişmemeli — çağıran prose'u sonra dolduruyor.
	if r.Prose != "uzun anlatı" {
		t.Error("WithoutProse kaynağı mutasyona uğrattı")
	}
}

func TestNewChartSpec(t *testing.T) {
	cases := []struct {
		name      string
		title     string
		service   string
		operation string
		agg       string
		rangeS    int64
		wantOK    bool
		wantRange int64
		wantTitle string
	}{
		{name: "geçerli", service: "checkout", agg: "p95", rangeS: 600,
			wantOK: true, wantRange: 600, wantTitle: "checkout · p95"},
		{name: "operasyon başlığa girer", service: "checkout", operation: "GET /pay",
			agg: "error_rate", rangeS: 900, wantOK: true, wantRange: 900,
			wantTitle: "GET /pay · error_rate"},
		{name: "açık başlık korunur", title: "Özel", service: "s", agg: "rate",
			rangeS: 60, wantOK: true, wantRange: 60, wantTitle: "Özel"},
		{name: "servis boş", service: "  ", agg: "p95", rangeS: 60},
		{name: "bilinmeyen agg", service: "s", agg: "apdex", rangeS: 60},
		{name: "boş agg", service: "s", agg: "", rangeS: 60},
		// Pencere bandı: 0/negatif → varsayılan, tavan aşımı → kırpılır.
		{name: "sıfır pencere varsayılana düşer", service: "s", agg: "rate", rangeS: 0,
			wantOK: true, wantRange: ChartRangeDefaultS, wantTitle: "s · rate"},
		{name: "negatif pencere varsayılana düşer", service: "s", agg: "rate", rangeS: -5,
			wantOK: true, wantRange: ChartRangeDefaultS, wantTitle: "s · rate"},
		{name: "tavan kırpılır", service: "s", agg: "rate", rangeS: 30 * 86400,
			wantOK: true, wantRange: ChartRangeMaxS, wantTitle: "s · rate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NewChartSpec(tc.title, tc.service, tc.operation, tc.agg, tc.rangeS)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v; want %v (%+v)", ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if got.RangeS != tc.wantRange {
				t.Errorf("rangeS = %d; want %d", got.RangeS, tc.wantRange)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("title = %q; want %q", got.Title, tc.wantTitle)
			}
		})
	}
}

// TestChartSpecJSONShapeMatchesChatChartBlock — kart spec'i, chat'in
// ```chart``` fence'iyle AYNI anahtar kümesini basmalı: frontend'in
// çizicisi (CosreChart) tek şekil biliyor. guidedChartSpec'in etiketleri
// title/service/operation/agg/rangeS.
func TestChartSpecJSONShapeMatchesChatChartBlock(t *testing.T) {
	b, err := json.Marshal(ChartSpec{Title: "t", Service: "s", Operation: "o", Agg: "p99", RangeS: 1800})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"title":"t","service":"s","operation":"o","agg":"p99","rangeS":1800}`
	if string(b) != want {
		t.Errorf("spec JSON = %s\nwant                   %s", b, want)
	}
}

// TestChartAggWhitelistMatchesRenderChart — KAYNAK PİNİ.
//
// Agg whitelist'i üç yerde yaşıyor: burada, mcptools.renderChartMetrics
// ve frontend'in AGG_META'sı. Bilinmeyen bir agg frontend'de sessizce
// 'rate'e düşüyor, yani ayrışma "yanlış seri, doğru görünüm" olarak
// çıkıyor — kullanıcı fark etmez. Go tarafındaki iki yazılışın
// ayrışmasını burada kırmızı yakıyoruz.
func TestChartAggWhitelistMatchesRenderChart(t *testing.T) {
	const src = "../../mcptools/tools.go"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("%s okunamadı (dosya taşındıysa bu pini yeniden konumlandır): %v", src, err)
	}
	i := strings.Index(string(b), "var renderChartMetrics = map[string]bool{")
	if i < 0 {
		t.Fatalf("%s içinde renderChartMetrics bulunamadı — pini yeniden konumlandır", src)
	}
	block := string(b)[i:]
	if j := strings.Index(block, "}"); j > 0 {
		block = block[:j]
	}
	quoted := regexp.MustCompile(`"([a-z0-9_]+)"`).FindAllStringSubmatch(block, -1)
	var theirs []string
	for _, m := range quoted {
		theirs = append(theirs, m[1])
	}
	var ours []string
	for k := range chartAggs {
		ours = append(ours, k)
	}
	sort.Strings(theirs)
	sort.Strings(ours)
	if strings.Join(theirs, ",") != strings.Join(ours, ",") {
		t.Errorf("agg whitelist ayrıştı:\n  mcptools: %v\n  insight : %v", theirs, ours)
	}
}

func TestKnownKind(t *testing.T) {
	for _, k := range Kinds() {
		if !KnownKind(k) {
			t.Errorf("KnownKind(%q) = false", k)
		}
	}
	for _, k := range []string{"", "Exception", "problems", "log", "slow-span"} {
		if KnownKind(k) {
			t.Errorf("KnownKind(%q) = true; bilinmeyen tür 404 olmalı", k)
		}
	}
}
