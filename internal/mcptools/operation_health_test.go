package mcptools

// operation_health_test.go — v0.9.1227 saf çekirdek pinleri.

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestOpHealthWindowS(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 3600},          // varsayılan 1h
		{-5, 3600},         // negatif → varsayılan
		{30, 60},           // kova tabanı (1m)
		{60, 60},           // tam kova
		{7200, 7200},       // aralıkta aynen
		{99 * 86400, 604800}, // tavan 7g
	}
	for _, c := range cases {
		if got := opHealthWindowS(c.in); got != c.want {
			t.Errorf("opHealthWindowS(%d) = %d, beklenen %d", c.in, got, c.want)
		}
	}
}

func TestOpHealthSort(t *testing.T) {
	cases := map[string]string{
		"": "calls", "calls": "calls", "p99": "p99Ms",
		"error_rate": "errorRate", "impact": "impact",
		"'; DROP TABLE spans--": "calls", // bilinmeyen → calls (SQL'e asla değmez)
	}
	for in, want := range cases {
		if got := opHealthSort(in); got != want {
			t.Errorf("opHealthSort(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

func TestOpHealthRows(t *testing.T) {
	rows := opHealthRows([]chstore.EndpointRow{{
		Service: "bsa-pay", Path: "/api/v1/pay", Method: "POST",
		Calls: 1000, Errors: 12, ErrorRate: 1.2,
		AvgMs: 45.5, P50Ms: 30, P95Ms: 120, P99Ms: 480.25, ReqPerMin: 16.7,
	}})
	if len(rows) != 1 {
		t.Fatalf("1 satır beklenirdi, %d geldi", len(rows))
	}
	r := rows[0]
	// Service alanı BİLİNÇLİ yok (zarf zaten taşıyor); ondalıklar HAM.
	if r.Path != "/api/v1/pay" || r.Method != "POST" || r.Calls != 1000 ||
		r.ErrorRatePct != 1.2 || r.P99Ms != 480.25 || r.ReqPerMin != 16.7 {
		t.Errorf("satır alanları yanlış eşlendi: %+v", r)
	}
}
