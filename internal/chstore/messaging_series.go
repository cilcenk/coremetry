package chstore

// Messaging zaman serisi — v0.9.814.
//
// /messaging sayfası bugüne dek SADECE tabloydu: her satır pencerenin
// TOPLAMINI gösteriyordu ve "ne zaman bozuldu" sorusunun cevabı yalnız
// satırı açıp drawer'a bakmakla bulunuyordu. Bu uç, sayfanın üstündeki
// KPI şeridi + üç grafiği besler: filo genelinde üretim/tüketim dengesi,
// gecikme ve hata oranı TEK bakışta.
//
// İKİ MV OKUMASI, İKİSİ DE ÖNCEDEN TOPLANMIŞ:
//
//   A) messaging_summary_5m — kova başına toplam/hata + p50/p95. KABA
//      MV bilinçli tercih: caller MV ile AYNI kaynaktan (spans WHERE
//      msg_system != '') türediği için toplamları birebir aynı, ama
//      (service_name, host_name, kind) grenini taşımadığı için birleşecek
//      state sayısı kat kat az.
//
//   B) messaging_caller_summary_5m — üretici/tüketici ayrımı. `kind`
//      boyutu YALNIZ bu MV'de var (kaba MV onu topluyor), o yüzden
//      ayrım için ikinci okuma şart.
//
// NEDEN E2E SERİSİ YOK — ÖLÇÜLDÜ, RAPORLANDI (v0.9.814):
// messaging_e2e.go zinciri span_links × spans × spans üçlü join'i ve her
// iki spans tarafını `LIMIT 50000` ile sınırlıyor. O tavan DESTINATION
// BAŞINA uygulandığında gerçek bir sınır (drawer böyle kullanıyor); sayfa
// geneline açıldığında ise sıralamasız, keyfi bir 50k örneklemi olur.
// Lokal ölçüm (query_log medyanı, n=5): sayfa-geneli E2E 38ms / 7.66 MiB,
// MV serisi 6ms / 1.11 MiB. Asıl mesele hız değil DÜRÜSTLÜK: prod
// hacminde (1B span/gün) messaging consumer span'leri 1 saatlik pencerede
// 50k'yı rahat aşar ve grafik, filonun tamamıymış gibi görünen keyfi bir
// örneklemi çizerdi — bu sayfada v0.9.813'te düzelttiğimiz yalan sınıfının
// ta kendisi. Üçüncü grafik bu yüzden ÖLÇÜLEN span gecikmesi (aynı TDigest
// state'i tablonun P50/P95 kolonlarını da besliyor, yani grafik ile tablo
// birbirini doğruluyor). E2E, tam olduğu yerde kalıyor: destination
// drawer'ı.

import (
	"context"
	"strconv"
	"time"
)

const (
	// msgSeriesBucketSeconds — MV'nin kova genişliği. Seri buradan
	// türediği için istemci adım seçmez; sunucu ne varsa onu söyler.
	msgSeriesBucketSeconds = 300
	// msgSeriesLimit — kova satır tavanı. 5000 ≈ 17 günden fazla 5-dk
	// kovası; sayfanın en geniş aralığının çok ötesinde.
	msgSeriesLimit = 5000
	// msgSeriesQueryMax — q parametresinin taşıyabileceği en uzun metin.
	// Cache anahtarı q'yu içerdiği için sınırsız q = sınırsız anahtar
	// kardinalitesi demekti.
	msgSeriesQueryMax = 200
)

// MessagingSeriesPoint — serinin bir 5-dk kovası.
type MessagingSeriesPoint struct {
	TimeS int64 `json:"timeS"` // kova başlangıcı, unix saniye
	// SpanCount TÜM messaging span'lerini sayar; Produce/Consume ise
	// yalnız producer/consumer kind'larını. Broker chatter'ı (client /
	// internal) toplama girer ama ayrıma girmez — tablodaki Calls ile
	// Produce+Consume'un neden eşit olmayabileceğinin sebebi bu.
	SpanCount    uint64  `json:"spanCount"`
	ErrorCount   uint64  `json:"errorCount"`
	ProduceCount uint64  `json:"produceCount"`
	ConsumeCount uint64  `json:"consumeCount"`
	ErrorRate    float64 `json:"errorRate"` // yüzde
	P50Ms        float64 `json:"p50Ms"`
	P95Ms        float64 `json:"p95Ms"`
}

// MessagingSeries — /api/messaging/series zarfı. KPI alanları serinin
// KENDİ satırlarından türetilir: şerit ile grafiklerin farklı sayı
// göstermesi imkânsız olsun diye (iki ayrı sorgu olsaydı mümkündü).
type MessagingSeries struct {
	Points        []MessagingSeriesPoint `json:"points"`
	BucketSeconds int                    `json:"bucketSeconds"`
	// Pencere KPI'ları. Oranlar İSTENEN pencereye bölünür (kova sayısına
	// değil): tablodaki Produce/min kolonu da öyle hesaplanıyor, ikisi
	// ayrışırsa aynı sayfada iki farklı "üretim hızı" olurdu.
	ProducePerMin float64 `json:"producePerMin"`
	ConsumePerMin float64 `json:"consumePerMin"`
	// ErrorRate pencere TOPLAMLARINDAN: kova oranlarının ortalaması
	// düşük trafikli kovaları yüksek trafikli olanlarla eşit ağırlıklar
	// ve sessiz bir dakika bütün pencereyi kırmızıya boyayabilirdi.
	ErrorRate  float64 `json:"errorRate"`
	SpanCount  uint64  `json:"spanCount"`
	ErrorCount uint64  `json:"errorCount"`
}

// msgSeriesTotalsRow / msgSeriesKindRow — taranan ham satırlar. Ayrı
// struct'lar, katlamanın SAF fonksiyon olabilmesi için (tablo-güdümlü
// test v0.9.814).
type msgSeriesTotalsRow struct {
	t          int64
	spanCount  uint64
	errorCount uint64
	p50Ms      float64
	p95Ms      float64
}

type msgSeriesKindRow struct {
	t     int64
	kind  string
	count uint64
}

// buildMessagingSeries — iki tarama sonucunu tek seriye katlar. SAF.
//
// Kind satırları toplam satırlarına t ile bağlanır; toplamda KARŞILIĞI
// OLMAYAN bir kind kovası ATILIR. Bu bilinçli: iki MV aynı kaynaktan
// türüyor, yani eşleşmeyen bir kova ancak iki okuma arasına düşen bir
// insert'ten gelebilir ve o kovanın toplamı/gecikmesi yok — yarım bir
// nokta çizmektense hiç çizmemek dürüst.
func buildMessagingSeries(
	totals []msgSeriesTotalsRow, kinds []msgSeriesKindRow, windowSeconds float64,
) *MessagingSeries {
	out := &MessagingSeries{
		Points:        []MessagingSeriesPoint{},
		BucketSeconds: msgSeriesBucketSeconds,
	}
	idxByT := make(map[int64]int, len(totals))
	for _, r := range totals {
		p := MessagingSeriesPoint{
			TimeS:      r.t,
			SpanCount:  r.spanCount,
			ErrorCount: r.errorCount,
			P50Ms:      r.p50Ms,
			P95Ms:      r.p95Ms,
		}
		if r.spanCount > 0 {
			p.ErrorRate = float64(r.errorCount) / float64(r.spanCount) * 100
		}
		out.Points = append(out.Points, p)
		idxByT[r.t] = len(out.Points) - 1
		out.SpanCount += r.spanCount
		out.ErrorCount += r.errorCount
	}
	var produce, consume uint64
	for _, k := range kinds {
		i, ok := idxByT[k.t]
		if !ok {
			continue
		}
		switch k.kind {
		case "producer":
			out.Points[i].ProduceCount += k.count
			produce += k.count
		case "consumer":
			out.Points[i].ConsumeCount += k.count
			consume += k.count
		}
	}
	if out.SpanCount > 0 {
		out.ErrorRate = float64(out.ErrorCount) / float64(out.SpanCount) * 100
	}
	if windowSeconds > 0 {
		mins := windowSeconds / 60
		out.ProducePerMin = float64(produce) / mins
		out.ConsumePerMin = float64(consume) / mins
	}
	return out
}

// msgSeriesFilterSQL — system + serbest arama yüklemi. Arama
// positionCaseInsensitive ile bağlanır, ILIKE ile DEĞİL: operatörün
// yazdığı `%` ya da `_` desen anlamı kazanıp sessizce farklı bir küme
// döndürmesin. Dönen args sırası SQL'deki `?` sırasıyla birebir.
func msgSeriesFilterSQL(system, q string) (string, []any) {
	sql := ""
	args := []any{}
	if system != "" {
		sql += " AND msg_system = ?"
		args = append(args, system)
	}
	if q != "" {
		if len(q) > msgSeriesQueryMax {
			q = q[:msgSeriesQueryMax]
		}
		// Tablodaki istemci filtresinin KİMLİK alanlarıyla aynı üçlü.
		// (İstemci ayrıca çağıran ADLARINDA da arıyor; caller adı MV'nin
		// bu grenine girmediği için sunucu tarafı onu kapsayamaz —
		// panel başlığındaki kapsam notu bunu söylüyor.)
		sql += ` AND (positionCaseInsensitive(destination, ?) > 0
		          OR positionCaseInsensitive(msg_system, ?) > 0
		          OR positionCaseInsensitive(cluster, ?) > 0)`
		args = append(args, q, q, q)
	}
	return sql, args
}

// GetMessagingSeries — KPI şeridi + üç grafiğin kaynağı.
func (s *Store) GetMessagingSeries(
	ctx context.Context, from, to time.Time, system, q string,
) (*MessagingSeries, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// v0.9.813 ile aynı hizalama: kova sorgusu hizalanmamış from ile
	// baştaki kısmi kovayı eler ve grafik, tablodan farklı bir pencere
	// çizerdi.
	bucketStart := alignBucketStart(from)
	windowSeconds := to.Sub(from).Seconds()

	filter, filterArgs := msgSeriesFilterSQL(system, q)

	// A) Kova başına toplam + gecikme — KABA MV.
	totalsArgs := append([]any{bucketStart, to}, filterArgs...)
	tRows, err := s.telemetryReadConn().Query(ctx, `
		SELECT toUnixTimestamp(time_bucket) AS t,
		       countMerge(span_count_state)  AS c,
		       countMerge(error_count_state) AS e,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95)(duration_q_state), 2) / 1e6 AS p95_ms
		FROM messaging_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?`+filter+`
		GROUP BY t
		ORDER BY t
		LIMIT `+strconv.Itoa(msgSeriesLimit)+`
		SETTINGS max_execution_time = 8`, totalsArgs...)
	if err != nil {
		return nil, err
	}
	var totals []msgSeriesTotalsRow
	for tRows.Next() {
		var r msgSeriesTotalsRow
		var p50, p95 *float64
		if err := tRows.Scan(&r.t, &r.spanCount, &r.errorCount, &p50, &p95); err != nil {
			tRows.Close()
			return nil, err
		}
		// v0.5.301 sınıfı — NaN/Inf JSON'a çıkmadan temizlenir.
		r.p50Ms = safeF(p50)
		r.p95Ms = safeF(p95)
		totals = append(totals, r)
	}
	tRows.Close()
	if err := tRows.Err(); err != nil {
		return nil, err
	}

	// B) Kova başına üretici/tüketici ayrımı — caller MV. Hata ÖLÜMCÜL
	// DEĞİL: ayrım olmadan da hata/gecikme grafikleri doğru, sadece
	// Produce vs Consume paneli boş kalır.
	kindArgs := append([]any{bucketStart, to}, filterArgs...)
	var kinds []msgSeriesKindRow
	kRows, err := s.telemetryReadConn().Query(ctx, `
		SELECT toUnixTimestamp(time_bucket) AS t,
		       kind,
		       countMerge(span_count_state) AS c
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND kind IN ('producer', 'consumer')`+filter+`
		GROUP BY t, kind
		ORDER BY t
		LIMIT `+strconv.Itoa(msgSeriesLimit*2)+`
		SETTINGS max_execution_time = 8`, kindArgs...)
	if err == nil {
		for kRows.Next() {
			var r msgSeriesKindRow
			if err := kRows.Scan(&r.t, &r.kind, &r.count); err != nil {
				continue
			}
			kinds = append(kinds, r)
		}
		kRows.Close()
	}

	return buildMessagingSeries(totals, kinds, windowSeconds), nil
}
