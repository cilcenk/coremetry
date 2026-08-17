package insight

import (
	"net/url"
	"strings"
	"testing"
)

// links_test.go — v0.9.1129 (AI Faz 2.1).
//
// İki hata sınıfı pinlendi:
//
//  1. PENCERESİZ LİNK. Hedef sayfa pencereyi URL'den çözer; yoksa
//     yapışkan aralığa düşer ve BOŞ liste çizer — "kayıt yok" gibi
//     okunur. Frontend'de dört kez ayrı ayrı gemiye gitti (v0.9.208,
//     v0.9.213×3). Bu yüzden aşağıda pencere gerektiren HER link için
//     `range=custom:` varlığı assert ediliyor, tek tek.
//  2. BİRİM KARIŞTIRMA (v0.6.36 sınıfı). Olay damgaları ns, `custom:`
//     token'ı ms, chart penceresi sn. Üç dönüşümün hepsi tabloda.

const sec = int64(1e9)

func TestRangeParamUnitsAndRejection(t *testing.T) {
	cases := []struct {
		name   string
		from   int64
		to     int64
		want   string
		reason string
	}{
		{name: "tam saniyeler", from: 1_700_000_000 * sec, to: 1_700_000_900 * sec,
			want: "custom:1700000000000-1700000900000"},
		// Üst kenar YUKARI yuvarlanır: kesilen ms en yeni kovayı düşürür.
		{name: "üst kenar yukarı yuvarlanır", from: 1_700_000_000 * sec, to: 1_700_000_000*sec + 1_500_000,
			want: "custom:1700000000000-1700000000002"},
		{name: "tam ms üst kenarı yuvarlanmaz", from: 1_700_000_000 * sec, to: 1_700_000_000*sec + 2_000_000,
			want: "custom:1700000000000-1700000000002"},
		// Alt kenar AŞAĞI (taban bölme) — pencere daralmasın.
		{name: "alt kenar aşağı yuvarlanır", from: 1_700_000_000*sec + 1_900_000, to: 1_700_000_100 * sec,
			want: "custom:1700000000001-1700000100000"},
		{name: "sıfır from", from: 0, to: 1_700_000_000 * sec, want: "", reason: "decodeRange reddeder"},
		{name: "sıfır to", from: 1_700_000_000 * sec, to: 0, want: ""},
		{name: "negatif", from: -5, to: 10, want: ""},
		{name: "ters pencere", from: 1_700_000_900 * sec, to: 1_700_000_000 * sec, want: ""},
		{name: "aynı an", from: 1_700_000_000 * sec, to: 1_700_000_000 * sec, want: ""},
		// ms çözünürlüğünün ALTINDA bir pencere: token üretilirse
		// toMs == fromMs olur ve decodeRange onu reddeder → boş dönmeli.
		{name: "1 ns pencere", from: 1_700_000_000 * sec, to: 1_700_000_000*sec + 1, want: "custom:1700000000000-1700000000001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RangeParam(tc.from, tc.to); got != tc.want {
				t.Errorf("RangeParam(%d,%d) = %q; want %q %s", tc.from, tc.to, got, tc.want, tc.reason)
			}
		})
	}
}

func TestExceptionWindow(t *testing.T) {
	now := int64(1_700_000_000) * sec
	cases := []struct {
		name     string
		first    int64
		last     int64
		wantFrom int64
		wantTo   int64
	}{
		{
			name: "eski grup: son oluşumun 30dk öncesi",
			// FirstSeen çok geride → alt kenar last-30dk.
			first: now - 10*24*3600*sec, last: now,
			wantFrom: now - 30*60*sec, wantTo: now + 5*60*sec,
		},
		{
			name: "genç grup: alt kenar FirstSeen-5dk (run-up pencerede)",
			first: now - 3*60*sec, last: now,
			wantFrom: now - 8*60*sec, wantTo: now + 5*60*sec,
		},
		{name: "damgasız", first: 0, last: 0, wantFrom: 0, wantTo: 0},
		{name: "yalnız last", first: 0, last: now, wantFrom: now - 30*60*sec, wantTo: now + 5*60*sec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := ExceptionWindow(tc.first, tc.last)
			if from != tc.wantFrom || to != tc.wantTo {
				t.Errorf("(%d,%d); want (%d,%d) [delta from=%ds to=%ds]",
					from, to, tc.wantFrom, tc.wantTo,
					(from-tc.wantFrom)/1e9, (to-tc.wantTo)/1e9)
			}
		})
	}
}

func TestMetricFamily(t *testing.T) {
	for _, tc := range []struct {
		metric string
		want   string
	}{
		{"error_rate", "error"}, {"ERROR_COUNT", "error"},
		{"p95_ms", "latency"}, {"p99", "latency"}, {"latency_avg", "latency"},
		{"duration_ms", "latency"}, {"response_ms", "latency"},
		{"throughput", "other"}, {"", "other"}, {"healthy_pods", "other"},
	} {
		if got := MetricFamily(tc.metric); got != tc.want {
			t.Errorf("MetricFamily(%q) = %q; want %q", tc.metric, got, tc.want)
		}
	}
}

// windowBearing — pencere TAŞIMASI ZORUNLU hedefler: sorguyu PENCEREYLE
// cevaplayan sayfalar. Pencereyi düşüren link burada boş liste çizer ve
// bu "kayıt yok" gibi okunur.
//
// Dışarıda kalanlar KİMLİK-ÇAPALI: /trace?id= tek bir nesne,
// /problems?problem=|exc= tek bir satır — aralık ne olursa olsun
// bulunurlar (pencere yine EKLENİR, ama yokluğu sessiz veri kaybı
// değil, o yüzden bu tabloda zorunlu tutulmuyor).
var windowBearing = map[string]bool{
	"/logs": true, "/traces": true, "/service": true,
}

// assertLinksCarryWindow — her linkin `range=custom:` taşıdığını (ya da
// muaf hedefte olduğunu) doğrular. MUTASYON KAPISI: bir üreticiden
// range'i düşürmek bu testi kırmızı yakar.
func assertLinksCarryWindow(t *testing.T, links []Link) {
	t.Helper()
	if len(links) == 0 {
		t.Fatal("hiç link üretilmedi")
	}
	for _, l := range links {
		u, err := url.Parse(l.Href)
		if err != nil {
			t.Fatalf("%q ayrıştırılamadı: %v", l.Href, err)
		}
		if !windowBearing[u.Path] {
			continue
		}
		rng := u.Query().Get("range")
		if rng == "" {
			t.Errorf("%s (%s) pencere TAŞIMIYOR — hedef yapışkan aralığa düşer ve boş liste çizer",
				l.Label, l.Href)
			continue
		}
		if !strings.HasPrefix(rng, "custom:") {
			t.Errorf("%s range=%q; mutlak pencere yalnız `custom:` ile taşınır", l.Label, rng)
		}
	}
}

func TestExceptionLinks(t *testing.T) {
	now := int64(1_700_000_000) * sec
	ev := ExceptionEvidence{
		Fingerprint: "fp/1+2", Service: "checkout svc", TraceID: "abc123",
		FirstSeenNs: now - 3*24*3600*sec, LastSeenNs: now, NowNs: now,
	}
	links := ExceptionLinks(ev)
	assertLinksCarryWindow(t, links)

	var labels []string
	for _, l := range links {
		labels = append(labels, l.Label)
	}
	wantOrder := []string{"Exception grubu", "Örnek trace", "Hatalı trace'ler", "Loglar (error+)", "Servis"}
	if strings.Join(labels, "|") != strings.Join(wantOrder, "|") {
		t.Errorf("link sırası = %v; want %v", labels, wantOrder)
	}

	byLabel := map[string]string{}
	for _, l := range links {
		byLabel[l.Label] = l.Href
	}
	// Fingerprint ve servis adı KODLANMALI (eğik çizgi, artı, boşluk).
	if got := byLabel["Exception grubu"]; !strings.Contains(got, "exc=fp%2F1%2B2") {
		t.Errorf("fingerprint kodlanmamış: %s", got)
	}
	// Kimlik-çapalı hedefler de pencere TAŞIR (zorunlu değil ama
	// sayfanın çevresi operatörün olayıyla aynı aralığı göstermeli).
	if got := byLabel["Exception grubu"]; !strings.Contains(got, "range=custom%3A") {
		t.Errorf("grup linki pencere taşımıyor: %s", got)
	}
	if got := byLabel["Servis"]; !strings.Contains(got, "name=checkout+svc") {
		t.Errorf("servis adı kodlanmamış: %s", got)
	}
	// rootOnly=false ŞART: exception span'i trace'in ortasında (v0.8.585).
	tr, _ := url.Parse(byLabel["Hatalı trace'ler"])
	if tr.Query().Get("rootOnly") != "false" || tr.Query().Get("hasError") != "true" {
		t.Errorf("hatalı-trace linki yanlış: %s", byLabel["Hatalı trace'ler"])
	}
	// /logs severity tabanı: param adı `severity` (readLogsParams). `minSev`
	// yazan bir link ÖLÜ param taşır ve hedef "tüm seviyeler"de açılır.
	lg, _ := url.Parse(byLabel["Loglar (error+)"])
	if lg.Query().Get("severity") != severityErrorFloor {
		t.Errorf("log linki severity tabanı taşımıyor: %s", byLabel["Loglar (error+)"])
	}
	if strings.Contains(byLabel["Loglar (error+)"], "minSev") {
		t.Error("log linki ölü `minSev` paramı taşıyor — /logs `severity` okuyor")
	}
	// Trace linki pencere taşımaz (nokta nesnesi) ama id taşır.
	if byLabel["Örnek trace"] != "/trace?id=abc123" {
		t.Errorf("trace linki = %s", byLabel["Örnek trace"])
	}
}

func TestExceptionLinksWithoutWindowDropsWindowBearers(t *testing.T) {
	// Damgasız grup: pencere kurulamaz → pencere isteyen link ÜRETİLMEZ.
	// Kırık pencereli bir link, link yokluğundan kötü (v0.9.655 ilkesi).
	ev := ExceptionEvidence{Fingerprint: "fp1", Service: "checkout", TraceID: "t1"}
	links := ExceptionLinks(ev)
	for _, l := range links {
		u, _ := url.Parse(l.Href)
		if windowBearing[u.Path] {
			t.Errorf("penceresiz kurulumda %s üretildi: %s", l.Label, l.Href)
		}
	}
	// Grup linki ve trace linki hâlâ olmalı — ikisi de kimlik-çapalı.
	if len(links) != 2 {
		t.Errorf("beklenen 2 link (grup + trace), gelen %d: %+v", len(links), links)
	}
}

func TestProblemLinks(t *testing.T) {
	now := int64(1_700_000_000) * sec
	base := ProblemEvidence{
		ID: "prob-42", Service: "checkout", FromNs: now - 3600*sec, ToNs: now, NowNs: now,
	}

	t.Run("hata ailesi", func(t *testing.T) {
		ev := base
		ev.Metric = "error_rate"
		links := ProblemLinks(ev)
		assertLinksCarryWindow(t, links)
		if !hasLabel(links, "Hatalı trace'ler") {
			t.Errorf("hata ailesinde hatalı-trace linki yok: %+v", links)
		}
	})
	t.Run("gecikme ailesi", func(t *testing.T) {
		ev := base
		ev.Metric = "p95_ms"
		links := ProblemLinks(ev)
		assertLinksCarryWindow(t, links)
		href, ok := hrefOf(links, "En yavaş trace'ler")
		if !ok {
			t.Fatalf("gecikme ailesinde en-yavaş linki yok: %+v", links)
		}
		u, _ := url.Parse(href)
		if u.Query().Get("sort") != "duration" {
			t.Errorf("en-yavaş linki sort=duration taşımıyor: %s", href)
		}
	})
	t.Run("diğer aile", func(t *testing.T) {
		ev := base
		ev.Metric = "throughput"
		links := ProblemLinks(ev)
		assertLinksCarryWindow(t, links)
		if !hasLabel(links, "Trace'ler") {
			t.Errorf("nötr ailede düz trace linki yok: %+v", links)
		}
	})
	t.Run("problem detayı e-posta şekliyle aynı param", func(t *testing.T) {
		links := ProblemLinks(base)
		href, _ := hrefOf(links, "Problem detayı")
		if !strings.HasPrefix(href, "/problems?") || !strings.Contains(href, "problem=prob-42") {
			t.Errorf("problem detay linki = %s; notify.go /problems?problem= şeklini bekliyor", href)
		}
		if !strings.Contains(href, "range=custom%3A") {
			t.Errorf("problem detay linki pencere taşımıyor: %s", href)
		}
	})
	t.Run("kök-neden adayı kendi sayfasına", func(t *testing.T) {
		ev := base
		ev.Hyp = &HypothesisRef{TopSuspect: "payments-api", Confidence: 0.8}
		links := ProblemLinks(ev)
		assertLinksCarryWindow(t, links)
		href, ok := hrefOf(links, "Aday: payments-api")
		if !ok {
			t.Fatalf("aday linki yok: %+v", links)
		}
		if !strings.Contains(href, "name=payments-api") {
			t.Errorf("aday linki = %s", href)
		}
	})
	t.Run("aday problemin servisiyse ikinci link yok", func(t *testing.T) {
		ev := base
		ev.Hyp = &HypothesisRef{TopSuspect: "checkout", Confidence: 0.8}
		for _, l := range ProblemLinks(ev) {
			if strings.HasPrefix(l.Label, "Aday: ") {
				t.Errorf("kendi servisi için ikinci link üretildi: %s", l.Href)
			}
		}
	})
	t.Run("penceresiz problem yalnız kimlik linki verir", func(t *testing.T) {
		ev := ProblemEvidence{ID: "p1", Service: "checkout"}
		links := ProblemLinks(ev)
		if len(links) != 1 || links[0].Label != "Problem detayı" {
			t.Errorf("penceresiz kurulumda linkler = %+v", links)
		}
	})
}

func hasLabel(links []Link, label string) bool {
	_, ok := hrefOf(links, label)
	return ok
}

func hrefOf(links []Link, label string) (string, bool) {
	for _, l := range links {
		if l.Label == label {
			return l.Href, true
		}
	}
	return "", false
}

func TestChartsFollowTheMetricFamily(t *testing.T) {
	now := int64(1_700_000_000) * sec
	for _, tc := range []struct {
		metric string
		want   string
	}{{"error_rate", "error_rate"}, {"p99", "p95"}, {"throughput", "rate"}} {
		got := ProblemCharts(ProblemEvidence{Service: "s", Metric: tc.metric,
			FromNs: now - 900*sec, ToNs: now})
		if len(got) != 1 {
			t.Fatalf("%s: chart sayısı %d", tc.metric, len(got))
		}
		if got[0].Agg != tc.want {
			t.Errorf("%s → agg %q; want %q", tc.metric, got[0].Agg, tc.want)
		}
		if got[0].RangeS != 900 {
			t.Errorf("%s → rangeS %d; want 900 (pencere SANİYE)", tc.metric, got[0].RangeS)
		}
	}
	// Servissiz problem → chart YOK (uydurma servisle boş panel çizmeyiz).
	if got := ProblemCharts(ProblemEvidence{Metric: "error_rate", FromNs: now - sec, ToNs: now}); got != nil {
		t.Errorf("servissiz chart üretildi: %+v", got)
	}
}

func TestExceptionChartsUseEventWindow(t *testing.T) {
	now := int64(1_700_000_000) * sec
	got := ExceptionCharts(ExceptionEvidence{Service: "checkout",
		FirstSeenNs: now - 10*24*3600*sec, LastSeenNs: now, NowNs: now})
	if len(got) != 1 {
		t.Fatalf("chart sayısı %d", len(got))
	}
	if got[0].Agg != "error_rate" {
		t.Errorf("agg = %q", got[0].Agg)
	}
	// Pencere = ExceptionWindow (30dk+5dk) → 2100 sn.
	if got[0].RangeS != 2100 {
		t.Errorf("rangeS = %d; want 2100", got[0].RangeS)
	}
	if got := ExceptionCharts(ExceptionEvidence{Service: ""}); got != nil {
		t.Errorf("servissiz chart üretildi: %+v", got)
	}
}
