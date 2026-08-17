package chstore

import (
	"context"
	"time"
)

// DBTrendPoint is one 5-minute bucket of a database's RED trend.
// Aligned to the db_summary_5m time_bucket grid so the frontend
// can stitch sparklines without re-bucketing. t is unix ns at the
// bucket start; the three RED series ride alongside it.
//
// Rps is spans/sec within the bucket (span_count / 300s, since
// the MV buckets on a 5-minute = 300s interval). ErrorRate is
// 0..100. P99Ms is the merged 0.99 quantile in milliseconds.
type DBTrendPoint struct {
	T         int64   `json:"t"`         // unix ns — bucket start
	Rps       float64 `json:"rps"`       // call rate: span_count / 300
	ErrorRate float64 `json:"errorRate"` // 0..100
	P99Ms     float64 `json:"p99Ms"`     // p99 duration, ms
}

// DBTrend is the per-row sparkline + latest-bucket health snapshot
// for one database on the /databases (or /messaging) overview grid.
// Keyed identically to chstore.DBInstance / the frontend DepRow:
// (DbSystem, Instance, DbName, Cluster). Cluster is empty for
// db_summary_5m-sourced rows (DB rows carry no cluster dimension);
// it rides the struct so the same shape can serve the messaging
// grid join key without a second type.
//
// Points is an ascending-time array of ~bucketsPerWindow entries
// (one per 5-minute bucket the window covers, capped). The Cur*
// fields are the latest non-empty bucket's snapshot — what the
// per-row health gauge renders without the frontend having to
// scan the array.
type DBTrend struct {
	DbSystem string `json:"dbSystem"`
	Instance string `json:"instance"`
	DbName   string `json:"dbName"`
	Cluster  string `json:"cluster"`

	Points []DBTrendPoint `json:"points"`

	// Son TAM kovanın sağlık anlık görüntüsü (rozet kaynağı).
	//
	// v0.9.820 — "son kova" idi, "son TAM kova" oldu. Canlı bir pencerede
	// en son kova henüz DOLUYOR: 5 dakikalık bir kovanın ilk saniyesinde
	// okunan rps neredeyse sıfır, p99 ise o ana dek görülmüş birkaç
	// span'in quantile'ı. Rozet bu yüzden her sayfa yenilemesinde
	// "trafik durdu / gecikme düzeldi" diye parlıyordu ve operatör
	// sayfayı yenileyince kendi kendine "düzelen" bir sistem görüyordu.
	// Sağlık göstergesinin dalgalanması, sağlık göstergesinin olmamasından
	// kötüdür.
	CurRps       float64 `json:"curRps"`
	CurErrorRate float64 `json:"curErrorRate"` // 0..100
	CurP99Ms     float64 `json:"curP99Ms"`
	// CurFromPartial — pencerede TEK BİR tam kova bile yoktu, rozet
	// dolmakta olan kovadan okundu. omitempty: normal hâlde alan hiç
	// çıkmaz; çıktığında frontend tooltip'i bunu söyler.
	CurFromPartial bool `json:"curFromPartial,omitempty"`
}

// dbTrendCurrentIdx — sağlık rozetinin okunacağı noktanın indeksi.
// SAF; tablo-güdümlü test (databases_series_test.go).
//
// Sondan geriye ilk TAM kovayı arar. Hiç yoksa (pencere tek ve hâlâ
// dolan bir kovadan ibaret) son noktaya düşer ve partial=true ile bunu
// İLAN eder — sessizce yanlış bir sayı basmaktansa "bu sayı henüz
// oturmadı" demek.
//
// Boş seri → (-1, false): çağıran Cur* alanlarına dokunmaz.
func dbTrendCurrentIdx(pts []DBTrendPoint, bucketSec, nowUnix int64) (int, bool) {
	if len(pts) == 0 {
		return -1, false
	}
	for i := len(pts) - 1; i >= 0; i-- {
		start := pts[i].T / 1e9
		if start+bucketSec <= nowUnix {
			return i, false
		}
	}
	return len(pts) - 1, true
}

// dbTrendBucketSeconds is the MV bucket width in seconds. The
// db_summary_5m MV groups on toStartOfInterval(time, INTERVAL 5
// MINUTE), so each bucket spans 300s — the divisor that turns a
// bucket's span_count into a rate.
const dbTrendBucketSeconds = 300.0

// GetDBTrends reads db_summary_5m bucketed by (db_system,
// instance, db_name, time_bucket) over [from,to] and returns one
// DBTrend per (db_system, instance, db_name) — a small call-rate /
// p99 / error-rate sparkline plus the latest-bucket health
// snapshot. Drives the per-row RED sparklines (#1) + health
// gauges (#6) on the /databases overview grid.
//
// MV-only (NOT raw spans) per the aggregate-read invariant — same
// AggregatingMergeTree the overview's GetDatabases reads, so the
// row identities line up exactly. The query is bounded three ways
// even though the MV is already small: time-bounded WHERE on the
// ORDER-BY-leading time_bucket, a LIMIT on the result, and a
// max_execution_time guard.
//
// The from is truncated to the 5-minute grid so a rolling window
// snaps to bucket boundaries (the same trick GetDatabases uses) —
// keeps the cache key + bucket alignment stable across adjacent
// polls.
//
// v0.9.823 — ÜST SINIR `< to`, `<= to` DEĞİL. MV kovaları
// BAŞLANGIÇLARIYLA etiketli: `<= to` ile başlangıcı tam `to` olan kova
// da giriyor, oysa o kova [to, to+5dk) aralığını kapsıyor — istenen
// pencerenin TAMAMEN dışında. Sparkline'ın sonuna pencereye ait
// olmayan bir nokta ekleniyordu (fark yalnız `to` kova sınırına tam
// oturduğunda görünür; hizalanmamış `to`'da iki operatör aynı sonucu
// verir). Sözleşme db_bucket_bound_test.go'da sabitli; GetDatabases ve
// dbTopCallersSQL ile AYNI sınır.
func (s *Store) GetDBTrends(ctx context.Context, from, to time.Time) ([]DBTrend, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	bucketStart := from.Truncate(5 * time.Minute)

	// One row per (db_system, instance, db_name, time_bucket).
	// ORDER BY trailing time_bucket so points arrive ascending
	// and we can append directly into each series. The LIMIT is a
	// hard safety net: at 5000 DBs × a 24h window (288 buckets)
	// the worst case is bounded; in practice the MV holds far
	// fewer rows because most DBs aren't seen every bucket.
	//
	// countIfMerge for the error state because the MV defines it
	// as countIfState(status_code = 'error'); countMerge would
	// silently read the wrong aggregate.
	rows, err := s.conn.Query(ctx, `
		SELECT db_system,
		       instance,
		       db_name,
		       toUnixTimestamp64Nano(toDateTime64(time_bucket, 9))                     AS bucket_ns,
		       countMerge(span_count_state)                                            AS span_count,
		       countIfMerge(error_count_state)                                         AS error_count,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		GROUP BY db_system, instance, db_name, time_bucket
		ORDER BY db_system, instance, db_name, time_bucket
		LIMIT 200000
		SETTINGS max_execution_time = 15`, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DBTrend{}
	type key struct{ system, instance, dbName string }
	idxByKey := map[key]int{}

	for rows.Next() {
		var (
			system, instance, dbName string
			bucketNs                 int64
			spanCount, errorCount    uint64
			p99Ms                    *float64
		)
		if err := rows.Scan(&system, &instance, &dbName, &bucketNs, &spanCount, &errorCount, &p99Ms); err != nil {
			return nil, err
		}
		k := key{system, instance, dbName}
		i, ok := idxByKey[k]
		if !ok {
			out = append(out, DBTrend{
				DbSystem: system,
				Instance: instance,
				DbName:   dbName,
				Points:   []DBTrendPoint{},
			})
			i = len(out) - 1
			idxByKey[k] = i
		}

		pt := DBTrendPoint{
			T:     bucketNs,
			Rps:   float64(spanCount) / dbTrendBucketSeconds,
			P99Ms: safeF(p99Ms),
		}
		if spanCount > 0 {
			pt.ErrorRate = float64(errorCount) / float64(spanCount) * 100
		}
		out[i].Points = append(out[i].Points, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// v0.9.820 — Cur* artık son TAM kovadan. Eskiden döngü içinde her
	// noktada üzerine yazılıyordu, yani daima DOLMAKTA olan son kovaya
	// oturuyordu (gerekçe: DBTrend başlığı).
	applyDBTrendCurrent(out, time.Now().Unix())
	return out, nil
}

// applyDBTrendCurrent — her trendin Cur* alanlarını son TAM kovadan
// doldurur. GetDBTrends ve GetMessagingTrends'in ORTAK adımı: iki
// sayfanın sağlık rozeti aynı tanımdan okumalı.
func applyDBTrendCurrent(out []DBTrend, nowUnix int64) {
	for i := range out {
		idx, partial := dbTrendCurrentIdx(out[i].Points, dbTrendBucketSeconds, nowUnix)
		if idx < 0 {
			continue
		}
		p := out[i].Points[idx]
		out[i].CurRps = p.Rps
		out[i].CurErrorRate = p.ErrorRate
		out[i].CurP99Ms = p.P99Ms
		out[i].CurFromPartial = partial
	}
}

// DbNamesBySystem returns the dominant db.name (schema / instance) per
// db.system over [from,to], ranked by call volume, read from db_summary_5m
// (the db.name dimension added in v0.5.327). The service-graph endpoint uses
// it to enrich database nodes — which are keyed on db.system — with the OTel
// db.name on their card. MV-only (never raw spans); the MV coalesces a missing
// db.name to 'default', which we skip so only a real schema/instance shows.
func (s *Store) DbNamesBySystem(ctx context.Context, from, to time.Time) (map[string]string, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	bucketStart := from.Truncate(5 * time.Minute)
	rows, err := s.conn.Query(ctx, `
		SELECT db_system, db_name, countMerge(span_count_state) AS calls
		FROM db_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		  AND db_name != '' AND db_name != 'default'
		GROUP BY db_system, db_name
		ORDER BY db_system, calls DESC
		LIMIT 5000
		SETTINGS max_execution_time = 10`, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// ORDER BY db_system, calls DESC → the first row seen for a db_system is
	// its busiest db.name; keep that one.
	out := map[string]string{}
	for rows.Next() {
		var system, dbName string
		var calls uint64
		if err := rows.Scan(&system, &dbName, &calls); err != nil {
			return nil, err
		}
		if _, seen := out[system]; !seen {
			out[system] = dbName
		}
	}
	return out, rows.Err()
}

// GetMessagingTrends (v0.9.434, kuyruk #3b — desen paritesi) —
// GetDBTrends'in messaging ikizi: messaging_summary_5m'i
// (msg_system, cluster, destination, time_bucket) kırılımında okur,
// satır başına sparkline + son-bucket sağlık anlık görüntüsü döner.
// DBTrend şekli yeniden kullanılır (başlık yorumu bunu zaten
// vadediyordu): DbSystem=msg_system, Instance=destination,
// Cluster=cluster, DbName boş — frontend join anahtarı messaging'de
// (system|cluster|destination). MV-only; üç sınır (time-bounded WHERE
// + LIMIT + max_execution_time). error_count_state bu MV'de countState
// — countMerge (db_summary_5m'in countIfMerge'ünden farklı, getMessaging
// ile birebir aynı okuma).
func (s *Store) GetMessagingTrends(ctx context.Context, from, to time.Time) ([]DBTrend, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	bucketStart := from.Truncate(5 * time.Minute)
	rows, err := s.conn.Query(ctx, `
		SELECT msg_system,
		       cluster,
		       destination,
		       toUnixTimestamp64Nano(toDateTime64(time_bucket, 9))                     AS bucket_ns,
		       countMerge(span_count_state)                                            AS span_count,
		       countMerge(error_count_state)                                           AS error_count,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		GROUP BY msg_system, cluster, destination, time_bucket
		ORDER BY msg_system, cluster, destination, time_bucket
		LIMIT 200000
		SETTINGS max_execution_time = 15`, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DBTrend{}
	type key struct{ system, cluster, dest string }
	idxByKey := map[key]int{}
	for rows.Next() {
		var (
			system, cluster, dest string
			bucketNs              int64
			spanCount, errorCount uint64
			p99Ms                 *float64
		)
		if err := rows.Scan(&system, &cluster, &dest, &bucketNs, &spanCount, &errorCount, &p99Ms); err != nil {
			return nil, err
		}
		k := key{system, cluster, dest}
		i, ok := idxByKey[k]
		if !ok {
			out = append(out, DBTrend{
				DbSystem: system,
				Instance: dest,
				Cluster:  cluster,
				Points:   []DBTrendPoint{},
			})
			i = len(out) - 1
			idxByKey[k] = i
		}
		pt := DBTrendPoint{
			T:     bucketNs,
			Rps:   float64(spanCount) / dbTrendBucketSeconds,
			P99Ms: safeF(p99Ms),
		}
		if spanCount > 0 {
			pt.ErrorRate = float64(errorCount) / float64(spanCount) * 100
		}
		out[i].Points = append(out[i].Points, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// v0.9.820 — DB ikiziyle aynı kural: rozet son TAM kovadan.
	applyDBTrendCurrent(out, time.Now().Unix())
	return out, nil
}
