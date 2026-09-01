package chstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// jsonField — r'nin JSON tel biçiminde `name` alanının ham değeri; alan
// yoksa "" (omitempty'yi ayırt etmek için — 0 ile yok aynı şey değil).
func jsonField(t *testing.T, r TraceRow, name string) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return string(m[name])
}

// trace_error_spans_test.go — v0.10.218 (Dynatrace liste düzeni, D3 dilimi).
//
// TraceRow.ErrorSpans dört yerde yaşıyor: iki SELECT (ham spans yolu +
// trace_summary_5m stage-2) ve iki Scan (scanTraceListRows + runTraceStage2).
// Sözleşme "kolon SELECT'te has_error'dan hemen sonra, Scan'de hasErr'den
// hemen sonra". Bir yol güncellenip diğeri unutulursa Scan kolon sayısı
// kayar ve ClickHouse sürücüsü hatayı ÇALIŞMA ANINDA verir — go build /
// go vet sessiz. Bu test dört yeri de pinler (memory: düzeltmenin ikinci
// yarısı — sözleşme kaç yerde yaşıyorsa sayı iddiaya çevrilir).

func TestTraceErrorSpans_RawListSQL(t *testing.T) {
	from := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	wc := buildGetTracesWhere(TraceFilter{From: from, To: from.Add(time.Hour)}, clusterDeriveExpr)
	sql := buildGetTracesListSQL(wc.sql(), "", "trace_start", "DESC")

	i := strings.Index(sql, "AS has_error")
	j := strings.Index(sql, "countIf(status_code = 'error')          AS err_spans")
	if i < 0 || j < 0 {
		t.Fatalf("ham liste SELECT'i has_error + err_spans taşımalı; got:\n%s", sql)
	}
	if j < i {
		t.Fatalf("err_spans, has_error'dan SONRA gelmeli (Scan sırası); got:\n%s", sql)
	}
	// has_error KALIYOR: sort ve HAVING ona referans veriyor (repo.go
	// "status": "has_error"). err_spans'ı onun yerine koymak sıralamayı
	// sessizce bozardı.
	if strings.Count(sql, "AS has_error") != 1 {
		t.Fatalf("has_error tam bir kez; got:\n%s", sql)
	}
}

func TestTraceErrorSpans_MVStage2SQL(t *testing.T) {
	body := funcBody(t, "repo.go", "func (s *Store) getTracesFromMV(")
	i := strings.Index(body, "AS has_error,")
	j := strings.Index(body, "countMerge(error_count_state)                               AS err_spans,")
	k := strings.Index(body, "AS first_bucket")
	if i < 0 || j < 0 || k < 0 {
		t.Fatalf("stage-2 SELECT has_error → err_spans → first_bucket sırasını kaybetti")
	}
	if !(i < j && j < k) {
		t.Fatalf("stage-2 kolon sırası has_error(%d) < err_spans(%d) < first_bucket(%d) olmalı", i, j, k)
	}
}

func TestTraceErrorSpans_BothScannersReadIt(t *testing.T) {
	cases := []struct{ file, fn, want string }{
		{"trace_raw_probe.go", "func scanTraceListRows(", "&t.SpanCount, &hasErr, &t.ErrorSpans)"},
		{"trace_slice.go", "func (s *Store) runTraceStage2(", "&t.SpanCount, &hasErr, &t.ErrorSpans, &firstBucket)"},
	}
	for _, c := range cases {
		body := funcBody(t, c.file, c.fn)
		if !strings.Contains(body, c.want) {
			t.Errorf("%s %s: Scan hasErr'den hemen sonra &t.ErrorSpans okumalı (SELECT ile aynı sıra); istenen parça yok: %q", c.file, c.fn, c.want)
		}
	}
}

func TestTraceErrorSpans_OmittedWhenZero(t *testing.T) {
	// Tel sözleşmesi: 0 → alan yok. Eski önbellek yanıtları da alansız gelir;
	// UI iki durumu aynı okur (yalnız rozet).
	r := TraceRow{TraceID: "a", HasError: false}
	if got := jsonField(t, r, "errorSpans"); got != "" {
		t.Fatalf("0 iken errorSpans tel'de olmamalı; got %q", got)
	}
	r.HasError, r.ErrorSpans = true, 3
	if got := jsonField(t, r, "errorSpans"); got != "3" {
		t.Fatalf("errorSpans=3 tel'de 3 olmalı; got %q", got)
	}
}
