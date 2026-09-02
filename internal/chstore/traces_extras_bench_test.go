package chstore

// traces_extras_bench_test.go — v0.10.233 (docs/audit/traces-attribute-columns.md D4).
//
// OPT-IN canlı regresyon ölçümü: COREMETRY_BENCH_CH_ADDR yoksa t.Skip.
// 7 günlük pencerede Traces liste sorgusu (kolonsuz) ile extras sorgusu
// (varsayılan dört attribute kolonu) — ÜRETİMDEKİ SQL'in kendisi
// (buildGetTracesListSQL / traceExtrasSQL), sadeleştirilmiş kopyası değil —
// query_log MEDYANI (≥3 koşu, query_id ile bağlanır) + read_bytes.
// Extras iki hâlde ölçülür: dizi yolu (terfi haritası boş) ve terfi yolu
// (CH'de var olan attr_* kolonları kaydedilmiş) — oran = kolonların
// kazancı. Kabul eşiği (terfi yolunda): read_bytes ≤ 3 × liste, p50 ≤ 500 ms.
// Sonuç t.Logf + testdata/bench-traces-extras.md'ye satır (git'e girer:
// zaman serisi). Tek ad-hoc zamanlama YALAN söyler (feedback-perf-benchmark-
// discipline) — bu yüzden medyan ve query_log.
//
// Güvenlik: DDL YOK, yalnız SELECT; store kurulmaz (New boot DDL'i
// koşabilir — küme CH'sine lokal binary bağlama dersi, 2026-08-28).
//
//   COREMETRY_BENCH_CH_ADDR=localhost:9000 COREMETRY_BENCH_CH_DB=coremetry \
//   COREMETRY_BENCH_CH_USER=default COREMETRY_BENCH_CH_PASS=… \
//   [COREMETRY_BENCH_CH_CLUSTER=coremetry] go test ./internal/chstore/ -run BenchTracesExtras -v

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	benchRuns            = 3
	benchIDs             = 50
	benchExtrasMaxP50Ms  = 500.0
	benchExtrasMaxBytesX = 3.0
)

var benchDefaultAttrs = []string{"channel_code", "function_code", "openshift.cluster.name", "function_id"}

type benchStat struct {
	P50Ms     float64
	MaxMs     float64
	ReadRows  uint64
	ReadBytes uint64
	Runs      int
}

func TestBenchTracesExtras7d(t *testing.T) {
	addr := os.Getenv("COREMETRY_BENCH_CH_ADDR")
	if addr == "" {
		t.Skip("COREMETRY_BENCH_CH_ADDR yok — opt-in canlı benchmark (audit D4)")
	}
	envOr := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: strings.Split(addr, ","),
		Auth: clickhouse.Auth{Database: envOr("COREMETRY_BENCH_CH_DB", "coremetry"),
			Username: envOr("COREMETRY_BENCH_CH_USER", "default"), Password: os.Getenv("COREMETRY_BENCH_CH_PASS")},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	// Pencere varsayılan 7 gün (audit D4); COREMETRY_BENCH_DAYS ile daraltılır
	// (CPU-kısıtlı lokal küme 7 günde max_execution_time'a takılır — o da
	// bir bulgudur, satıra "timeout" yazılır).
	days := 7
	if d, err := strconv.Atoi(os.Getenv("COREMETRY_BENCH_DAYS")); err == nil && d > 0 {
		days = d
	}
	to := time.Now().UTC().Truncate(time.Minute)
	from := to.Add(-time.Duration(days) * 24 * time.Hour)

	// ── 1. Liste (kolonsuz) — üretim SQL'i ─────────────────────────────
	f := TraceFilter{From: from, To: to, Limit: benchIDs}
	wc := buildGetTracesWhere(f, clusterDeriveExpr)
	listSQL := buildGetTracesListSQL(wc.sql(), "", "trace_start", "DESC")
	listArgs := append(append([]any{}, wc.args...), benchIDs, 0)
	var ids []string
	listIDs := make([]string, 0, benchRuns)
	for i := 0; i < benchRuns; i++ {
		qid := fmt.Sprintf("bench-traces-list-%d-%d", time.Now().UnixNano(), i)
		rows, err := conn.Query(clickhouse.Context(ctx, clickhouse.WithQueryID(qid)), listSQL, listArgs...)
		if err != nil {
			benchAppend(t, fmt.Sprintf("| %s | %s | %dd | — | liste HATA: %s |", time.Now().UTC().Format("2006-01-02 15:04"), benchHost(addr), days, benchErr(err)))
			t.Fatalf("liste (%d gün): %v", days, err)
		}
		trs, err := scanTraceListRows(rows)
		rows.Close()
		if err != nil {
			benchAppend(t, fmt.Sprintf("| %s | %s | %dd | — | liste HATA: %s |", time.Now().UTC().Format("2006-01-02 15:04"), benchHost(addr), days, benchErr(err)))
			t.Fatalf("liste scan (%d gün): %v", days, err)
		}
		if i == 0 {
			for _, tr := range trs {
				ids = append(ids, tr.TraceID)
			}
		}
		listIDs = append(listIDs, qid)
	}
	if len(ids) == 0 {
		t.Skip("7 günlük pencerede trace yok — ölçülecek bir şey yok")
	}

	// ── 2. Extras — dizi yolu vs terfi yolu ─────────────────────────────
	present := benchPromotedColumns(t, ctx, conn)
	presentCols := map[string]bool{}
	for _, c := range present {
		presentCols[c] = true
	}
	runExtras := func(label string) []string {
		sql, projArgs := traceExtrasSQL(len(ids), benchDefaultAttrs)
		args := append([]any{}, projArgs...)
		args = append(args, from, to.Add(5*time.Minute))
		for _, id := range ids {
			args = append(args, id)
		}
		qids := make([]string, 0, benchRuns)
		for i := 0; i < benchRuns; i++ {
			qid := fmt.Sprintf("bench-traces-extras-%s-%d-%d", label, time.Now().UnixNano(), i)
			rows, err := conn.Query(clickhouse.Context(ctx, clickhouse.WithQueryID(qid)), sql, args...)
			if err != nil {
				t.Fatalf("extras(%s): %v", label, err)
			}
			for rows.Next() {
			}
			rows.Close()
			qids = append(qids, qid)
		}
		return qids
	}
	registerTraceAttrMaterialized(map[string]string{})
	t.Cleanup(func() { registerTraceAttrMaterialized(map[string]string{}) })
	arrayIDs := runExtras("array")
	registerTraceAttrMaterialized(present)
	promotedIDs := runExtras("promoted")

	// ── 3. query_log ─────────────────────────────────────────────────────
	_ = conn.Exec(ctx, "SYSTEM FLUSH LOGS") // yetki yoksa sessiz: satırlar zaten birkaç sn içinde düşer
	time.Sleep(2 * time.Second)
	cluster := os.Getenv("COREMETRY_BENCH_CH_CLUSTER")
	list := benchQueryLog(t, ctx, conn, cluster, listIDs)
	arr := benchQueryLog(t, ctx, conn, cluster, arrayIDs)
	prom := benchQueryLog(t, ctx, conn, cluster, promotedIDs)

	line := fmt.Sprintf("| %s | %s | %dd | %d/%d | %.0f | %s | %.0f | %s | %.0f | %s | %.1f× |",
		time.Now().UTC().Format("2006-01-02 15:04"), benchHost(addr), days, len(ids), len(presentCols),
		list.P50Ms, benchBytes(list.ReadBytes), arr.P50Ms, benchBytes(arr.ReadBytes), prom.P50Ms, benchBytes(prom.ReadBytes),
		benchRatio(arr.ReadBytes, prom.ReadBytes))
	t.Logf("liste p50 %.0f ms / %s · extras dizi p50 %.0f ms / %s · extras terfi p50 %.0f ms / %s (terfi kolonları: %d)",
		list.P50Ms, benchBytes(list.ReadBytes), arr.P50Ms, benchBytes(arr.ReadBytes), prom.P50Ms, benchBytes(prom.ReadBytes), len(presentCols))
	benchAppend(t, line)

	if list.Runs == 0 || prom.Runs == 0 {
		t.Skip("query_log satırı bulunamadı (log_queries kapalı?) — eşik uygulanamaz, yalnız koşuldu")
	}
	if prom.P50Ms > benchExtrasMaxP50Ms {
		t.Errorf("extras (terfi yolu) p50 %.0f ms > %.0f ms eşiği", prom.P50Ms, benchExtrasMaxP50Ms)
	}
	if list.ReadBytes > 0 && float64(prom.ReadBytes) > benchExtrasMaxBytesX*float64(list.ReadBytes) {
		t.Errorf("extras (terfi yolu) read_bytes %s > %.0f × liste (%s)", benchBytes(prom.ReadBytes), benchExtrasMaxBytesX, benchBytes(list.ReadBytes))
	}
}

// benchPromotedColumns — CH'de gerçekten VAR olan terfi kolonlarını
// promotedAttrs'ın iki yazımıyla haritalar (probe'un yaptığı gibi; ama
// doluluk değil varlık — ölçüm "kolon okununca ne olur" sorusuna cevap).
func benchPromotedColumns(t *testing.T, ctx context.Context, conn driver.Conn) map[string]string {
	rows, err := conn.Query(ctx, `SELECT name FROM system.columns WHERE database = currentDatabase() AND table = 'spans' AND name LIKE 'attr_%'`)
	if err != nil {
		t.Fatalf("system.columns: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		have[n] = true
	}
	out := map[string]string{}
	for _, a := range promotedAttrs {
		if !have[a.col] {
			continue
		}
		for _, k := range a.keys {
			for _, mk := range promotedMapKeys(a, k) {
				out[mk] = a.col
			}
		}
	}
	return out
}

func benchQueryLog(t *testing.T, ctx context.Context, conn driver.Conn, cluster string, qids []string) benchStat {
	table := "system.query_log"
	if cluster != "" {
		table = fmt.Sprintf("clusterAllReplicas('%s', system.query_log)", cluster)
	}
	holders := strings.TrimSuffix(strings.Repeat("?,", len(qids)), ",")
	args := make([]any, 0, len(qids))
	for _, q := range qids {
		args = append(args, q)
	}
	rows, err := conn.Query(ctx, `SELECT query_duration_ms, read_rows, read_bytes FROM `+table+
		` WHERE type = 'QueryFinish' AND is_initial_query AND query_id IN (`+holders+`)`, args...)
	if err != nil {
		t.Logf("query_log okunamadı: %v", err)
		return benchStat{}
	}
	defer rows.Close()
	var st benchStat
	var durs []float64
	for rows.Next() {
		var d uint64
		var rr, rb uint64
		if err := rows.Scan(&d, &rr, &rb); err != nil {
			t.Fatal(err)
		}
		durs = append(durs, float64(d))
		if rr > st.ReadRows {
			st.ReadRows = rr
		}
		if rb > st.ReadBytes {
			st.ReadBytes = rb
		}
	}
	if len(durs) == 0 {
		return st
	}
	sort.Float64s(durs)
	st.Runs = len(durs)
	st.P50Ms = durs[len(durs)/2]
	st.MaxMs = durs[len(durs)-1]
	return st
}

func benchBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

func benchRatio(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// benchHost — adres host kısmı, port ve kimlik bilgisi olmadan (dosya git'e girer).
func benchHost(addr string) string {
	h := strings.Split(addr, ",")[0]
	if i := strings.LastIndex(h, ":"); i > 0 {
		h = h[:i]
	}
	return h
}

func benchAppend(t *testing.T, line string) {
	p := filepath.Join("testdata", "bench-traces-extras.md")
	if _, err := os.Stat(p); os.IsNotExist(err) {
		_ = os.MkdirAll("testdata", 0o755)
		header := "# Traces extras benchmark (audit D4, v0.10.233)\n\n" +
			"Opt-in canlı ölçüm; her satır bir koşu (query_log medyanı, 3 koşu, 7 gün, 50 trace).\n\n" +
			"| zaman (UTC) | host | pencere | trace/terfi kolon | liste p50 ms | liste bytes | extras dizi p50 | dizi bytes | extras terfi p50 | terfi bytes | dizi/terfi |\n" +
			"|---|---|---|---|---|---|---|---|---|---|---|\n"
		if err := os.WriteFile(p, []byte(header), 0o644); err != nil {
			t.Logf("testdata yazılamadı: %v", err)
			return
		}
	}
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Logf("testdata açılamadı: %v", err)
		return
	}
	defer fh.Close()
	_, _ = fh.WriteString(line + "\n")
}

func benchErr(err error) string {
	m := err.Error()
	if len(m) > 120 {
		m = m[:120] + "…"
	}
	return strings.ReplaceAll(m, "|", "/")
}
