package chstore

// trace_raw_light_v244_test.go — v0.10.245 uzun-aralıklı attribute araması.
// Sözleşme (trace_raw_light.go başlığı): akışkan 1. aşama GROUP BY'sız +
// pencereyle ölçeklenen tavan; tekilleştirme sırayı korur ve k ile keser;
// kök kontrolü demet IN (kova −2..+1, 5 dk tabanı); 2. aşama trace-başı
// aralık (t0'a göre sıralı, ±10 dk dolgu); probe alt sorgusu basamak
// penceresiyle bağlanır.

import (
	"strings"
	"testing"
	"time"
)

func TestTraceRawStage1MaxExecScalesWithWindow(t *testing.T) {
	to := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		win  time.Duration
		want int
	}{
		{time.Hour, 25}, {24 * time.Hour, 25}, {25 * time.Hour, 60},
		{7 * 24 * time.Hour, 60}, {8 * 24 * time.Hour, 120}, {30 * 24 * time.Hour, 120},
	}
	for _, c := range cases {
		if got := traceRawStage1MaxExec(to.Add(-c.win), to); got != c.want {
			t.Errorf("%s: %d, istenen %d", c.win, got, c.want)
		}
	}
}

func TestTraceRawStage1StreamingEligibility(t *testing.T) {
	if !traceRawStage1Streaming("duration", "") || !traceRawStage1Streaming("time", "") || !traceRawStage1Streaming("", "") {
		t.Error("süre/zaman + HAVING'siz → akışkan")
	}
	if traceRawStage1Streaming("duration", " HAVING countIf(service_name = ?) > 0") {
		t.Error("HAVING varken akışkan OLMAMALI (GROUP BY gerekir)")
	}
	if traceRawStage1Streaming("spans", "") || traceRawStage1Streaming("status", "") {
		t.Error("spans/status sıralaması GROUP BY ister")
	}
}

func TestTraceRawStage1StreamSQLShape(t *testing.T) {
	q := traceRawStage1StreamSQL(" WHERE time >= ? AND service_name = ?", "duration", "DESC", 60)
	for _, want := range []string{
		"SELECT trace_id, toUnixTimestamp64Nano(time) AS t",
		"FROM spans  WHERE time >= ? AND service_name = ?",
		"ORDER BY duration DESC, trace_id",
		"LIMIT ?",
		"max_execution_time = 60",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("sorguda %q yok:\n%s", want, q)
		}
	}
	if strings.Contains(q, "GROUP BY") {
		t.Error("akışkan 1. aşama GROUP BY taşımamalı (bellek düz)")
	}
	if !strings.Contains(traceRawStage1StreamSQL("", "time", "ASC", 25), "ORDER BY time ASC") {
		t.Error("zaman sıralaması time kolonu")
	}
	if got := traceRawStage1OverFetch(200); got != 800 {
		t.Errorf("over-fetch 200 → %d, istenen 800", got)
	}
	if got := traceRawStage1OverFetch(10000); got != 4*traceStage2MaxIDs {
		t.Errorf("over-fetch tavanı %d", got)
	}
}

func TestTraceRawStage1GroupSQLShape(t *testing.T) {
	q, ok := traceRawStage1GroupSQL(" WHERE x", " HAVING h", "spans", "DESC", 120)
	if !ok {
		t.Fatal("spans sıralaması desteklenmeli")
	}
	for _, want := range []string{
		"toUnixTimestamp64Nano(min(time)) AS t0, toUnixTimestamp64Nano(max(time)) AS t1",
		"GROUP BY trace_id HAVING h",
		"ORDER BY count() DESC, trace_id",
		"LIMIT ? OFFSET ?",
		"max_execution_time = 120",
		"max_bytes_before_external_group_by",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("sorguda %q yok:\n%s", want, q)
		}
	}
	if _, ok := traceRawStage1GroupSQL("", "", "service", "DESC", 25); ok {
		t.Error("service sıralaması hafif yola uygun değil")
	}
}

func TestDedupeStage1(t *testing.T) {
	rows := []stage1Cand{{"a", 3, 3}, {"b", 2, 2}, {"a", 1, 1}, {"c", 9, 9}, {"b", 0, 0}}
	got := dedupeStage1(rows, 2)
	if len(got) != 2 || got[0].id != "a" || got[0].t0 != 3 || got[1].id != "b" {
		t.Errorf("tekilleştirme sırayı/ilk görüleni korumalı: %+v", got)
	}
	if got := dedupeStage1(rows, 10); len(got) != 3 {
		t.Errorf("k büyükken tüm tekiller: %d", len(got))
	}
}

func TestRootCheckArgsAndSQL(t *testing.T) {
	// t0 = 12:07:30 → kova tabanı 12:05:00; −2..+1 → 11:55, 12:00, 12:05, 12:10.
	t0 := time.Date(2026, 9, 2, 12, 7, 30, 0, time.UTC)
	t0b := t0.Add(5 * time.Minute) // 12:12:30 → 12:00, 12:05, 12:10, 12:15 (3 ortak kova)
	buckets, ids := rootCheckArgs([]stage1Cand{{"abc", t0.UnixNano(), t0.UnixNano()}, {"def", t0b.UnixNano(), t0b.UnixNano()}})
	want := []int64{
		time.Date(2026, 9, 2, 11, 55, 0, 0, time.UTC).Unix(),
		time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 9, 2, 12, 5, 0, 0, time.UTC).Unix(),
		time.Date(2026, 9, 2, 12, 10, 0, 0, time.UTC).Unix(),
		time.Date(2026, 9, 2, 12, 15, 0, 0, time.UTC).Unix(),
	}
	if len(buckets) != len(want) {
		t.Fatalf("kova sayısı %d (tekil+sıralı beklenir), istenen %d: %v", len(buckets), len(want), buckets)
	}
	for i, w := range want {
		if buckets[i] != w {
			t.Errorf("kova %d: %v, istenen %d", i, buckets[i], w)
		}
	}
	if len(ids) != 2 || ids[0] != "abc" || ids[1] != "def" {
		t.Errorf("id'ler: %v", ids)
	}
	q := rootCheckSQL(len(buckets), len(ids))
	if strings.Count(q, "toDateTime(?, 'UTC')") != 5 || !strings.Contains(q, "AND trace_id IN (?,?)") ||
		!strings.Contains(q, "time_bucket IN (") || !strings.Contains(q, "argMaxIfMerge(root_service_state) != ''") ||
		!strings.Contains(q, "max_execution_time = 10") {
		t.Errorf("iki-IN sorgusu şekli:\n%s", q)
	}
	if strings.Contains(q, "time_bucket >=") || strings.Contains(q, "(time_bucket, trace_id) IN") {
		t.Error("pencere taraması / demet IN olmamalı (ölçüldü: 7.7 s / 3.3 s vs 0.8 s)")
	}
}

func TestStage2PrewhereShape(t *testing.T) {
	q := buildGetTracesListSQLWith(stage2PrewhereSQL(3)+" WHERE time >= ? AND service_name = ?", "", "dur_ms", "DESC", stage2Settings)
	pre := strings.Index(q, "PREWHERE trace_id IN (?,?,?)")
	wh := strings.Index(q, " WHERE time >= ? AND service_name = ?")
	if pre < 0 || wh < 0 || pre > wh {
		t.Fatalf("PREWHERE id listesi WHERE'den önce gelmeli:\n%s", q)
	}
	if !strings.Contains(q, "optimize_move_to_prewhere = 0,") || !strings.Contains(q, "max_execution_time = 25") {
		t.Errorf("SETTINGS: %s", q[strings.Index(q, "SETTINGS"):])
	}
	if strings.Contains(q, "fromUnixTimestamp64Nano") {
		t.Error("2. aşamada zaman aralığı birleşimi olmamalı (ölçüldü: her varyantta daha yavaş)")
	}
	if strings.Contains(buildGetTracesListSQL(" WHERE x", "", "dur_ms", "DESC"), "optimize_move_to_prewhere") {
		t.Error("varsayılan liste sorgusu ek ayar taşımamalı")
	}
}

func TestProbeFetchLimit(t *testing.T) {
	if probeFetchLimit(51, false) != 51 || probeFetchLimit(51, true) != 102 {
		t.Error("kök son-filtreli probe K×2 çekmeli")
	}
}

func TestProbeHavingArgsBindsRungWindow(t *testing.T) {
	from := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	have := []any{int64(1), int64(2), "svc"}
	got := probeHavingArgs(have, true, from)
	if got[0] != from.Unix() || got[1] != int64(2) || got[2] != "svc" {
		t.Errorf("kök alt sorgusu basamak penceresiyle bağlanmalı: %v", got)
	}
	if have[0] != int64(1) {
		t.Error("girdi dilimi değişmemeli")
	}
	if got := probeHavingArgs(have, false, from); got[0] != int64(1) {
		t.Error("alt sorgu yokken argümanlar aynen")
	}
}
