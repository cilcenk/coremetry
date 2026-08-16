// v0.9.1094 regresyon testi — Logs "Load more" transport düzeltmesi.
//
// Operator-reported (prod): Load more → "Query failed / Failed to
// fetch". ES keyset cursor'ı base64(PIT id + search_after) taşır ve PIT
// id'leri KB'larca olur; GET URL'si ingress istek-satırı sınırını aşınca
// bağlantı ağ katmanında resetlenir — ilk sayfa hep çalışır, ikincisi
// ölür. Liste artık POST gövdesiyle sorulur; bu test gövde→values
// çevriminin GET sözleşmesiyle bire bir kalmasını çiviler.
package api

import (
	"strings"
	"testing"
)

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

// v0.9.1096 — 64KB LimitReader kendi bug'ımızdı: prod ES PIT id'leri
// (yüzlerce shard) on-KB'larca; 2. sayfa gövdesi kesilince Load more
// 400 "invalid body" yiyordu. Sınır 1MB + üç ayrı teşhis mesajı.
func TestDecodeLogsBody(t *testing.T) {
	t.Run("100KB cursor GEÇER (eski 64KB sınırı burada ölürdü)", func(t *testing.T) {
		big := strings.Repeat("A", 100<<10)
		vals, err := decodeLogsBody(strings.NewReader(`{"after":"` + big + `","limit":5}`))
		if err != nil {
			t.Fatalf("100KB gövde geçmeli: %v", err)
		}
		if len(vals.Get("after")) != 100<<10 || vals.Get("limit") != "5" {
			t.Errorf("değerler eksik geldi")
		}
	})
	t.Run("boş gövde ayrı mesaj", func(t *testing.T) {
		_, err := decodeLogsBody(strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "empty request body") {
			t.Errorf("boş gövde teşhisi bekleniyordu: %v", err)
		}
	})
	t.Run("1MB üstü ayrı mesaj", func(t *testing.T) {
		big := strings.Repeat("A", (1<<20)+100)
		_, err := decodeLogsBody(strings.NewReader(`{"after":"` + big + `"}`))
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("boyut aşımı teşhisi bekleniyordu: %v", err)
		}
	})
	t.Run("bozuk JSON bayt sayısıyla", func(t *testing.T) {
		_, err := decodeLogsBody(strings.NewReader(`{"after": kesik`))
		if err == nil || !strings.Contains(err.Error(), "bytes read") {
			t.Errorf("bozuk JSON teşhisi bekleniyordu: %v", err)
		}
	})
}
