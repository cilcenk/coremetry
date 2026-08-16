package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1111 — /services compare=prior + sort=p99Delta saf çekirdekleri.
// Endpoints'in v0.9.761/812 desenine servis eşi; buradaki semantik
// gevşerse "kötüleşenler" listesi ya sıra atlar ya sessizce kırpılır.

func TestServicesDeltaPool(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"varsayılan sayfa (50) tabana oturur", 50, 250},
		{"küçük limit tabanın altına inmez", 10, 250},
		{"orta limit ×5", 100, 500},
		{"tavan 2000", 500, 2000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := servicesDeltaPool(c.limit); got != c.want {
				t.Errorf("servicesDeltaPool(%d) = %d, beklenen %d", c.limit, got, c.want)
			}
		})
	}
}

func TestSortServicesByP99Delta(t *testing.T) {
	rows := []chstore.ServiceSummary{
		{Name: "steady", P99Ms: 100, PriorP99Ms: 100, SpanCount: 50},
		{Name: "worse-2x", P99Ms: 200, PriorP99Ms: 100, SpanCount: 10},
		{Name: "no-prior-busy", P99Ms: 500, SpanCount: 9000},
		{Name: "worse-4x", P99Ms: 400, PriorP99Ms: 100, SpanCount: 5},
		{Name: "improved", P99Ms: 50, PriorP99Ms: 100, SpanCount: 99},
		{Name: "no-prior-quiet", P99Ms: 900, SpanCount: 3},
	}
	got := sortServicesByP99Delta(rows, 0)
	want := []string{"worse-4x", "worse-2x", "steady", "improved", "no-prior-busy", "no-prior-quiet"}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("sıra %d: %s, beklenen %s (tam sıra: %v)", i, got[i].Name, w, svcNames(got))
		}
	}
	// limit kesmesi sıralamadan SONRA — "top 2" en kötü ikiyi verir.
	got2 := sortServicesByP99Delta(append([]chstore.ServiceSummary{}, rows...), 2)
	if len(got2) != 2 || got2[0].Name != "worse-4x" || got2[1].Name != "worse-2x" {
		t.Errorf("limit=2 en kötü ikiyi vermeli, geldi: %v", svcNames(got2))
	}
}

func TestMergePriorServices(t *testing.T) {
	rows := []chstore.ServiceSummary{
		{Name: "a", P99Ms: 200},
		{Name: "b", P99Ms: 300},
	}
	prior := []chstore.ServiceSummary{
		{Name: "a", SpanCount: 7, ErrorRate: 1.5, AvgMs: 12, P99Ms: 100},
		{Name: "yok", P99Ms: 999},
	}
	mergePriorServices(rows, prior)
	if rows[0].PriorP99Ms != 100 || rows[0].PriorSpanCount != 7 ||
		rows[0].PriorErrorRate != 1.5 || rows[0].PriorAvgMs != 12 {
		t.Errorf("a satırı prior almadı: %+v", rows[0])
	}
	// Prior'da olmayan satır SIFIR kalır (omitempty → JSON'da düşer →
	// TrendDelta chip basmaz); uydurma bir taban yazılmaz.
	if rows[1].PriorP99Ms != 0 || rows[1].PriorSpanCount != 0 {
		t.Errorf("b satırı prior'suz kalmalıydı: %+v", rows[1])
	}
}

func svcNames(rows []chstore.ServiceSummary) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].Name
	}
	return out
}
