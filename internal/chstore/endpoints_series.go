package chstore

// endpoints_series.go — /endpoints sayfasının KPI şeridi + üç grafiği
// (v0.9.819).
//
// Sayfa bugüne dek SADECE tabloydu: her satır pencerenin TOPLAMINI
// gösteriyordu ve "ne zaman bozuldu" sorusunun cevabı ancak bir satırın
// sparkline'ına tıklayıp modalı açmakla — endpoint endpoint — bulunuyordu.
// Filo genelinde "trafik ne zaman düştü", "gecikme ne zaman tırmandı",
// "hata dalgası nerede" soruları hiç cevaplanamıyordu.
//
// TEK MV TARAMASI hem şeridi hem üç grafiği besler ve pencere KPI'ları
// serinin KENDİ sorgusundan gelir (WITH ROLLUP): şerit ile grafiklerin
// farklı sayı göstermesi imkânsız.
//
// NEDEN WITH ROLLUP: pencere p50/p95'i SAYILARIN TOPLAMI GİBİ
// hesaplanamaz — iki TDigest'in quantile'ı ne toplanır ne ortalanır. Kova
// p95'lerinin ortalaması hem yanlış hem sistematik olarak İYİMSER olurdu.
// ROLLUP satırı (t=0) tüm kovaların state'lerini TEK merge'de birleştirir,
// yani gerçek pencere quantile'ı — ve ikinci bir tarama ödemeden.
//
// TIER + IZGARA: endpointsSparkGrid, tablonun satır-içi sparkline'ları ile
// AYNI kararı veriyor (10s tier yalnız kısa VE genç pencerelerde; slot
// genişliği daima grenin tam katı). Bilinçli: grafik ile satır sparkline'ı
// aynı kaynağı aynı çözünürlükte okumazsa aynı ekranda iki farklı gerçek
// olurdu.

import (
	"context"
	"strconv"
	"time"
)

const (
	// endpointsSeriesLimit — kova satır tavanı. endpointsSparkGrid
	// zaten en çok SparklineBuckets (120) + 1 ROLLUP satırı üretiyor;
	// bu tavan tel-bayt güvenliği, sıcak yol değil.
	endpointsSeriesLimit = 500
	// endpointsSeriesSearchMax — serbest metin araması için tavan.
	// Cache anahtarı aramayı içerdiği için sınırsız metin = sınırsız
	// anahtar kardinalitesi demekti (v0.8.270 disiplini).
	endpointsSeriesSearchMax = 200
)

// EndpointsSeriesPoint — serinin bir kovası.
type EndpointsSeriesPoint struct {
	TimeS int64 `json:"timeS"` // kova başlangıcı, unix saniye
	// Calls / Errors HAM SAYAÇ; Rps ve ErrorRate türetilmiş. İkisi de
	// taşınıyor çünkü grafik oranı, tooltip ham sayıyı istiyor.
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	Rps       float64 `json:"rps"`
	ErrorRate float64 `json:"errorRate"` // yüzde
	P50Ms     float64 `json:"p50Ms"`
	P95Ms     float64 `json:"p95Ms"`
}

// EndpointsSeries — /api/endpoints/series zarfı.
type EndpointsSeries struct {
	SeriesWindow
	Points []EndpointsSeriesPoint `json:"points"`
	// SourceMV — okunan tier. Grafiğin altındaki kapsam notu bunu
	// yazıyor: 10s ile 1m arasındaki fark operatörün gördüğü
	// çözünürlüğü belirliyor ve tahmin ettirilmemeli.
	SourceMV string `json:"sourceMV"`

	// Pencere KPI'ları — hepsi ROLLUP satırından, yani grafiklerin
	// çizdiği kovaların TAM merge'ünden.
	Calls       uint64  `json:"calls"`
	Errors      uint64  `json:"errors"`
	CallsPerMin float64 `json:"callsPerMin"`
	// ErrorRate pencere TOPLAMLARINDAN: kova oranlarının ortalaması
	// sessiz bir dakikayı yoğun bir dakikayla eşit ağırlardı.
	ErrorRate float64 `json:"errorRate"`
	P50Ms     float64 `json:"p50Ms"`
	P95Ms     float64 `json:"p95Ms"`
	// PriorP95Ms — bir önceki EŞİT pencerenin p95'i (yalnız compare
	// istendiğinde). omitempty: 0 ms bir ölçüm değil, ölçüm YOKLUĞUDUR
	// ve frontend o zaman Δ çizmez.
	PriorP95Ms float64 `json:"priorP95Ms,omitempty"`

	// UnsupportedScope — bu kapsam bu uçtan CEVAPLANAMAZ ("env" /
	// "cluster" / "env + cluster"). Boş değilse Points da boştur.
	//
	// NEDEN BOŞ SERİ DEĞİL DE İLAN: spanmetrics MV'sinde ne deploy_env
	// ne cluster boyutu var (EndpointsQuery.forcesRaw), o yüzden tablo
	// bu filtrelerde ham span'lere düşüyor. Seriyi yine de çizseydik
	// FİLTRESİZ bir filo grafiğini, filtrelenmiş bir tablonun üstüne
	// koymuş olurduk — v0.9.313'te açıkça reddettiğimiz sessiz-kapsam
	// sınıfının ta kendisi. Sorgu HİÇ atılmaz: bu yol CH'ye dokunmaz.
	UnsupportedScope string `json:"unsupportedScope,omitempty"`
}

// EndpointsSeriesQuery — uç girdileri. Tablo ile AYNI filtreler
// (service/search/entry) taşınır ki şeritteki sayı ile listedeki
// satırlar aynı kümeyi anlatsın.
type EndpointsSeriesQuery struct {
	From, To time.Time
	Service  string
	Search   string
	Entry    EntryKind
	// Env / Cluster — cevaplanabilirlik KAPISI (UnsupportedScope).
	Env     string
	Cluster string
	// Compare — bir önceki eş pencerenin p95'i de okunsun mu? Ayrı ve
	// ÇOK küçük bir sorgu (GROUP BY yok, tek satır).
	Compare bool
}

// unsupportedScope — hangi boyut bu ucu cevaplanamaz kılıyor? SAF.
func (q EndpointsSeriesQuery) unsupportedScope() string {
	switch {
	case q.Env != "" && q.Cluster != "":
		return "env + cluster"
	case q.Env != "":
		return "env"
	case q.Cluster != "":
		return "cluster"
	}
	return ""
}

// endpointsSeriesEntrySQL — tablonun entry yüklemi ile BİREBİR AYNI
// (GetEndpointsMV'deki entryWhere). İki yer ayrışırsa grafik tablodan
// farklı bir popülasyonu çizer ve ikisi de "doğru" görünür.
// Döndürülen ikinci değer, aramanın filtreleyeceği DİMENSİYON kolonu.
func endpointsSeriesEntrySQL(entry EntryKind) (where, dimCol string) {
	if entry == EntryRPC {
		return " AND kind IN ('server', 'consumer') AND http_route = ''", "name"
	}
	return " AND kind NOT IN ('client', 'producer') AND http_route != ''", "http_route"
}

// endpointsSeriesFilterSQL — service + arama yüklemi ve bind args'ı,
// SQL'deki `?` sırasıyla birebir. Arama positionCaseInsensitive ile
// bağlanır, ILIKE ile DEĞİL: operatörün yazdığı `%` / `_` desen anlamı
// kazanıp sessizce farklı bir küme döndürmesin.
func endpointsSeriesFilterSQL(service, search, dimCol string) (string, []any) {
	sql := ""
	args := []any{}
	if service != "" {
		sql += " AND service_name = ?"
		args = append(args, service)
	}
	if search != "" {
		if len(search) > endpointsSeriesSearchMax {
			search = search[:endpointsSeriesSearchMax]
		}
		sql += " AND positionCaseInsensitive(" + dimCol + ", ?) > 0"
		args = append(args, search)
	}
	return sql, args
}

// endpointsSeriesRow — taranan ham satır (ROLLUP satırı dahil: t == 0).
// Ayrı struct, katlamanın SAF fonksiyon olabilmesi için.
type endpointsSeriesRow struct {
	t      int64
	calls  uint64
	errors uint64
	p50Ms  float64
	p95Ms  float64
}

// buildEndpointsSeries — ham satırları zarfa katlar. SAF.
//
// t == 0 satırı WITH ROLLUP'ın PENCERE TOPLAMIDIR, bir kova DEĞİL:
// noktalara girmez, KPI'ları doldurur. (Zaman-sınırlı WHERE yüzünden
// gerçek bir kova asla epoch'a düşemez.) ROLLUP satırı hiç gelmezse
// KPI sayaçları kovalardan toplanır — ama p50/p95 O ZAMAN DA
// TÜRETİLMEZ: quantile toplanamaz, ve iyimser bir ortalama basmaktansa
// alanı 0 bırakmak (frontend '—' çizer) dürüst olan.
func buildEndpointsSeries(
	rows []endpointsSeriesRow, bucketSec int64, windowSeconds float64,
	coveredFromNs, coveredToNs int64, sourceMV string, nowUnix int64,
) *EndpointsSeries {
	out := &EndpointsSeries{
		SeriesWindow: SeriesWindow{
			BucketSeconds: int(bucketSec),
			CoveredFromNs: coveredFromNs,
			CoveredToNs:   coveredToNs,
		},
		Points:   []EndpointsSeriesPoint{},
		SourceMV: sourceMV,
	}
	var haveRollup bool
	for _, r := range rows {
		if r.t == 0 {
			haveRollup = true
			out.Calls = r.calls
			out.Errors = r.errors
			out.P50Ms = r.p50Ms
			out.P95Ms = r.p95Ms
			continue
		}
		p := EndpointsSeriesPoint{
			TimeS:  r.t,
			Calls:  r.calls,
			Errors: r.errors,
			P50Ms:  r.p50Ms,
			P95Ms:  r.p95Ms,
		}
		if bucketSec > 0 {
			p.Rps = float64(r.calls) / float64(bucketSec)
		}
		if r.calls > 0 {
			p.ErrorRate = float64(r.errors) / float64(r.calls) * 100
		}
		out.Points = append(out.Points, p)
	}
	if !haveRollup {
		for _, p := range out.Points {
			out.Calls += p.Calls
			out.Errors += p.Errors
		}
	}
	if out.Calls > 0 {
		out.ErrorRate = float64(out.Errors) / float64(out.Calls) * 100
	}
	// Oran İSTENEN pencereye bölünür, kova sayısına değil: tablonun
	// Req/min kolonu da öyle hesaplanıyor (endpointsWindowMinutes) ve
	// ikisi ayrışırsa aynı sayfada iki farklı "istek hızı" olurdu.
	if windowSeconds > 0 {
		out.CallsPerMin = float64(out.Calls) / (windowSeconds / 60)
	}
	if n := len(out.Points); n > 0 {
		out.PartialLastBucket = seriesLastBucketPartial(
			out.Points[n-1].TimeS, bucketSec, coveredToNs/1e9, nowUnix)
	}
	return out
}

// GetEndpointsSeries — KPI şeridi + üç grafiğin kaynağı.
func (s *Store) GetEndpointsSeries(
	ctx context.Context, q EndpointsSeriesQuery,
) (*EndpointsSeries, error) {
	if q.From.IsZero() {
		q.From = time.Now().Add(-1 * time.Hour)
	}
	if q.To.IsZero() {
		q.To = time.Now()
	}
	// Cevaplanamaz kapsam → CH'ye HİÇ gitmeden ilan et.
	if scope := q.unsupportedScope(); scope != "" {
		return &EndpointsSeries{
			Points:           []EndpointsSeriesPoint{},
			UnsupportedScope: scope,
		}, nil
	}

	// Tablonun satır-içi sparkline'ları ile aynı hizalama ve aynı tier
	// kararı (GetEndpointsMV: dakika sınırı hem 60s hem 10s greni için
	// geçerli bir başlangıç).
	from := q.From.Truncate(time.Minute)
	windowSec := q.To.Unix() - from.Unix()
	if windowSec <= 0 {
		windowSec = 60
	}
	now := time.Now()
	sourceMV, bucketSec, _ := endpointsSparkGrid(windowSec, now.Unix()-from.Unix())

	entryWhere, dimCol := endpointsSeriesEntrySQL(q.Entry)
	filterSQL, filterArgs := endpointsSeriesFilterSQL(q.Service, q.Search, dimCol)

	step := strconv.FormatInt(bucketSec, 10)
	args := append([]any{from, q.To}, filterArgs...)
	sql := `
		SELECT toUnixTimestamp(toStartOfInterval(time_bucket, INTERVAL ` + step + ` SECOND)) AS t,
		       countMerge(calls_state)   AS c,
		       countIfMerge(error_state) AS e,
		       arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p95_ms
		FROM ` + s.spanmetricsSourceFor(sourceMV) + `
		WHERE time_bucket >= ? AND time_bucket <= ?` + entryWhere + filterSQL + `
		GROUP BY t WITH ROLLUP
		ORDER BY t
		LIMIT ` + strconv.Itoa(endpointsSeriesLimit) + `
		SETTINGS max_execution_time = 8`
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	var scanned []endpointsSeriesRow
	for rows.Next() {
		var r endpointsSeriesRow
		var p50, p95 *float64
		// toUnixTimestamp() UInt32 döndürür — sürücü *int64'e ÇEVİRMEZ
		// (v0.9.817'de messaging'in iki kardeşi tam bu yüzden hiç
		// çizilmiyordu).
		var t uint32
		if err := rows.Scan(&t, &r.calls, &r.errors, &p50, &p95); err != nil {
			rows.Close()
			return nil, err
		}
		r.t = int64(t)
		// v0.5.301 — NaN/Inf JSON'a çıkmadan temizlenir.
		r.p50Ms = safeF(p50)
		r.p95Ms = safeF(p95)
		scanned = append(scanned, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := buildEndpointsSeries(
		scanned, bucketSec, q.To.Sub(q.From).Seconds(),
		from.UnixNano(), q.To.UnixNano(), sourceMV, now.Unix())

	// Bir önceki EŞİT pencerenin p95'i — "Filo p95" karosunun Δ'sı.
	// AYRI ve küçük bir sorgu: GROUP BY yok, tek satır. Hata ÖLÜMCÜL
	// DEĞİL (Δ çizilmez, geri kalan her şey doğru).
	//
	// TIER PRIOR'IN KENDİ YAŞINA GÖRE seçilir: önceki pencere daha
	// eskidir ve 10s tier'ın 2 günlük TTL'inin dışına düşmüş olabilir.
	// Aynı tier'ı zorlamak sessizce BOŞ bir prior verirdi — yani "Δ yok"
	// diye okunan bir ölçüm kaybı.
	if q.Compare {
		dur := q.To.Sub(q.From)
		priorFrom := from.Add(-dur)
		priorMV, _, _ := endpointsSparkGrid(
			int64(dur.Seconds()), now.Unix()-priorFrom.Unix())
		pArgs := append([]any{priorFrom, from}, filterArgs...)
		var priorP95 *float64
		err := s.telemetryReadConn().QueryRow(ctx, `
			SELECT arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 3) / 1e6
			FROM `+s.spanmetricsSourceFor(priorMV)+`
			WHERE time_bucket >= ? AND time_bucket < ?`+entryWhere+filterSQL+`
			SETTINGS max_execution_time = 8`, pArgs...).Scan(&priorP95)
		if err == nil {
			out.PriorP95Ms = safeF(priorP95)
		}
	}
	return out, nil
}
