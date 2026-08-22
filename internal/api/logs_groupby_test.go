package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.9.1250 — /api/logs/timeseries kırılım ekseni whitelist'i.
//
// Sözleşme: bir eksen ancak backend'de GERÇEKTEN uygulanmışsa geçer.
// İki logstore backend'i de tanımadıkları groupBy'ı tek '_total'
// serisine indirir — makul görünen sessiz bir cevapsızlık; v0.9.1220
// cluster/namespace'i tam bu yüzden sunmamıştı. Whitelist o sözü
// taşıyan yer: buradan düşen bir eksen frontend select'inde dursa bile
// telde hiçbir şey değiştirmez.
//
// Mutasyon kapısı: listeden "namespace"i düşür → aşağıdaki vaka kırmızı.
func TestNormalizeLogsGroupBy(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "seviye", in: "severity", want: "severity"},
		{name: "servis", in: "service", want: "service"},
		{name: "cluster (v0.9.1250)", in: "cluster", want: "cluster"},
		{name: "namespace (v0.9.1250)", in: "namespace", want: "namespace"},
		{name: "boşluk kırpılır", in: "  cluster  ", want: "cluster"},
		{name: "boş = toplam", in: "", want: ""},
		// Bilinmeyen değer MEVCUT davranışı korur: 400 değil (eski
		// kayıtlı görünümler ve elle kurulmuş URL'ler çizmeye devam
		// eder), sessizce severity'ye kaydırma da değil (operatör
		// istemediği bir kırılıma bakmış olmaz).
		{name: "bilinmeyen eksen toplama düşer", in: "pod", want: ""},
		{name: "büyük harf eşleşmez", in: "Cluster", want: ""},
		{name: "çöp", in: "'; DROP", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeLogsGroupBy(c.in); got != c.want {
				t.Fatalf("normalizeLogsGroupBy(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Normalizasyon cache anahtarından ÖNCE koşar: aynı cevabı üreten
// tanınmayan değerler tek girdi paylaşır, tanınan eksenler ise ASLA
// birbirinin girdisini görmez (v0.5.187 çapraz-zehirlenme sınıfı).
func TestLogsTimeseriesKey_AxisSeparation(t *testing.T) {
	f := logstore.Filter{Service: "checkout", Cluster: "ocp5"}
	keyOf := func(axis string) string {
		return logsTimeseriesKey(f, "now-1h", "now", 30, normalizeLogsGroupBy(axis))
	}
	seen := map[string]string{}
	for _, axis := range []string{"severity", "service", "cluster", "namespace"} {
		k := keyOf(axis)
		if prev, dup := seen[k]; dup {
			t.Fatalf("%s ve %s aynı cache anahtarını paylaşıyor: %s", prev, axis, k)
		}
		seen[k] = axis
	}
	if keyOf("pod") != keyOf("çöp") {
		t.Error("tanınmayan iki eksen aynı cevabı üretiyor ama farklı anahtar tutuyor")
	}
}
