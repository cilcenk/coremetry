package chstore

// trace_raw_light.go — v0.10.245 uzun-aralıklı attribute araması (saf
// yardımcılar; CH'ye dokunan çağrılar repo.go GetTraces'te kalır).
//
// Operatör direktifi (2026-09-02): "herhangi bir attribute seçildiğinde
// operation name seçilmiş gibi hızlı ve uzun süreli trace'leri getirsin";
// "Dynatrace 30 günlük trace'i biraz zaman alsa da getiriyor" — uzun
// pencere YAVAŞ olabilir, BOŞ/HATA olamaz.
//
// Ölçülen darboğazlar (lokal 21 g veri, query_log, v0.10.241):
//   • 1. aşama GROUP BY trace_id tüm pencere: bellek ∝ trace sayısı
//     (prod 12 s → 241 3.73 GiB), süre ∝ satır; sabit 25 s tavan 30 g'de
//     159 → pencere yarılanıyor ("daraltıldı").
//   • Kök kontrolü trace_summary_5m'de `trace_id IN` + pencere: 30 g MV
//     taraması 3.07M satır / 236 MiB → 10 s tavanda 159.
//   • 2. aşama `trace_id IN (50)` + tam pencere: granül budaması yok
//     (bloom part-düzeyi), 7 g'de 875K satır okunuyor.
//   • Zaman sıralı probe HAVING alt sorgusunu TAM pencereyle bağlıyordu
//     (basamak 1 saat, alt sorgu 30 gün) → 159.
//
// Tasarım (Datadog/Honeycomb "span ara → trace listele"):
//   1. Akışkan 1. aşama: süre/zaman sıralamasında GROUP BY YOK —
//      `ORDER BY duration DESC LIMIT K×4` (heap, bellek düz), Go'da trace
//      tekilleştirme. Sıralama anahtarı "en uzun eşleşen span" (ham yol
//      zaten span-yerel: WHERE'i geçen span'lerden hesaplıyordu).
//      HAVING gereken şekiller (RequireServices, servissiz rootOnly,
//      spans/status sıralaması) GROUP BY varyantında kalır — o da min/max
//      zamanı döndürür.
//   2. Tavan pencereyle ölçeklenir: ≤24 s 25 s · ≤7 g 60 s · üstü 120 s.
//      159/241'de yarılama yine devrede (son çare).
//   3. Kök kontrolü: `time_bucket IN (kovalar) AND trace_id IN (idler)` —
//      MV ORDER BY (time_bucket, trace_id): ilk PK kolonu kova kümesiyle
//      budanır; kova −2..+1 (kök span adayın span'inden önce olabilir).
//      Ölçüldü (lokal 7 g, 200 aday): demet IN 3307 ms vs iki IN 834 ms
//      (aynı satır sayısı — demet karşılaştırması satır başına pahalı);
//      eski pencereli tarama 7.7 s. K > 1500'de eski tarama (derin sayfa).
//   4. 2. aşama: `PREWHERE trace_id IN (idler)` + `optimize_move_to_prewhere
//      = 0` — id süzgeci ÖNCE, ağır kolonlar yalnız kalan satırlar için.
//      Ölçüldü (lokal 7 g, 50 id): WHERE IN 2794 ms → PREWHERE 326 ms;
//      trace-başı zaman aralığı birleşimi (OR) her varyantta DAHA YAVAŞ
//      (2670-2799 ms, satır başına 50 dal) — bilinçli olarak YOK.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// stage1Cand — 1. aşamanın döndürdüğü aday: id + eşleşen span zaman
// sınırları (ns). Akışkan varyantta t0 == t1 (tek span).
type stage1Cand struct {
	id     string
	t0, t1 int64
}

// traceRawStage1MaxExec — pencereyle ölçeklenen 1. aşama tavanı (saniye).
func traceRawStage1MaxExec(from, to time.Time) int {
	win := to.Sub(from)
	switch {
	case win <= 24*time.Hour:
		return 25
	case win <= 7*24*time.Hour:
		return 60
	default:
		return 120
	}
}

// traceRawStage1Streaming — GROUP BY'sız akışkan 1. aşama uygun mu:
// süre/zaman sıralaması ve HAVING yok.
func traceRawStage1Streaming(sort, lightHavingSQL string) bool {
	if lightHavingSQL != "" {
		return false
	}
	return sort == "duration" || sort == "" || sort == "time"
}

// traceRawStage1OverFetch — akışkan varyantta span-düzeyi LIMIT: K trace
// için K×4 span (çok-span'lı trace'ler slot yer), tavan 4×traceStage2MaxIDs.
func traceRawStage1OverFetch(k int) int {
	n := k * 4
	if n > 4*traceStage2MaxIDs {
		n = 4 * traceStage2MaxIDs
	}
	if n < 1 {
		n = 1
	}
	return n
}

// traceRawStage1StreamSQL — akışkan 1. aşama. Döndürür: trace_id, t (ns).
func traceRawStage1StreamSQL(whereSQL, sort, order string, maxExec int) string {
	key := "duration"
	if sort == "" || sort == "time" {
		key = "time"
	}
	if order != "ASC" {
		order = "DESC"
	}
	return `
		SELECT trace_id, toUnixTimestamp64Nano(time) AS t
		FROM spans ` + whereSQL + `
		ORDER BY ` + key + ` ` + order + `, trace_id
		LIMIT ?
		SETTINGS
		  max_execution_time = ` + fmt.Sprint(maxExec) + `,
		  distributed_product_mode = 'global'`
}

// traceRawStage1GroupSQL — GROUP BY varyantı (HAVING'li şekiller ve
// spans/status sıralaması). Döndürür: trace_id, t0, t1 (ns).
func traceRawStage1GroupSQL(whereSQL, havingSQL, sort, order string, maxExec int) (string, bool) {
	var sortExpr string
	switch sort {
	case "duration":
		sortExpr = "(max(toUnixTimestamp64Nano(time) + duration) - toUnixTimestamp64Nano(min(time)))"
	case "spans":
		sortExpr = "count()"
	case "status":
		sortExpr = "max(if(status_code = 'error', 1, 0))"
	case "", "time":
		sortExpr = "min(time)"
	case "service", "operation":
		// v0.10.499 — string anahtar 1. aşamada sıralanmaz: en yeni N aday
		// (recency, her zaman DESC); 2. aşama root_svc/root_name sıralar.
		sortExpr = "min(time)"
		order = "DESC"
	default:
		return "", false
	}
	if order != "ASC" {
		order = "DESC"
	}
	return `
		SELECT trace_id, toUnixTimestamp64Nano(min(time)) AS t0, toUnixTimestamp64Nano(max(time)) AS t1
		FROM spans ` + whereSQL + `
		GROUP BY trace_id` + havingSQL + `
		ORDER BY ` + sortExpr + ` ` + order + `, trace_id
		LIMIT ? OFFSET ?
		SETTINGS
		  max_execution_time = ` + fmt.Sprint(maxExec) + `,
		  distributed_product_mode = 'global',
		  ` + tracesSpillSettings, true
}

// dedupeStage1 — akışkan varyant: span satırlarını trace'e indirger (ilk
// görülen = en iyi sıradaki span), k ile keser.
func dedupeStage1(rows []stage1Cand, k int) []stage1Cand {
	seen := make(map[string]bool, len(rows))
	out := make([]stage1Cand, 0, min(k, len(rows)))
	for _, r := range rows {
		if seen[r.id] {
			continue
		}
		seen[r.id] = true
		out = append(out, r)
		if len(out) >= k {
			break
		}
	}
	return out
}

// Kök kontrolü nokta okuma sınırları.
const (
	rootCheckTupleMaxIDs  = 1500 // üstünde pencereli tarama (derin sayfa)
	rootCheckBucketBefore = 2    // kova: adayın span'i kökten sonra olabilir
	rootCheckBucketAfter  = 1
	rootCheckBucketSec    = 300
)

// rootCheckArgs — kova (unix sn, 5 dk tabanı, −2..+1) kümesi + id listesi.
// SAF; kovalar tekil ve sıralı (IN kümesi küçük kalsın).
func rootCheckArgs(cands []stage1Cand) (buckets []any, ids []any) {
	seen := map[int64]bool{}
	var bs []int64
	ids = make([]any, 0, len(cands))
	for _, c := range cands {
		base := (c.t0 / 1e9 / rootCheckBucketSec) * rootCheckBucketSec
		for k := -rootCheckBucketBefore; k <= rootCheckBucketAfter; k++ {
			b := base + int64(k)*rootCheckBucketSec
			if !seen[b] {
				seen[b] = true
				bs = append(bs, b)
			}
		}
		ids = append(ids, c.id)
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i] < bs[j] })
	buckets = make([]any, len(bs))
	for i, b := range bs {
		buckets[i] = b
	}
	return buckets, ids
}

// rootCheckSQL — nBuckets kova × nIDs id için iki-IN sorgusu.
func rootCheckSQL(nBuckets, nIDs int) string {
	bucketPh := make([]string, nBuckets)
	for i := range bucketPh {
		bucketPh[i] = "toDateTime(?, 'UTC')"
	}
	return `
			SELECT trace_id FROM trace_summary_5m
			WHERE time_bucket IN (` + strings.Join(bucketPh, ", ") + `)
			  AND trace_id IN (` + chPlaceholders(nIDs) + `)
			GROUP BY trace_id
			HAVING argMaxIfMerge(root_service_state) != ''
			SETTINGS max_execution_time = 10`
}

// stage2PrewhereSQL — 2. aşama ön-süzgeci: id listesi PREWHERE'de,
// pencere/servis/filtre WHERE'de (lwc). SAF.
func stage2PrewhereSQL(n int) string {
	return "PREWHERE trace_id IN (" + chPlaceholders(n) + ") "
}

// stage2Settings — otomatik PREWHERE taşımasını kapat: aksi hâlde CH
// ucuz kolonlu WHERE koşullarını id süzgecinin ÖNÜNE alıp satır başına
// değerlendirir (ölçüldü: 1140 ms vs 326 ms).
const stage2Settings = "optimize_move_to_prewhere = 0"

// probeHavingArgs — zaman sıralı probe: rootOnly alt sorgusunun ZAMAN
// sınırı basamak penceresiyle bağlanır (tam pencereyle değil). Alt sorgu
// HAVING'in ilk iki argümanıdır (from, to unix sn). SAF.
func probeHavingArgs(havingArgs []any, rootSub bool, from time.Time) []any {
	out := append([]any{}, havingArgs...)
	if rootSub && len(out) >= 2 {
		out[0] = from.Unix()
	}
	return out
}

// probeFetchLimit — zaman sıralı probe: kök son-filtresi satır düşürür,
// K×2 çekilir ki kesinlik kontrolü (traceRawProbePage) yine K satır görsün.
func probeFetchLimit(k int, rootPostFilter bool) int {
	if !rootPostFilter {
		return k
	}
	return k * 2
}
