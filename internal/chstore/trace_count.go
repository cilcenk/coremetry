// v0.9.638 — /traces "Toplamı göster" listeyi MV'den DÜŞÜRÜYORDU.
//
// `count=exact` tek başına countModeAllowsMV'yi kapatıyor ve liste ham
// spans yoluna iniyordu: çift ceza — hem pahalı bir DISTINCT hem
// 22.575:1 oranlı bir liste taraması (docs/perf/traces-plan.md "D3").
//
// Sayım liste isteğinden TAMAMEN çıkarıldı. Liste SQL'i bayt bayt aynı
// kaldığı için "toplamı göster" artık listeyi MV'de BIRAKIYOR — kabul
// kriteri kod okumasıyla değil YAPISAL olarak sağlanıyor.
//
// ÜÇ TASARIM ÜRETİLİP YARGILANDI; reddedilenlerin ölümcül kusuru:
//
//   - aşama-1'in LIMIT'ini yükseltmek: bütçe TARANAN değil EŞLEŞEN
//     satıra konuyor. %0,1 hata oranında `error_count_state > 0 …
//     LIMIT 30000` otuz milyon satır tarar.
//   - sayımı listeye gömmek: her offset değişiminde yeniden ödenir ve
//     "kesin" toplam Pager'ı listenin ULAŞAMAYACAĞI sayfalara açar.
//
// SAYI ASLA SAYFALAMA SINIRI DEĞİL. Pager.tsx lastPage/atEnd'i
// total'dan türetiyordu; UI artık total'ı Pager'a GEÇMİYOR, gezinme
// hasMore üzerinde kalıyor. Bu, tavanın ulaşılabilir sınırla
// eşleşmesi zorunluluğunu da kaldırıyor — ilk tasarımda tavanı 5.000'e
// çekmeyi önermiştim, kısıtı KABUL ederek; doğru hamle kısıtı KOPARMAK.
package chstore

import (
	"context"
	"fmt"
	"strings"
)

// traceCountCap — sayımın durduğu yer. Sorguya LIMIT cap+1 gider, yani
// cap+1 dönmesi "en az cap kadar var" demek.
//
// 10.000: ölçüldü, sorgu PENCEREDEN BAĞIMSIZ (filtresiz 7g ve 30g'de
// aynı satır sayısı) çünkü DISTINCT + LIMIT erken duruyor. Tavanı
// büyütmenin maliyeti doğrusal ve küçük; 10.000 operatöre "çok var"
// demekten fazlasını söylüyor.
const traceCountCap = 10_000

// TraceCount — sayım sonucu.
//
// Reason != "" ise SAYI YOK ve sebebi var. Bu bilinçli: bazı şekiller
// (süre filtresi, servis+post-agg) MV'de ucuza sayılamıyor ve
// "yanlış sayı, sayı yokluğundan kötüdür" ilkesinin devamı olarak
// PAHALI sayı da dürüst bir retten kötüdür.
type TraceCount struct {
	Value   uint64 `json:"value"`
	AtLeast bool   `json:"atLeast"`          // tavana değdi → "10.000+"
	Reason  string `json:"reason,omitempty"` // dolu ise Value anlamsız
}

// Ret sebepleri — UI bunları metne çeviriyor.
const (
	traceCountReasonRawPath  = "raw-path-filter" // filtre MV'yi zaten kapatıyor
	traceCountReasonDuration = "duration-filter" // minMs/maxMs: ucuz şekil yok
	traceCountReasonSvcAgg   = "service+filter"  // servis + post-agg kombinasyonu
)

// traceCountPlan — hangi kaynaktan, hangi yüklemlerle sayılacak.
// SAF (tablo testli).
//
// Dalları getTracesFromMV'nin dallarıyla AYNI koşullardan türüyor ve
// eligibility'yi AYNALAMIYOR, tracesMVEligible'ı ÇAĞIRIYOR.
//
// getTracesFromMV'de BEŞ yol var — plan dokümanı üç, ilk okumam dört
// saymıştı; beşincisi stage2NeedsSafetySlice (aşama-1 hiç kimlik
// üretmediğinde devreye giren recency slice). Kaçırılan bir dal, o
// filtre kombinasyonunda SESSİZCE yanlış sayı demekti.
func traceCountPlan(f TraceFilter) (source string, preds []string, args []any, reason string) {
	if !tracesMVEligible(f) {
		return "", nil, nil, traceCountReasonRawPath
	}
	// Süre filtresi: trace_summary_5m'de süre ancak iki merge state'in
	// farkı olarak çıkıyor, yani her satır için finalize gerekiyor —
	// LIMIT erken duramaz ve maliyet pencereye bağlanır.
	if f.MinMs > 0 || f.MaxMs > 0 {
		return "", nil, nil, traceCountReasonDuration
	}
	// Servis + post-agg filtre: liste bunu iki tabloyu bağlayan bir
	// alt-sorguyla karşılıyor (serviceSubquery). Aynısını sayımda
	// kurmak DISTINCT'in erken durmasını öldürüyor.
	if f.Service != "" && tracePostAggFiltered(f) {
		return "", nil, nil, traceCountReasonSvcAgg
	}

	if f.Service != "" {
		// Liste de bu evreni kullanıyor (trace_service_index_5m aşama-1).
		return "trace_service_index_5m",
			[]string{"service_name = ?", "time_bucket >= ?", "time_bucket <= ?"},
			[]any{f.Service, f.From, f.To}, ""
	}

	preds = []string{"time_bucket >= ?", "time_bucket <= ?"}
	args = []any{f.From, f.To}
	if f.HasError {
		preds = append(preds, "finalizeAggregation(error_count_state) > 0")
	}
	if f.RootOnly {
		preds = append(preds, "finalizeAggregation(root_service_state) != ''")
	}
	return "trace_summary_5m", preds, args, ""
}

// buildTraceCountSQL — tavanlı DISTINCT. SAF (tablo testli).
//
// GROUP BY DEĞİL, DISTINCT — ve bu ölçülmüş bir karar:
// trace_summary_5m ORDER BY (time_bucket, trace_id), yani trace_id
// sıralama anahtarının ÖNEKİ DEĞİL. GROUP BY tüm pencereyi okutuyor
// (v0.9.633'te EXPLAIN PIPELINE ile ölçüldü: aggregation-in-order
// devreye girmiyor). DISTINCT + LIMIT ise erken durabiliyor.
//
// ORDER BY YASAK: maliyeti pencereye bağlıyor (ölçüldü, sıralı şekil
// 6s'te 126k, 7g'de 913k satır). Bunun bedeli, tavan vurunca okunan alt
// kümenin "en yeni" DEĞİL keyfi olması — bu yüzden bir zaman tabanı
// İDDİA ETMİYORUZ. "≥ 10.000" her koşulda doğru.
//
// max_threads=1 + max_block_size=8192: erken durmayı keskinleştiriyor
// (ölçüldü, 363k → 50k satır). Çok iş parçacığı her biri kendi bloğunu
// okuduğu için tavanı aşıyor.
func buildTraceCountSQL(source string, preds []string) string {
	return fmt.Sprintf(`SELECT count() FROM (
		SELECT DISTINCT trace_id
		FROM %s
		WHERE %s
		LIMIT %d
	) SETTINGS max_execution_time = 10, max_threads = 1, max_block_size = 8192`,
		source, strings.Join(preds, " AND "), traceCountCap+1)
}

// CountTracesCapped — listeyle AYNI evreni tavana kadar sayar.
func (s *Store) CountTracesCapped(ctx context.Context, f TraceFilter) (TraceCount, error) {
	source, preds, args, reason := traceCountPlan(f)
	if reason != "" {
		return TraceCount{Reason: reason}, nil
	}
	sql := buildTraceCountSQL(source, preds)
	// count() UInt64 döner — int'e taramak derlenir, testleri geçer,
	// yalnız canlıda patlar (v0.9.595 dersi).
	var n uint64
	if err := s.telemetryReadConn().QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return TraceCount{}, err
	}
	if n > traceCountCap {
		return TraceCount{Value: traceCountCap, AtLeast: true}, nil
	}
	return TraceCount{Value: n}, nil
}
