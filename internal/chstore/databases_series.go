package chstore

// databases_series.go — /databases sayfasının KPI şeridi + üç grafiği
// (v0.9.820).
//
// Sayfa iki tablo + bir ifade listesiydi: her satır pencerenin TOPLAMINI
// gösteriyordu ve "veritabanı ne zaman yavaşladı" sorusu ancak satır
// açılıp drawer'a bakılarak — instance instance — cevaplanabiliyordu.
// Filo genelinde "hangi motor ne zaman tırmandı", "hata dalgası nerede"
// soruları hiç cevaplanamıyordu.
//
// TEK MV TARAMASI üç şeyi birden veriyor (İKİ SEVİYELİ WITH ROLLUP):
//
//	(t, motor) → hacim grafiğinin motor kırılımı
//	(t, '')    → o kovanın FİLO toplamı + GERÇEK merge edilmiş quantile'ı
//	(0, '')    → pencerenin toplamı + gerçek pencere quantile'ı
//
// Quantile'ın ROLLUP'tan gelmesi optimizasyon değil DOĞRULUK meselesi:
// motorların p95'leri ne toplanır ne ortalanır. Canlı ölçüm (chc-0, 15 dk):
// filo p95 = 186 ms iken oracle 230, redis 8.9 — ortalama da maksimum da
// yanlış cevap verirdi.
//
// db_system MV'de ASLA boş değil (MV `WHERE db_system != ''` ile
// tanımlı), o yüzden '' güvenle ROLLUP işareti olarak kullanılıyor.

import (
	"context"
	"strconv"
	"time"
)

const (
	// dbSeriesBucketSeconds — db_summary_5m'in kova genişliği. Seri
	// buradan türediği için istemci adım SEÇMEZ; sunucu ne varsa onu
	// söyler (BucketSeconds zarfta).
	dbSeriesBucketSeconds = 300
	// dbSeriesLimit — satır tavanı. Kova × motor: 7 günlük pencere
	// (2016 kova) × 10 motor bile bu tavanın altında.
	dbSeriesLimit = 30000
	// dbSeriesMaxEngines — hacim grafiğine giden AYRI motor sayısı.
	// Fazlası frontend'de foldTopN ile "others"a katlanıyor; sunucu
	// tarafında kesmek, katlanan payı GÖRÜNMEZ yapardı.
	dbSeriesMaxEngines = 12
)

// DBSeriesPoint — serinin bir 5-dk kovası (FİLO toplamı).
type DBSeriesPoint struct {
	TimeS         int64   `json:"timeS"` // kova başlangıcı, unix saniye
	Queries       uint64  `json:"queries"`
	Errors        uint64  `json:"errors"`
	QueriesPerMin float64 `json:"queriesPerMin"`
	ErrorRate     float64 `json:"errorRate"` // yüzde
	P50Ms         float64 `json:"p50Ms"`
	P95Ms         float64 `json:"p95Ms"`
	P99Ms         float64 `json:"p99Ms"`
}

// DBEngineSeriesPoint — bir motorun bir kovadaki hacmi. Gecikme TAŞIMAZ:
// motor kırılımı hacim grafiği için var ve motor başına quantile'ı da
// taşımak, aynı ekranda birbirine benzeyen ama farklı merge'lerden gelen
// iki p95 seti üretirdi.
type DBEngineSeriesPoint struct {
	TimeS         int64   `json:"timeS"`
	Queries       uint64  `json:"queries"`
	QueriesPerMin float64 `json:"queriesPerMin"`
}

// DBEngineSeries — bir motorun zaman serisi.
type DBEngineSeries struct {
	System string                `json:"system"`
	Points []DBEngineSeriesPoint `json:"points"`
}

// DatabasesSeries — /api/databases/series zarfı.
type DatabasesSeries struct {
	SeriesWindow
	Points  []DBSeriesPoint  `json:"points"`
	Engines []DBEngineSeries `json:"engines"`

	// Pencere KPI'ları — ROLLUP satırından (0, '').
	Queries       uint64  `json:"queries"`
	Errors        uint64  `json:"errors"`
	QueriesPerMin float64 `json:"queriesPerMin"`
	// ErrorRate pencere TOPLAMLARINDAN; kova oranlarının ortalaması
	// sessiz bir 5 dakikayı yoğun bir 5 dakikayla eşit ağırlardı.
	ErrorRate float64 `json:"errorRate"`
	P50Ms     float64 `json:"p50Ms"`
	P95Ms     float64 `json:"p95Ms"`
	P99Ms     float64 `json:"p99Ms"`
	// PriorP95Ms — bir önceki EŞİT pencerenin p95'i (compare açıkken).
	// omitempty: 0 ms ölçüm değil, ölçüm YOKLUĞUDUR → Δ çizilmez.
	PriorP95Ms float64 `json:"priorP95Ms,omitempty"`
}

// DatabasesSeriesQuery — uç girdileri. Sayfanın kendi filtreleri
// (?dbsys= / ?dbname=) taşınır ki şeritteki sayı ile tablodaki satırlar
// aynı kümeyi anlatsın.
type DatabasesSeriesQuery struct {
	From, To time.Time
	DBSystem string
	DBName   string
	Compare  bool
}

// dbSeriesFilterSQL — dbsys/dbname yüklemi ve args'ı, SQL'deki `?`
// sırasıyla birebir. İkisi de KATALOG değeri (tablodaki select'ten
// geliyor), serbest metin değil — yine de bind ediliyor.
func dbSeriesFilterSQL(system, name string) (string, []any) {
	sql := ""
	args := []any{}
	if system != "" {
		sql += " AND db_system = ?"
		args = append(args, system)
	}
	if name != "" {
		sql += " AND db_name = ?"
		args = append(args, name)
	}
	return sql, args
}

// dbSeriesRow — taranan ham satır. system == "" → ROLLUP satırı
// (t > 0 ise kova filo toplamı, t == 0 ise pencere toplamı).
type dbSeriesRow struct {
	t       int64
	system  string
	queries uint64
	errors  uint64
	p50Ms   float64
	p95Ms   float64
	p99Ms   float64
}

// buildDatabasesSeries — ham satırları zarfa katlar. SAF.
//
// ÜÇ SATIR SINIFI birbirine KARIŞTIRILMAMALI:
//   - (0, "")   pencere toplamı → KPI'lar, nokta DEĞİL
//   - (t, "")   kova filo toplamı → Points
//   - (t, sys)  motor kırılımı → Engines
//
// Bir motor satırının Points'e sızması grafiği o motorun trafiğiyle
// filo trafiği arasında gidip gelen bir testereye çevirirdi.
func buildDatabasesSeries(
	rows []dbSeriesRow, windowSeconds float64,
	coveredFromNs, coveredToNs int64, nowUnix int64,
) *DatabasesSeries {
	out := &DatabasesSeries{
		SeriesWindow: SeriesWindow{
			BucketSeconds: dbSeriesBucketSeconds,
			CoveredFromNs: coveredFromNs,
			CoveredToNs:   coveredToNs,
		},
		Points:  []DBSeriesPoint{},
		Engines: []DBEngineSeries{},
	}
	perMin := func(n uint64) float64 {
		return float64(n) / (float64(dbSeriesBucketSeconds) / 60)
	}
	engIdx := map[string]int{}
	for _, r := range rows {
		switch {
		case r.t == 0:
			// Pencere toplamı. (Zaman-sınırlı WHERE yüzünden gerçek bir
			// kova asla epoch'a düşemez.)
			out.Queries = r.queries
			out.Errors = r.errors
			out.P50Ms = r.p50Ms
			out.P95Ms = r.p95Ms
			out.P99Ms = r.p99Ms
		case r.system == "":
			p := DBSeriesPoint{
				TimeS: r.t, Queries: r.queries, Errors: r.errors,
				QueriesPerMin: perMin(r.queries),
				P50Ms:         r.p50Ms, P95Ms: r.p95Ms, P99Ms: r.p99Ms,
			}
			if r.queries > 0 {
				p.ErrorRate = float64(r.errors) / float64(r.queries) * 100
			}
			out.Points = append(out.Points, p)
		default:
			i, ok := engIdx[r.system]
			if !ok {
				if len(out.Engines) >= dbSeriesMaxEngines {
					continue
				}
				out.Engines = append(out.Engines, DBEngineSeries{
					System: r.system, Points: []DBEngineSeriesPoint{},
				})
				i = len(out.Engines) - 1
				engIdx[r.system] = i
			}
			out.Engines[i].Points = append(out.Engines[i].Points, DBEngineSeriesPoint{
				TimeS: r.t, Queries: r.queries, QueriesPerMin: perMin(r.queries),
			})
		}
	}
	if out.Queries > 0 {
		out.ErrorRate = float64(out.Errors) / float64(out.Queries) * 100
	}
	// Oran İSTENEN pencereye bölünür, kova sayısına değil.
	if windowSeconds > 0 {
		out.QueriesPerMin = float64(out.Queries) / (windowSeconds / 60)
	}
	if n := len(out.Points); n > 0 {
		out.PartialLastBucket = seriesLastBucketPartial(
			out.Points[n-1].TimeS, dbSeriesBucketSeconds, coveredToNs/1e9, nowUnix)
	}
	return out
}

// GetDatabasesSeries — KPI şeridi + üç grafiğin kaynağı.
func (s *Store) GetDatabasesSeries(
	ctx context.Context, q DatabasesSeriesQuery,
) (*DatabasesSeries, error) {
	if q.From.IsZero() {
		q.From = time.Now().Add(-1 * time.Hour)
	}
	if q.To.IsZero() {
		q.To = time.Now()
	}
	// PENCERE HİZASI KAYNAKTA DOĞRU.
	//
	// Alt sınır AŞAĞI yuvarlanır: MV kovaları başlangıçlarıyla etiketli,
	// hizalanmamış `>= from` baştaki kısmi kovayı tamamen elerdi
	// (from=10:03 → 10:00-10:05 arası her şey düşer).
	//
	// Üst sınır `< to`, `<= to` DEĞİL — kardeş okumaların (GetDatabases,
	// GetDBTrends) sınıfı bu: `<= to` ile başlangıcı tam `to` olan kova da
	// giriyor, oysa o kova [to, to+5dk) aralığını kapsıyor, yani İSTENEN
	// PENCERENİN TAMAMEN DIŞINDA. Grafiğe fazladan bir kova eklemek
	// serinin sonuna pencereye ait olmayan bir nokta koymak demek.
	bucketStart := q.From.Truncate(5 * time.Minute)
	now := time.Now()
	filterSQL, filterArgs := dbSeriesFilterSQL(q.DBSystem, q.DBName)

	args := append([]any{bucketStart, q.To}, filterArgs...)
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT toUnixTimestamp(time_bucket) AS t,
		       db_system,
		       countMerge(span_count_state)    AS c,
		       countIfMerge(error_count_state) AS e,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?`+filterSQL+`
		GROUP BY t, db_system WITH ROLLUP
		ORDER BY t, db_system
		LIMIT `+strconv.Itoa(dbSeriesLimit)+`
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	var scanned []dbSeriesRow
	for rows.Next() {
		var r dbSeriesRow
		var p50, p95, p99 *float64
		// toUnixTimestamp() UInt32 döndürür — sürücü *int64'e ÇEVİRMEZ
		// (v0.9.817'de messaging'in iki kardeşi tam bu yüzden çizilmiyordu).
		var t uint32
		if err := rows.Scan(&t, &r.system, &r.queries, &r.errors, &p50, &p95, &p99); err != nil {
			rows.Close()
			return nil, err
		}
		r.t = int64(t)
		// v0.5.301 — NaN/Inf JSON'a çıkmadan temizlenir.
		r.p50Ms = safeF(p50)
		r.p95Ms = safeF(p95)
		r.p99Ms = safeF(p99)
		scanned = append(scanned, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := buildDatabasesSeries(
		scanned, q.To.Sub(q.From).Seconds(),
		bucketStart.UnixNano(), q.To.UnixNano(), now.Unix())

	// Bir önceki EŞİT pencerenin p95'i — "Filo p95" karosunun Δ'sı.
	// AYRI ve küçük bir sorgu (GROUP BY yok, tek satır). Hata ÖLÜMCÜL
	// DEĞİL: Δ çizilmez, geri kalan her şey doğru.
	if q.Compare {
		dur := q.To.Sub(q.From)
		pArgs := append([]any{bucketStart.Add(-dur), bucketStart}, filterArgs...)
		var priorP95 *float64
		if err := s.telemetryReadConn().QueryRow(ctx, `
			SELECT arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6
			FROM db_summary_5m
			WHERE time_bucket >= ? AND time_bucket < ?`+filterSQL+`
			SETTINGS max_execution_time = 8`, pArgs...).Scan(&priorP95); err == nil {
			out.PriorP95Ms = safeF(priorP95)
		}
	}
	return out, nil
}
