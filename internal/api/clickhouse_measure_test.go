package api

// clickhouse_measure_test.go — v0.10.683 (kuyruk 1: ClickHouse mimari
// denetimi 646, öneri 1/2/8 doğrulama sorguları /admin/clickhouse'a).
//
// SÖZLEŞME (saf SQL üreteçleri, canlı CH yok):
//   1. Küme adı varsa HER sistem tablosu clusterAllReplicas ile okunur
//      (system.parts / events / asynchronous_inserts / query_log node-yerel;
//      tek node okumak kümenin geri kalanını "sıfır" gösterirdi — v0.9.540
//      merges dersi). Küme yoksa düz tablo, clusterAllReplicas geçmez.
//   2. Her sorgu LIMIT + max_execution_time taşır (CLAUDE.md CH sınırı).
//   3. Parça sorgusu yalnız aktif parçalar + currentDatabase(), partition
//      düzeyinde gruplar (partition başına parça = parts_to_delay_insert
//      sinyali).
//   4. Olay sorgusu dört sayacı adıyla taşır; insert-boyutu sorgusu
//      query_kind='Insert' + spans tablosuna bağlıdır.
//   5. Rota kendi dosyasında, defterde, admin kapılı; api.go kaydetmez.

import (
	"os"
	"strings"
	"testing"
)

func TestCHMeasureQueriesClusterAware(t *testing.T) {
	cases := []struct {
		name string
		fn   func(string) string
		tbl  string
	}{
		{"parts", chMeasurePartsQuery, "system.parts"},
		{"events", chMeasureEventsQuery, "system.events"},
		{"async", chMeasureAsyncQuery, "system.asynchronous_inserts"},
		{"insert-size", chMeasureInsertSizeQuery, "system.query_log"},
	}
	for _, c := range cases {
		q := c.fn("ab")
		if !strings.Contains(q, "clusterAllReplicas('ab', "+c.tbl+")") {
			t.Errorf("%s: küme kipinde clusterAllReplicas('ab', %s) bekleniyor:\n%s", c.name, c.tbl, q)
		}
		if !strings.Contains(q, "LIMIT") || !strings.Contains(q, "max_execution_time") {
			t.Errorf("%s: LIMIT + max_execution_time şart:\n%s", c.name, q)
		}
		local := c.fn("")
		if strings.Contains(local, "clusterAllReplicas") || !strings.Contains(local, "FROM "+c.tbl) {
			t.Errorf("%s: tek node kipinde düz %s bekleniyor:\n%s", c.name, c.tbl, local)
		}
	}
}

func TestCHMeasurePartsQueryShape(t *testing.T) {
	q := chMeasurePartsQuery("")
	for _, want := range []string{"active", "database = currentDatabase()", "GROUP BY host, table, partition", "uniqExact(partition)", "max(pp)"} {
		if !strings.Contains(q, want) {
			t.Errorf("parça sorgusu %q taşımalı:\n%s", want, q)
		}
	}
}

func TestCHMeasureEventsAndInsertSizeShape(t *testing.T) {
	e := chMeasureEventsQuery("")
	for _, ev := range []string{"DelayedInserts", "RejectedInserts", "InsertedRows", "MergedRows", "uptime()"} {
		if !strings.Contains(e, ev) {
			t.Errorf("olay sorgusu %q taşımalı:\n%s", ev, e)
		}
	}
	i := chMeasureInsertSizeQuery("")
	for _, want := range []string{"query_kind = 'Insert'", "spans", "quantile(0.5)(written_rows)", "INTERVAL 1 HOUR"} {
		if !strings.Contains(i, want) {
			t.Errorf("insert-boyutu sorgusu %q taşımalı:\n%s", want, i)
		}
	}
}

func TestCHMeasureRouteLivesInOwnFile(t *testing.T) {
	src, err := os.ReadFile("clickhouse_measure.go")
	if err != nil {
		t.Fatal(err)
	}
	api := readAPISourceNoComments(t, "api.go")
	const pat = `"GET /api/admin/clickhouse/measure"`
	if !strings.Contains(string(src), pat) {
		t.Errorf("clickhouse_measure.go %s kalıbını taşımıyor", pat)
	}
	if strings.Contains(api, pat) {
		t.Errorf("api.go %s kaydediyor — api.go BÜYÜMEZ", pat)
	}
	if !strings.Contains(string(src), `registerRoutesExtra("clickhouse-measure"`) {
		t.Error("deftere kayıt yok")
	}
	if !strings.Contains(string(src), "auth.RequireRole(auth.RoleAdmin") {
		t.Error("admin kapısı kayıt satırında olmalı")
	}
}
