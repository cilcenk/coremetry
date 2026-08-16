package api

import (
	"sort"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1111 — /services compare=prior + sort=p99Delta ("en çok
// kötüleşenler", Faz 5). Endpoints'in aday-havuzu deseninin (v0.9.761)
// servis-listesi eşi; saf çekirdekler tablo testli
// (services_delta_test.go).

// servicesDeltaPool — p99Delta sıralamasının aday havuzu: limit×5,
// [250, 2000] aralığına kıstırılır. Endpoints'in 10000'lik tavanından
// bilinçli küçük: /services limit'i zaten ≤500 ve filo evreni
// (1000'ler servis) endpoint evreninden (10000'ler yol) dar — 2000
// aday, "en çok trafik alanlar" evreninin dürüst bir üst sınırı.
func servicesDeltaPool(limit int) int {
	pool := limit * 5
	if pool < 250 {
		pool = 250
	}
	if pool > 2000 {
		pool = 2000
	}
	return pool
}

// sortServicesByP99Delta — göreli p99 kötüleşmesi (cur-prior)/prior
// büyükten küçüğe; prior'u olmayan satırlar (pencerede yeni ya da
// prior havuz dışı) tanımlı deltaların ARKASINA, kendi aralarında
// spanCount'a göre. sortEndpointsByP99Delta ile birebir semantik —
// iki "kötüleşenler" listesi farklı sıralama dili konuşmasın.
func sortServicesByP99Delta(rows []chstore.ServiceSummary, limit int) []chstore.ServiceSummary {
	deltaOf := func(r chstore.ServiceSummary) (float64, bool) {
		if r.PriorP99Ms <= 0 {
			return 0, false
		}
		return (r.P99Ms - r.PriorP99Ms) / r.PriorP99Ms, true
	}
	sort.SliceStable(rows, func(i, j int) bool {
		di, oi := deltaOf(rows[i])
		dj, oj := deltaOf(rows[j])
		if oi != oj {
			return oi // tanımlı delta önce
		}
		if !oi {
			return rows[i].SpanCount > rows[j].SpanCount
		}
		if di != dj {
			return di > dj
		}
		return rows[i].Name < rows[j].Name
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// mergePriorServices — prior penceresinin satırlarını ada göre cari
// satırlara işler. Prior'da bulunmayan satırın alanları sıfır kalır
// (JSON'da omitempty ile düşer) — TrendDelta bunu "chip yok" okur,
// uydurma bir %0 tabanı okumaz.
func mergePriorServices(rows, prior []chstore.ServiceSummary) {
	idx := make(map[string]*chstore.ServiceSummary, len(prior))
	for i := range prior {
		idx[prior[i].Name] = &prior[i]
	}
	for i := range rows {
		if p, ok := idx[rows[i].Name]; ok {
			rows[i].PriorSpanCount = p.SpanCount
			rows[i].PriorErrorRate = p.ErrorRate
			rows[i].PriorAvgMs = p.AvgMs
			rows[i].PriorP99Ms = p.P99Ms
		}
	}
}
