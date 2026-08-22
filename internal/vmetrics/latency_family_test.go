package vmetrics

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1274 — OPERATÖR-BİLDİRİMİ, prod. Service Overview'ın "Response time ·
// avg (by route)" paneli ve Response time KAROSU eksende "14.2 weeks" /
// "17.6 hours" gösteriyordu; panelin kendi sorgu metni suçu itiraf ediyordu:
// `avg(http_server_request_duration_seconds_count) by (http.route)`.
//
// SEMPTOMUN KAYNAĞI ADIN KENDİSİYDİ. Throughput ucu VM kurulumunda
// `…_seconds_count` serisini çözer — histogramın hızı `rate(_count)` olduğu
// için bu THROUGHPUT açısından doğru cevaptır. Overview ise tek bir "çözülmüş
// metrik" alanı okuyup aynı adı `agg=avg` ile RT panellerine taşıyordu.
// buildPromQL açık bir `_count` adını "operatör bilinçli seçti" sayar
// (mayHaveHistogramParts'ın belgelenmiş reddi) ve tek kollu ham `avg(...)`
// üretir: kümülatif bir SAYACIN ortalaması ≈ 8,5M, ve `_seconds` soneki birimi
// "s" yaptığı için hafta olarak biçimlendi. v0.6.36'nın birim-karışımı sınıfı,
// yeni bir yüzeyde.
//
// Tablo her sonek SINIFINI ayrı yürütür: kırpılan üç parça, kırpılmayan üç
// sınıf, ve kırpımın kendini iptal ettiği iki sınır.
func TestLatencyFamilyName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		why  string
	}{
		{
			name: "count part trimmed — bildirilen bug",
			in:   "http_server_request_duration_seconds_count",
			want: "http_server_request_duration_seconds",
			why:  "aileye inilmezse avg kümülatif sayacın ortalamasını çizer (eksen: 14.2 weeks)",
		},
		{
			name: "sum part trimmed",
			in:   "http_server_request_duration_seconds_sum",
			want: "http_server_request_duration_seconds",
			why:  "`_sum` de bir PARÇA: ham toplamın ortalaması bir gecikme değildir",
		},
		{
			name: "bucket part trimmed",
			in:   "http_server_request_duration_seconds_bucket",
			want: "http_server_request_duration_seconds",
			why:  "`_bucket` avg altında `le` başına bir seri demek",
		},
		{
			name: "unit-suffix'siz aile de kırpılır",
			in:   "db_client_operation_duration_count",
			want: "db_client_operation_duration",
			why:  "kural parça sonekine bakar, birim sonekine değil",
		},
		{
			name: "counter suffix DEĞİŞMEZ",
			in:   "http_requests_total",
			want: "http_requests_total",
			why:  "`_total` monotonik bir TOPLAM; histogram kardeşi yok, kırpım var olmayan seriye işaret ederdi",
		},
		{
			name: "noktalı OTel adı DEĞİŞMEZ",
			in:   "http.server.request.duration",
			want: "http.server.request.duration",
			why:  "aile adı zaten bu; kırpılacak parça soneki yok",
		},
		{
			name: "noktalı `.count` adı DEĞİŞMEZ",
			in:   "queue.message.count",
			want: "queue.message.count",
			why:  "kural HAM ad üzerinde: sanitize edilmiş hâle bakılsaydı masum bir gauge ailesinin adı kırpılırdı",
		},
		{
			name: "tek başına parça soneki DEĞİŞMEZ",
			in:   "_count",
			want: "_count",
			why:  "kırpım boş ad bırakır; sorulan adı yok etmektense dokunmamak doğru",
		},
		{
			name: "parça üstüne parça DEĞİŞMEZ",
			in:   "http_server_duration_sum_count",
			want: "http_server_duration_sum_count",
			why:  "kırpım `…_sum` verir, o da or bileşimini AÇMAZ: ad değişir, hiçbir şey düzelmez",
		},
		{
			name: "boş ad boş kalır",
			in:   "",
			want: "",
			why:  "boş ad çağıranın hatası; burada üretilecek bir aile yok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := latencyFamilyName(tc.in); got != tc.want {
				t.Errorf("latencyFamilyName(%q) = %q, want %q\n\tNEDEN: %s", tc.in, got, tc.want, tc.why)
			}
		})
	}
}

// ÖN ŞART PİNİ. Kırpma yalnız buildPromQL'in avg dalındaki `or` bileşimini
// AÇTIĞI için işe yarar, ve o bileşimin kapısı mayHaveHistogramParts'tır.
// Kırpılmış taban o kapıdan geçmiyorsa düzeltme adı değiştirir ama sorguyu
// düzeltmez — sessiz bir no-op. İki uç da pinleniyor: bug'lı adın kapıyı
// AÇMADIĞI (yoksa bug hiç oluşmazdı) ve kırpılmış tabanın AÇTIĞI.
func TestTrimmedLatencyFamilyReopensTheHistogramArms(t *testing.T) {
	const reported = "http_server_request_duration_seconds_count"

	if mayHaveHistogramParts(reported) {
		t.Fatal("ön koşul kayboldu: `…_count` adı histogram kollarını zaten açıyorsa bu bug hiç oluşmazdı")
	}
	base := latencyFamilyName(reported)
	if !mayHaveHistogramParts(base) {
		t.Fatalf("kırpılmış taban %q histogram kollarını AÇMIYOR — kırpma sessiz bir no-op", base)
	}

	// Ve avg aday listesi ailenin İKİ parçasını da taşıyor: or bileşiminin sağ
	// kolu `sum(rate(_sum)) / sum(rate(_count))`, yani panelin çizdiği
	// gözlem-ağırlıklı ortalamanın ta kendisi (v0.9.1160; CH'nin sum/count
	// kolon semantiğiyle aynı cevap).
	cands := nameCandidates(base, "avg")
	for _, want := range []string{
		"http_server_request_duration_seconds_sum",
		"http_server_request_duration_seconds_count",
	} {
		found := false
		for _, c := range cands {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("avg aday listesi %q taşımıyor — or bileşiminin bir kolu boş kalır\n\taday listesi: %v", want, cands)
		}
	}
}

// ÜRETİLEN İFADE. Zincirin sonunda operatörün VM sorgu kaydında göreceği metin
// budur, ve düzeltmenin öncesi/sonrası tam burada okunur: solda kırpılmamış
// adın TEK KOLLU ham ortalaması (bug), sağda ailenin or-bileşimli oranı.
//
// Fixture operatörün gerçek panelini taklit ediyor — service.name matcher'ı +
// http.route grubu — çünkü avg dalı guardBucketScan'den geçiyor ve filtresiz
// bir fixture kapıyı test etmek yerine kapıya takılırdı (v0.9.1164).
func TestLatencyFamilyChangesTheRenderedAvgExpression(t *testing.T) {
	render := func(name string) string {
		q, err := buildPromQL(chstore.MetricQueryFilter{
			Name:        name,
			Aggregation: "avg",
			GroupBy:     []string{"http.route"},
			Filters: []chstore.FilterExpr{
				{Key: "service.name", Op: "=", Values: []string{"bsa-checkout"}},
			},
			StepSeconds: 60,
		}, promOpts{})
		if err != nil {
			t.Fatalf("buildPromQL(%q): %v", name, err)
		}
		return q
	}

	before := render("http_server_request_duration_seconds_count")
	after := render(latencyFamilyName("http_server_request_duration_seconds_count"))

	// ÖNCE: tek kol, ham avg — hiçbir rate yok, yani kümülatif sayaç.
	if strings.Contains(before, " or ") || strings.Contains(before, "rate(") {
		t.Fatalf("düzeltme öncesi ifade beklenen tek-kollu ham hâlde değil — bu test yanlış şeyi ölçüyor:\n\t%s", before)
	}
	// SONRA: or bileşimi + sum/count oranı.
	if !strings.Contains(after, " or ") {
		t.Fatalf("düzeltme sonrası ifade or bileşimi taşımıyor:\n\t%s", after)
	}
	if !strings.Contains(after, "_seconds_sum") || !strings.Contains(after, "_seconds_count") {
		t.Fatalf("düzeltme sonrası ifade sum/count oranını kurmuyor:\n\t%s", after)
	}
	if !strings.Contains(after, "rate(") {
		t.Fatalf("düzeltme sonrası ifade rate() taşımıyor — kümülatif sayaçların oranı tüm-zaman ortalamasıdır:\n\t%s", after)
	}

	t.Logf("ÖNCE (bug): %s", before)
	t.Logf("SONRA     : %s", after)
}

// Seam metodu saf fonksiyona bağlı kalsın: api.metricSource bu METODU çağırıyor,
// testler ise saf fonksiyonu. İkisi ayrışırsa düzeltme testte yaşar, üründe
// ölür.
func TestServiceLatencyMetricNameDelegates(t *testing.T) {
	svc := New()
	for _, in := range []string{
		"http_server_request_duration_seconds_count",
		"http_requests_total",
		"http.server.request.duration",
	} {
		if got, want := svc.LatencyMetricName(in), latencyFamilyName(in); got != want {
			t.Errorf("Service.LatencyMetricName(%q) = %q, saf kural %q diyor", in, got, want)
		}
	}
}
