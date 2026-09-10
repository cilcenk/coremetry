package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.10.584 — CH log yolunda iki sessiz cevapsızlık (logstore audit'i,
// 2026-09-09):
//
//  1. logstore.Filter.TraceIDs (v0.5.271, DQL çapraz-sinyal join'i) CH
//     arka ucunda LogFilter'a HİÇ geçmiyordu — ES'te çalışan çoklu-trace
//     log araması CH'de boş dönüyor, hata vermiyordu.
//  2. ES trace/span id'yi ToLower yapıyor, CH kesin eşitlik bağlıyordu.
//     OTLP yazımı küçük harf hex (otlp/convert.go); büyük harfli bir id
//     CH'de 0 satır — sessiz.
//
// Prod ES'te olduğu için bugün ısırmıyor; CH-backend kurulumda ikisi de
// "hata değil, cevapsızlık" sınıfı. Bu test SAF logsWhere'i pinler; aynı
// gövdeyi Histogram da kullanır (LogTraceIDsConjunct), yani düzeltmenin
// iki yarısı ayrışamaz.
func TestLogsWhere_TraceIDsReachClickHouse(t *testing.T) {
	base := logsWhere(LogFilter{From: time.Unix(0, 1), To: time.Unix(0, 2)})
	wc := logsWhere(LogFilter{
		From: time.Unix(0, 1), To: time.Unix(0, 2),
		TraceIDs: []string{"ABCDEF0123456789ABCDEF0123456789", " 0123456789abcdef0123456789abcdef "},
	})
	sql := wc.sql()
	if !strings.Contains(sql, "trace_id IN (?,?)") {
		t.Fatalf("çoğul trace filtresi sorguya girmemiş (sessiz no-op sınıfı):\n%s", sql)
	}
	if len(wc.conds) != len(base.conds)+1 {
		t.Fatalf("TraceIDs tam olarak bir conjunct eklemeli: %d vs %d", len(wc.conds), len(base.conds))
	}
	// Bağlanan değerler NORMALİZE: küçük harf + kırpılmış (ES paritesi).
	got := wc.args[len(wc.args)-2:]
	if got[0] != "abcdef0123456789abcdef0123456789" || got[1] != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("id'ler normalize edilmemiş: %v", got)
	}
}

func TestLogsWhere_SingleIDsNormalized(t *testing.T) {
	wc := logsWhere(LogFilter{
		From: time.Unix(0, 1), To: time.Unix(0, 2),
		TraceID: " ABCDEF0123456789ABCDEF0123456789 ",
		SpanID:  "0123456789ABCDEF",
	})
	sql := wc.sql()
	if !strings.Contains(sql, "trace_id = ?") || !strings.Contains(sql, "span_id = ?") {
		t.Fatalf("tekil id yüklemleri kayıp:\n%s", sql)
	}
	var seenTrace, seenSpan bool
	for _, a := range wc.args {
		switch a {
		case "abcdef0123456789abcdef0123456789":
			seenTrace = true
		case "0123456789abcdef":
			seenSpan = true
		}
	}
	if !seenTrace || !seenSpan {
		t.Fatalf("büyük harfli id küçük harfe çevrilmedi (CH'de 0 satır sınıfı): %v", wc.args)
	}
}

// Tavan bir iddia: üst akış bugün en çok 50 gönderiyor (influx enrichMaxRows);
// 200 onun üstünde ama sınırsız değil — sınırsız IN listesi sorgu metnini
// ve bind sayısını girdiyle orantılı büyütür.
func TestLogsWhere_TraceIDsCapped(t *testing.T) {
	ids := make([]string, LogsTraceIDsCap+57)
	for i := range ids {
		ids[i] = strings.Repeat("a", 31) + string(rune('a'+i%26))
	}
	wc := logsWhere(LogFilter{From: time.Unix(0, 1), To: time.Unix(0, 2), TraceIDs: ids})
	want := "trace_id IN (" + strings.TrimRight(strings.Repeat("?,", LogsTraceIDsCap), ",") + ")"
	if !strings.Contains(wc.sql(), want) {
		t.Fatalf("tavan uygulanmamış: %d yer tutucu bekleniyordu\n%s", LogsTraceIDsCap, wc.sql())
	}
}

// Boş liste hiçbir şey eklemez — "boş IN ()" CH'de sözdizimi hatasıdır.
func TestLogsWhere_EmptyTraceIDsNoConjunct(t *testing.T) {
	base := logsWhere(LogFilter{From: time.Unix(0, 1), To: time.Unix(0, 2)})
	wc := logsWhere(LogFilter{From: time.Unix(0, 1), To: time.Unix(0, 2), TraceIDs: []string{}})
	if len(wc.conds) != len(base.conds) || strings.Contains(wc.sql(), "IN (") {
		t.Fatalf("boş TraceIDs conjunct eklememeli:\n%s", wc.sql())
	}
}
