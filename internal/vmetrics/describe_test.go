package vmetrics

// v0.9.1180 — describeMetricName tablo testi.
//
// Bu bir BİRİM ŞABLONU ve repo kuralı net: değer+birim taşıyan her şablon
// HER dalı ship anında denenir ([[feedback-unit-mixing-needs-both-branches]]).
// Buradaki dallar üç eksende çarpışıyor — birim eki (seconds/milliseconds/
// bytes/yok), parça eki (bucket/sum/count/total/yok) ve ikisinin BİRLİKTE
// dizilişi — ve prod'daki gerçek ad tam da en dolu kombinasyonda:
// `http_server_request_duration_seconds_bucket`.
//
// Ayrıca burada asıl korunan şey bir SUSMA: `_count` ve `_sum` için tip
// tahmini YAPILMAMALI. names.go'nun anlattığı belirsizlik (queue_message_count
// gerçek bir sayaç da olabilir) burada yanlış varsayılan agregasyona dönüşür.

import "testing"

func TestDescribeMetricName(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantUnit string
		wantType string
	}{
		// ── Prod'un canlı ailesi (operatör ekranı, 2026-08-17).
		{"histogram bucket", "http_server_request_duration_seconds_bucket", "s", "histogram"},
		{"histogram sum", "http_server_request_duration_seconds_sum", "s", ""},
		{"histogram count", "http_server_request_duration_seconds_count", "s", ""},
		{"histogram base", "http_server_request_duration_seconds", "s", ""},

		// ── Birim ekleri tek tek.
		{"milliseconds", "job_step_milliseconds", "ms", ""},
		{"bytes", "process_resident_memory_bytes", "B", ""},
		{"bytes + total", "http_response_body_bytes_total", "B", "sum"},
		{"seconds + total", "process_cpu_time_seconds_total", "s", "sum"},

		// ── Birimsizler: boş dize "bilmiyorum" demek, uydurma değil.
		{"birimsiz sayaç", "http_requests_total", "", "sum"},
		{"birimsiz gauge", "queue_depth", "", ""},
		{"ratio bilerek birimsiz", "cpu_usage_ratio", "", ""},
		{"percent bilerek birimsiz", "memory_usage_percent", "", ""},

		// ── SUSMA testleri: _count/_sum tip tahmin ETMEZ.
		{"gerçek sayaç _count ile biter", "queue_message_count", "", ""},
		{"gerçek toplam _sum ile biter", "batch_bytes_sum", "B", ""},

		// ── Noktalı OTel adı (CH yolundan gelirse): ek yok, tahmin yok.
		{"noktalı ad", "http.server.request.duration", "", ""},

		// ── Dejenere girdiler.
		{"boş", "", "", ""},
		{"yalnız boşluk", "   ", "", ""},
		{"yalnız ek", "_seconds", "s", ""},
		{"yalnız parça eki", "_bucket", "", "histogram"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unit, typ := describeMetricName(c.in)
			if unit != c.wantUnit {
				t.Errorf("describeMetricName(%q) birim = %q, beklenen %q",
					c.in, unit, c.wantUnit)
			}
			if typ != c.wantType {
				t.Errorf("describeMetricName(%q) tip = %q, beklenen %q",
					c.in, typ, c.wantType)
			}
		})
	}
}

// TestDescribeMetricUnitsAreFormatterKnown — üretilen her birim dizesi
// frontend/src/lib/chartFmt.ts'in fmtSmart'ının TANIDIĞI bir dize olmalı.
// Tanımadığı bir dize (ör. "1" ya da "By") panelde "0.25 By" gibi okunur ve
// birimi taşımamaktan daha kötüdür — bu yüzden liste ile formatlayıcının
// sözlüğü arasındaki bağ teste bağlanıyor.
func TestDescribeMetricUnitsAreFormatterKnown(t *testing.T) {
	// chartFmt.ts fmtSmart'ın özel-kabuk birimleri (throughput ailesi hariç,
	// onu ad son ekinden üretmiyoruz).
	known := map[string]bool{"ms": true, "s": true, "%": true, "B": true, "bytes": true}
	for _, u := range displayUnitBySuffix {
		if !known[u.unit] {
			t.Errorf("%q eki %q birimini üretiyor ama fmtSmart bunu tanımıyor — "+
				"panel \"0.25 %s\" gibi okur; chartFmt.ts'e ekle ya da bu eşlemeyi "+
				"kaldır", u.suffix, u.unit, u.unit)
		}
	}
}
