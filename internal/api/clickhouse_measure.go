package api

// clickhouse_measure.go — v0.10.683 (kuyruk 1; ClickHouse mimari denetimi
// docs/audit/clickhouse-architecture-advisor-2026-09-10.md öneri 1/2/8:
// "doğrulama sorgularını /admin/clickhouse'a taşı; BatchSize 10k→50k A/B").
//
//   GET /api/admin/clickhouse/measure        (admin; serveCached 30 s)
//
// /admin/clickhouse zaten yavaş sorgu, merge, tablo-bazlı part hotspot,
// async insert ve mutasyonu gösteriyor (clickhouse_health.go). Burası
// denetimin EKSİK bıraktığı dört ölçümü host bazında getirir:
//   - parça baskısı: host × tablo → partition sayısı, aktif parça, satır,
//     partition başına EN ÇOK parça (parts_to_delay_insert sinyali);
//   - system.events: DelayedInserts / RejectedInserts / InsertedRows /
//     MergedRows + uptime (kümülatif; UI saat başına hız türetir);
//   - system.asynchronous_inserts: host başına tampon sayısı ve bayt
//     (pod başına tampon × N pod = sunucu belleği);
//   - insert boyutu: system.query_log'dan spans insert'lerinin satır medyanı
//     (1 saat). Prod'da query_log KAPALI olabilir (log_queries=0 → tablo
//     yok): slot "kullanılamıyor" diye İLAN edilir, sessiz sıfır yok.
//   + ingestBatchSize: A/B için yürürlükteki BatchSize (main.go besler).
//
// Küme adı varsa HER sistem tablosu clusterAllReplicas ile okunur — bu
// tablolar node-yereldir; tek node okumak kümenin gerisini "sıfır"
// gösterir (v0.9.540 merges dersi). Her sorgu LIMIT + max_execution_time.
// Her bacak bağımsız: biri düşerse Note yazar, diğerleri döner. api.go
// BÜYÜMEZ — route_registry defteri; BatchSize de bu yüzden paket düzeyinde
// (Server struct'ına alan eklemek api.go'yu büyütürdü).

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

func init() { registerRoutesExtra("clickhouse-measure", (*Server).registerCHMeasureRoutes) }

func (s *Server) registerCHMeasureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/clickhouse/measure", auth.RequireRole(auth.RoleAdmin, s.getCHMeasure))
}

// ingestBatchSize — yürürlükteki consumer BatchSize'ı (main.go SetIngestBatchSize).
// Yalnız gösterim: A/B'de operatör hangi değerin ölçüldüğünü panelde görsün.
var ingestBatchSize atomic.Int64

func SetIngestBatchSize(n int) { ingestBatchSize.Store(int64(n)) }

type CHMeasurePartsRow struct {
	Host                 string `json:"host"`
	Table                string `json:"table"`
	Partitions           uint64 `json:"partitions"`
	Parts                uint64 `json:"parts"`
	Rows                 uint64 `json:"rows"`
	MaxPartsPerPartition uint64 `json:"maxPartsPerPartition"`
}

type CHMeasureEventsRow struct {
	Host            string `json:"host"`
	UptimeS         uint64 `json:"uptimeS"`
	DelayedInserts  uint64 `json:"delayedInserts"`
	RejectedInserts uint64 `json:"rejectedInserts"`
	InsertedRows    uint64 `json:"insertedRows"`
	MergedRows      uint64 `json:"mergedRows"`
}

type CHMeasureAsyncRow struct {
	Host    string `json:"host"`
	Buffers uint64 `json:"buffers"`
	Bytes   uint64 `json:"bytes"`
}

type CHMeasureInsertRow struct {
	Host          string  `json:"host"`
	RowsPerInsert float64 `json:"rowsPerInsert"`
	Inserts       uint64  `json:"inserts"`
}

type CHMeasureResponse struct {
	Mode        string `json:"mode"` // "cluster" | "standalone"
	Cluster     string `json:"cluster,omitempty"`
	GeneratedAt int64  `json:"generatedAt"`
	BatchSize   int64  `json:"batchSize"`

	Parts     []CHMeasurePartsRow `json:"parts"`
	PartsNote string              `json:"partsNote,omitempty"`

	Events     []CHMeasureEventsRow `json:"events"`
	EventsNote string               `json:"eventsNote,omitempty"`

	Async     []CHMeasureAsyncRow `json:"async"`
	AsyncNote string              `json:"asyncNote,omitempty"`

	// InsertSize yalnız query_log açıkken; QueryLogAvailable=false ise slot
	// "kullanılamıyor" — prod'da log_queries=0 tipiktir.
	InsertSize        []CHMeasureInsertRow `json:"insertSize"`
	InsertSizeNote    string               `json:"insertSizeNote,omitempty"`
	QueryLogAvailable bool                 `json:"queryLogAvailable"`
}

// chMeasureSource — küme adı varsa clusterAllReplicas, yoksa düz tablo.
func chMeasureSource(clusterName, table string) string {
	if cn := strings.TrimSpace(clusterName); cn != "" {
		return "clusterAllReplicas('" + cn + "', " + table + ")"
	}
	return table
}

// chMeasurePartsQuery — host × tablo; partition düzeyinde iç gruplama ki
// "partition başına en çok parça" (parts_to_delay_insert sinyali) çıksın.
func chMeasurePartsQuery(clusterName string) string {
	return `
		SELECT host, table,
		       uniqExact(partition) AS partitions,
		       sum(pp)              AS parts,
		       sum(prows)           AS rows,
		       max(pp)              AS max_pp
		FROM (
		  SELECT hostName() AS host, table, partition, count() AS pp, sum(rows) AS prows
		  FROM ` + chMeasureSource(clusterName, "system.parts") + `
		  WHERE active AND database = currentDatabase()
		  GROUP BY host, table, partition
		)
		GROUP BY host, table
		ORDER BY parts DESC, host
		LIMIT 200
		SETTINGS max_execution_time = 5`
}

// chMeasureEventsQuery — kümülatif sayaçlar + uptime (UI hız türetir).
func chMeasureEventsQuery(clusterName string) string {
	return `
		SELECT hostName()                                     AS host,
		       toUInt64(any(uptime()))                        AS uptime_s,
		       sumIf(value, event = 'DelayedInserts')         AS delayed,
		       sumIf(value, event = 'RejectedInserts')        AS rejected,
		       sumIf(value, event = 'InsertedRows')           AS inserted_rows,
		       sumIf(value, event = 'MergedRows')             AS merged_rows
		FROM ` + chMeasureSource(clusterName, "system.events") + `
		GROUP BY host
		ORDER BY host
		LIMIT 64
		SETTINGS max_execution_time = 5`
}

// chMeasureAsyncQuery — host başına async tampon sayısı ve bayt. Tamponu
// olmayan host satır ÜRETMEZ (GROUP BY boş küme) — UI bunu "0" değil
// "listede yok" diye okur.
func chMeasureAsyncQuery(clusterName string) string {
	return `
		SELECT hostName() AS host, count() AS buffers, sum(total_bytes) AS bytes
		FROM ` + chMeasureSource(clusterName, "system.asynchronous_inserts") + `
		GROUP BY host
		ORDER BY host
		LIMIT 64
		SETTINGS max_execution_time = 5`
}

// chMeasureInsertSizeQuery — son 1 saatte spans insert'lerinin satır medyanı
// (denetim öneri 1: 10k tabanda; 10k–100k bandı önerilir). Küme kipinde
// yazım Distributed `spans`'a, yerelde `spans_local`'a düşebilir; ikisi de.
func chMeasureInsertSizeQuery(clusterName string) string {
	return `
		SELECT hostName()                     AS host,
		       quantile(0.5)(written_rows)    AS rows_per_insert,
		       count()                        AS inserts
		FROM ` + chMeasureSource(clusterName, "system.query_log") + `
		WHERE type = 'QueryFinish' AND query_kind = 'Insert'
		  AND event_time > now() - INTERVAL 1 HOUR
		  AND hasAny(tables, [currentDatabase() || '.spans', currentDatabase() || '.spans_local'])
		GROUP BY host
		ORDER BY host
		LIMIT 64
		SETTINGS max_execution_time = 5`
}

func (s *Server) getCHMeasure(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "ch:measure:v1", 30*time.Second, func(ctx context.Context) (any, error) {
		cn := strings.TrimSpace(s.store.ClusterName())
		out := CHMeasureResponse{
			Mode: "standalone", Cluster: cn, GeneratedAt: time.Now().UnixNano(),
			BatchSize:  ingestBatchSize.Load(),
			Parts:      []CHMeasurePartsRow{},
			Events:     []CHMeasureEventsRow{},
			Async:      []CHMeasureAsyncRow{},
			InsertSize: []CHMeasureInsertRow{},
		}
		if cn != "" {
			out.Mode = "cluster"
		}
		conn := s.store.Conn()

		// Her bacak bağımsız: hata Note'a, diğerleri döner. Tarama hatası
		// YUTULMAZ (v0.9.544 dersi: yutulan hata "satır yok" diye okunuyordu).
		if rows, err := conn.Query(ctx, chMeasurePartsQuery(cn)); err != nil {
			out.PartsNote = "system.parts okunamadı: " + err.Error()
		} else {
			for rows.Next() {
				var p CHMeasurePartsRow
				if err := rows.Scan(&p.Host, &p.Table, &p.Partitions, &p.Parts, &p.Rows, &p.MaxPartsPerPartition); err != nil {
					out.PartsNote = "system.parts satırı çözümlenemedi: " + err.Error()
					break
				}
				out.Parts = append(out.Parts, p)
			}
			rows.Close()
		}

		if rows, err := conn.Query(ctx, chMeasureEventsQuery(cn)); err != nil {
			out.EventsNote = "system.events okunamadı: " + err.Error()
		} else {
			for rows.Next() {
				var e CHMeasureEventsRow
				if err := rows.Scan(&e.Host, &e.UptimeS, &e.DelayedInserts, &e.RejectedInserts, &e.InsertedRows, &e.MergedRows); err != nil {
					out.EventsNote = "system.events satırı çözümlenemedi: " + err.Error()
					break
				}
				out.Events = append(out.Events, e)
			}
			rows.Close()
		}

		if rows, err := conn.Query(ctx, chMeasureAsyncQuery(cn)); err != nil {
			out.AsyncNote = "system.asynchronous_inserts okunamadı: " + err.Error()
		} else {
			for rows.Next() {
				var a CHMeasureAsyncRow
				if err := rows.Scan(&a.Host, &a.Buffers, &a.Bytes); err != nil {
					out.AsyncNote = "system.asynchronous_inserts satırı çözümlenemedi: " + err.Error()
					break
				}
				out.Async = append(out.Async, a)
			}
			rows.Close()
		}

		if rows, err := conn.Query(ctx, chMeasureInsertSizeQuery(cn)); err != nil {
			out.InsertSizeNote = "system.query_log kullanılamıyor (prod'da log_queries=0 tipiktir): " + err.Error()
		} else {
			out.QueryLogAvailable = true
			for rows.Next() {
				var i CHMeasureInsertRow
				if err := rows.Scan(&i.Host, &i.RowsPerInsert, &i.Inserts); err != nil {
					out.InsertSizeNote = "system.query_log satırı çözümlenemedi: " + err.Error()
					break
				}
				out.InsertSize = append(out.InsertSize, i)
			}
			rows.Close()
		}
		return out, nil
	})
}
