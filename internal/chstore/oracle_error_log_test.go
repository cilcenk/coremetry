package chstore

// oracle_error_log_test.go — v0.10.599 (Oracle Aşama 2) sözleşme pinleri.
//
// CH'siz: DDL metni, INSERT kolon listesi ve okuma SQL'i saf. Kanıtlanan
// sözleşmeler:
//   - state tablosu şekli: ReplacingMergeTree(version), ORDER BY = dedup
//     anahtarı (source_id, time, row_id), PARTITION kolonu ORDER BY'da (Kural
//     P1), Nullable yok, trace_id bloom index'i, TTL var
//   - INSERT kolon listesi DDL'deki 22 veri kolonuyla birebir + sıra
//   - okuma: FINAL + trace_id eşitliği + zaman sınırı + LIMIT kelepçesi +
//     max_execution_time
//   - DDL `tables` diliminde (store.go kaynak taraması — dilim fonksiyon-yerel)

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOracleErrorLogDDLShape(t *testing.T) {
	ddl := oracleErrorLogDDL
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS oracle_error_log",
		"ENGINE = ReplacingMergeTree(version)",
		"ORDER BY (source_id, time, row_id)",
		"PARTITION BY toYYYYMM(time)",
		"INDEX idx_oracle_trace trace_id TYPE bloom_filter",
		"TTL toDate(time) + INTERVAL 30 DAY",
		"version        UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))",
		"attr_keys      Array(String)",
		"attr_values    Array(String)",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL %q içermeli", want)
		}
	}
	if strings.Contains(ddl, "Nullable") {
		t.Error("Nullable yok — sentinel default (/clickhouse-schema C4)")
	}
	// Kural P1: partition ifadesinin kolonu ORDER BY'da.
	if !regexp.MustCompile(`ORDER BY \([^)]*\btime\b[^)]*\)`).MatchString(ddl) {
		t.Error("partition kolonu `time` ORDER BY'da olmalı (Kural P1)")
	}
}

func TestOracleErrorLogInsertColumnsMatchDDL(t *testing.T) {
	// DDL'den veri kolonlarını çıkar: `version` (DEFAULT) ve INDEX satırı hariç.
	var ddlCols []string
	for _, line := range strings.Split(oracleErrorLogDDL, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "CREATE" || f[0] == "INDEX" || f[0] == "version" ||
			f[0] == ")" || strings.HasPrefix(f[0], "ENGINE") || f[0] == "PARTITION" || f[0] == "ORDER" || f[0] == "TTL" {
			continue
		}
		ddlCols = append(ddlCols, f[0])
	}
	var insCols []string
	for _, c := range strings.Split(oracleErrorLogColumns, ",") {
		insCols = append(insCols, strings.TrimSpace(c))
	}
	if len(ddlCols) != len(insCols) || len(insCols) != oracleErrorLogColumnCount() {
		t.Fatalf("kolon sayısı: DDL %d, INSERT %d", len(ddlCols), len(insCols))
	}
	for i := range ddlCols {
		if ddlCols[i] != insCols[i] {
			t.Errorf("kolon %d: DDL %q, INSERT %q — sıra birebir olmalı", i, ddlCols[i], insCols[i])
		}
	}
	if len(insCols) != 22 {
		t.Errorf("22 veri kolonu bekleniyor, %d", len(insCols))
	}
}

func TestOracleErrorsByTraceSQLContract(t *testing.T) {
	q := oracleErrorsByTraceSQL(0)
	for _, want := range []string{
		"FROM oracle_error_log FINAL",
		"trace_id = ?",
		"time >= ? AND time < ?",
		"ORDER BY time",
		"LIMIT 200",
		"SETTINGS max_execution_time = 5",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("okuma SQL %q içermeli:\n%s", want, q)
		}
	}
	// SELECT listesi INSERT listesiyle aynı sırada — Scan hedefleri ona göre.
	sel := q[strings.Index(q, "SELECT ")+7 : strings.Index(q, "FROM")]
	var selCols []string
	for _, c := range strings.Split(sel, ",") {
		selCols = append(selCols, strings.TrimSpace(c))
	}
	var insCols []string
	for _, c := range strings.Split(oracleErrorLogColumns, ",") {
		insCols = append(insCols, strings.TrimSpace(c))
	}
	if strings.Join(selCols, ",") != strings.Join(insCols, ",") {
		t.Errorf("SELECT listesi INSERT listesiyle aynı olmalı:\n%v\n%v", selCols, insCols)
	}
	if strings.Contains(q, "coremetry.") {
		t.Error("telemetri tablosu niteliksiz anılır")
	}
}

func TestClampOracleErrorsLimit(t *testing.T) {
	cases := map[int]int{-5: 200, 0: 200, 1: 1, 500: 500, 1000: 1000, 5000: 1000}
	for in, want := range cases {
		if got := clampOracleErrorsLimit(in); got != want {
			t.Errorf("clamp(%d) = %d, want %d", in, got, want)
		}
	}
	if !strings.Contains(oracleErrorsByTraceSQL(99999), "LIMIT 1000") {
		t.Error("tavan üstü limit 1000'e kelepçelenmeli")
	}
}

// TestOracleErrorLogDDLRegistered — `tables` dilimi migrate() içinde yerel;
// kaynak taraması (partition_dedup_test.go emsali) DDL sabitinin dilimde
// anıldığını kanıtlar. Anılmazsa tablo hiç oluşmaz ve poller'ın ilk INSERT'i
// düşer — go build bunu göremez.
func TestOracleErrorLogDDLRegistered(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "tables := []string{")
	if i < 0 {
		t.Fatal("store.go'da `tables := []string{` bulunamadı")
	}
	rest := s[i:]
	j := strings.Index(rest, "\n\t}")
	if j < 0 {
		t.Fatal("tables dilimi kapanışı bulunamadı")
	}
	if !strings.Contains(rest[:j], "oracleErrorLogDDL,") {
		t.Error("oracleErrorLogDDL `tables` diliminde anılmalı (CREATE TABLE tables dilimine — /clickhouse-schema §8)")
	}
}
