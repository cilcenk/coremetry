package api

import (
	"strings"
	"testing"
)

// v0.9.494 — koordinatör dağılımı panelinin iki sözleşmesi burada
// pinli: (1) SQL çok-node okumasını DOĞRU fan-out'la yapar,
// (2) dengesizlik oranı max/ortalama olarak hesaplanır.

// cluster() her shard'dan TEK replika okur; 4 node'lu (2 shard × 2
// replika) bir kurulumda panel hep 2 node gösterirdi — v0.9.454'te
// disk panelinde yaşanan operatör bug'ının aynısı. Bu test o hatanın
// bu uçta tekrarlanmasını engeller.
func TestCoordinatorQueryFanout(t *testing.T) {
	cl := coordinatorQuery("prod_cluster", 3600)
	if !strings.Contains(cl, "clusterAllReplicas('prod_cluster', system.query_log)") {
		t.Error("cluster modunda clusterAllReplicas yok — 4 node'lu kurulumda panel node'ların yalnız bir kısmını gösterir (v0.9.454 sınıfı)")
	}
	if strings.Contains(cl, "cluster('prod_cluster'") {
		t.Error("cluster() kullanılmış — shard başına TEK replika okur, replikalar görünmez olur")
	}

	solo := coordinatorQuery("", 3600)
	if !strings.Contains(solo, "FROM system.query_log") {
		t.Error("standalone modda düz system.query_log okunmalı")
	}
	if strings.Contains(solo, "clusterAllReplicas") {
		t.Error("cluster adı yokken clusterAllReplicas — cluster tanımsız hatası verir")
	}
}

// Her query_log okuması sınırlı olmalı: pencere + LIMIT + duvar saati.
func TestCoordinatorQueryBounds(t *testing.T) {
	q := coordinatorQuery("c", 7200)
	for _, want := range []string{
		"INTERVAL 7200 SECOND",         // pencere gerçekten bind edilmiş
		"type = 'QueryFinish'",         // yalnız tamamlanmış sorgular
		"is_initial_query = 1",         // shard alt-sorguları HARİÇ — panelin tüm anlamı bu
		"LIMIT 64",                     //
		"SETTINGS max_execution_time",  //
	} {
		if !strings.Contains(q, want) {
			t.Errorf("sorguda %q yok — sınırsız/yanlış query_log okuması", want)
		}
	}
	// quantile() değil quantileTDigest(): 1M+ satırda bellek farkı
	// (clickhouse-schema guardrail).
	if strings.Contains(q, "quantile(") {
		t.Error("çıplak quantile() — büyük query_log'da bellek patlatır, quantileTDigest kullan")
	}
}

// v0.9.502 — prod'da query_log YOK (log_queries=0 → tablo hiç yaratılmaz,
// okuma UNKNOWN_TABLE verir). Panel o kurulumda boş kalmak yerine
// system.events'e düşer; `InitialQuery` ProfileEvent'i query_log'daki
// is_initial_query=1'in birebir karşılığıdır, yani asıl sinyal korunur.
func TestCoordinatorEventsQuery(t *testing.T) {
	cl := coordinatorEventsQuery("uptrace")
	for _, want := range []string{
		"clusterAllReplicas('uptrace', system.events)", // cluster() DEĞİL
		"InitialQuery",   // koordinatör sinyali — panelin asıl rakamı
		"any(uptime())",  // sayaçlar açılıştan beri; restart etmiş node düşük okunur
		"SETTINGS max_execution_time",
		"LIMIT 64",
	} {
		if !strings.Contains(cl, want) {
			t.Errorf("events sorgusunda %q yok", want)
		}
	}
	if strings.Contains(cl, "cluster('uptrace'") {
		t.Error("cluster() kullanılmış — shard başına TEK replika okur, 4 node'un ikisi görünmez olur")
	}

	solo := coordinatorEventsQuery("")
	if !strings.Contains(solo, "FROM system.events") || strings.Contains(solo, "clusterAllReplicas") {
		t.Error("standalone modda düz system.events okunmalı")
	}
}

func TestImbalance(t *testing.T) {
	cases := []struct {
		name   string
		counts []uint64
		want   float64
	}{
		{"bos", nil, 1},
		{"tek node", []uint64{500}, 1},
		{"kusursuz dagilim", []uint64{100, 100, 100, 100}, 1},
		// 4 node, hepsi tek node'da → max/ortalama = 400/100 = 4.0.
		// Bugünkü SELECT durumunun tam sayısal karşılığı.
		{"hepsi tek node", []uint64{400, 0, 0, 0}, 4},
		// 2 node çalışıyor, 2 node boş → 200/100 = 2.0
		{"yarisi bosta", []uint64{200, 200, 0, 0}, 2},
		{"hafif egim", []uint64{120, 100, 90, 90}, 1.2},
		// Sıfır trafik dengesizlik DEĞİLDİR — 1.0 dönmeli, NaN değil.
		{"hic sorgu yok", []uint64{0, 0, 0}, 1},
		// Prod ölçümü 2026-08-01 (4 node, ~4.2 gün uptime, system.events
		// InitialQuery). İlk node giriş sorgularının %85.6'sını koordine
		// ediyordu — okuma havuzu dilimlerinin (v0.9.496/497) düzeltmeye
		// çalıştığı durumun sayısal fotoğrafı. Dilimler prod'a çıkınca bu
		// oranın düşmesi beklenir; test rakamı ÖLÇÜT olarak duruyor.
		{"prod 2026-08-01 giris", []uint64{22071239, 1565701, 1071510, 1079233}, 3.42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := imbalance(c.counts); got != c.want {
				t.Errorf("imbalance(%v) = %v, beklenen %v", c.counts, got, c.want)
			}
		})
	}
}

// v0.9.544 — KOORDİNATÖR panelinin events yolu da aynı tip hatasını
// taşıyordu ve bu daha ciddi: prod'da query_log KAPALI, yani prod tam
// bu yolu kullanıyor. `any(uptime())` UInt32 döner, CHCoordinator.UptimeS
// uint64; sürücü her satırda ColumnConverterError verir ve sessiz
// `continue` onu yutar → panel prod'da sessizce boş kalıyordu.
func TestCoordinatorEventsQueryCastsUptime(t *testing.T) {
	for _, cn := range []string{"", "uptrace_all"} {
		q := coordinatorEventsQuery(cn)
		if !strings.Contains(q, "toUInt64(any(uptime()))") {
			t.Errorf("uptime UInt64'e cast edilmeli — prod bu yolu kullanıyor:\n%s", q)
		}
		if strings.Contains(q, "       any(uptime())") {
			t.Errorf("cast'siz any(uptime()) kalmış:\n%s", q)
		}
	}
}
