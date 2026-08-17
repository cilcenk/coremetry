package insight

// charts.go — kartın opsiyonel chart spec'leri. Saf.
//
// "Doğrulanmış" olması sözleşmenin yarısı: spec MODEL'den gelmiyor
// (render_chart yolunda gelir), deterministik kanıttan türüyor — ama
// aynı doğrulayıcıdan (NewChartSpec) geçiyor ki FE tek bir kabul kümesi
// bilsin. Geçersiz spec ATILIR; boş bir panel çizdirmek, chart
// olmamasından kötü.

// windowSeconds — [fromNs,toNs] → saniye. Birim disiplini: chart
// penceresi SANİYE (render_chart'ın range_s bandı), olay damgaları
// NANOSANİYE. ≤0 dönerse NewChartSpec varsayılana (30dk) düşer.
func windowSeconds(fromNs, toNs int64) int64 {
	if fromNs <= 0 || toNs <= fromNs {
		return 0
	}
	return (toNs - fromNs) / 1e9
}

// ExceptionCharts — grubun servisinin hata oranı, olay penceresinde.
// Tek kart: exception'ın sorusu "hata ne zaman patladı", ve error_rate
// bunu bir bakışta gösteriyor.
func ExceptionCharts(ev ExceptionEvidence) []ChartSpec {
	from, to := ExceptionWindow(ev.FirstSeenNs, ev.LastSeenNs)
	spec, ok := NewChartSpec("", ev.Service, "", "error_rate", windowSeconds(from, to))
	if !ok {
		return nil
	}
	return []ChartSpec{spec}
}

// ProblemCharts — ihlal edilen metriğin AİLESİNE uygun seri. Aile
// eşlemesi MetricFamily ile tek yerden: kart linki "en yavaş trace"
// derken chart error_rate çizmesin.
func ProblemCharts(ev ProblemEvidence) []ChartSpec {
	agg := "rate"
	switch MetricFamily(ev.Metric) {
	case "error":
		agg = "error_rate"
	case "latency":
		agg = "p95"
	}
	spec, ok := NewChartSpec("", ev.Service, "", agg, windowSeconds(ev.FromNs, ev.ToNs))
	if !ok {
		return nil
	}
	return []ChartSpec{spec}
}
