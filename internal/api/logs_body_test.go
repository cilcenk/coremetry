// v0.9.1094 regresyon testi — Logs "Load more" transport düzeltmesi.
//
// Operator-reported (prod): Load more → "Query failed / Failed to
// fetch". ES keyset cursor'ı base64(PIT id + search_after) taşır ve PIT
// id'leri KB'larca olur; GET URL'si ingress istek-satırı sınırını aşınca
// bağlantı ağ katmanında resetlenir — ilk sayfa hep çalışır, ikincisi
// ölür. Liste artık POST gövdesiyle sorulur; bu test gövde→values
// çevriminin GET sözleşmesiyle bire bir kalmasını çiviler.
package api

import "testing"

func TestLogsBodyToValues(t *testing.T) {
	vals := logsBodyToValues(map[string]any{
		"service":  "checkout",
		"after":    "eyJwaXQiOiJ2ZXJ5LWxvbmctcGl0LWlkIn0", // cursor GÖVDEDE taşınır — URL sınırı yok
		"hasTrace": true,
		"paging":   true,
		"asc":      false,          // false → HİÇ yazılmaz (parseBoolParam sözleşmesi)
		"limit":    float64(100),   // JSON sayısı → ondalıksız metin
		"severity": float64(17),
		"nested":   map[string]any{"x": 1}, // GET'te ifade edilemez → sızmaz
	})
	cases := []struct{ key, want string }{
		{"service", "checkout"},
		{"after", "eyJwaXQiOiJ2ZXJ5LWxvbmctcGl0LWlkIn0"},
		{"hasTrace", "1"},
		{"paging", "1"},
		{"asc", ""},
		{"limit", "100"},
		{"severity", "17"},
		{"nested", ""},
	}
	for _, c := range cases {
		if got := vals.Get(c.key); got != c.want {
			t.Errorf("%s = %q, beklenen %q", c.key, got, c.want)
		}
	}
}
