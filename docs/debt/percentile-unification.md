# Percentile birleştirme borcu (envanter)

**Durum:** BELGE. Bu dosya KOD DEĞİŞTİRMEZ — v0.9.774'te Service Overview'un
"Response time · metrik" paneli ikinci implementasyondan çıkarılırken açığa
çıkan ayrışmaların envanteri. Bir sonraki dokunuşta nereye bakılacağı burada.

**Neden yazıldı:** v0.9.774 operatör-bildirimli bir hatayı kapattı — prod'da
panel boştu. Kök neden bir kod hatası değil, İKİ percentile motorunun
varlığıydı: aynı sayfadaki "Response time · P99" KAROSU (tDigest yolu)
çalışırken hemen altındaki GRAFİK (bucket yolu) boş dönüyordu. İki motor
farklı veri şekli istiyor, farklı filtre uyguluyor ve farklı pencere
semantiği taşıyor; hangi yüzeyin hangisine düştüğü kod okunmadan
görülemiyor.

---

## 1) Bucket yolu (histogram_quantile)

| Parça | Yer |
|---|---|
| Ham okuma + kova toplama | `internal/chstore/metrichist.go` — `QueryMetricHistogram`, `dominantBounds`, `cumulativeToDelta` |
| Quantile çekirdeği | `internal/chstore/metrichist_percentile.go` — `queryHistogramQuantiles`, `histWindowK`, `slidingSumCounts`, `percentileFromBuckets` |
| Rollup kademesi | `internal/chstore/metric_rollup_hist_read.go` — `tryRollupHistogramQuantiles`, `hist_counts` katlaması (0003 şeması) |
| Giriş uçları | `QueryMetricHistogramPercentile` (agg-string: p50/p95/p99), `QueryMetricHistogramQuantile` (keyfi q) |
| Tüketiciler | Explore `agg=pXX` (`metricquery.go:143`), PromQL `histogram_quantile` (`internal/promql/eval.go`) |

**Desteklediği şekil:** `bucket_bounds` + `bucket_counts` dolu
`metric_points` satırları (ya da `rollup_metrics`'in `hist_counts` kolonu).
Explicit ve exponential histogram aynı kolonlardan okunur.

**Kırılma noktası (v0.9.774'ün prod vakası):** satırlar `bucket_counts`
taşıyıp `bucket_bounds` taşımıyorsa `dominantBounds` → `nil` döner ve
`QueryMetricHistogram` boş `HistogramSeries` verir → percentile hesaplanamaz,
seri BOŞ. Sessiz: hata değil, veri yokluğu gibi görünür. (v0.9.761'in
`HistogramLatencyDiag`'ı tam bunu teşhis etmek için yazılmıştı; panele özel
yol silinince o da silindi — teşhis, yeniden ihtiyaç duyulursa bu satırın
tarifiyle geri yazılabilir.)

## 2) tDigest yolu (quantilesTDigestMerge)

| Parça | Yer |
|---|---|
| Dar rollup hızlı yolu | `internal/chstore/rollup_fastpath.go:121-169` — `narrowRollupSQL`, `quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state)` (`rollup_spans_narrow_*`) |
| Ham span yolu | `internal/chstore/spanmetric.go:1132` — `quantilesTDigest(...)` tek geçiş |
| Operasyon MV'si | `internal/chstore/spanmetric.go:1539-1543` — `quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)` |
| Tüketiciler | Overview KPI karoları, RED grafikleri, Endpoints/Operations tabloları |

**Kısıt:** oran kümesi SABİT — 0.5 / 0.95 / 0.99. Keyfi bir q (p999, p90)
bu yoldan SORULAMAZ; SQL'e gömülü ve `arrayElement(..., 1|2|3)` ile
okunuyor. Yeni bir oran isteyen yüzey ya SQL'i değiştirmek ya bucket yoluna
düşmek zorunda — bugün ikisi de yapılıyor ve seçim yüzey başına gömülü.

---

## Üç somut ayrışma

1. **Filtre uygulayıcısı.** Bucket yolu `metrichist.go:73`'te `ApplyFilters`
   (span-şekilli) çağırıyor; kardeş metrik sorgusu (`metricquery.go`)
   `ApplyMetricFilters` (metrik-bilinçli; `metric_points`'te olmayan
   span kolonlarını attr dizi aramasına yönlendirir — v0.8.381).
   Sonuç: AYNI filtre çipiyle iki yol farklı satır kümesi görebiliyor.
   Explore'da çalışan bir filtre histogram yolunda 0 satır seçebilir.

2. **`RateWindowSec`.** Bucket yolu pencereyi `histWindowK` +
   `slidingSumCounts` ile UYGULUYOR (v0.9.765). tDigest'in dar-rollup
   yolu `narrowRollupSQL`'de kendi `winK` kayan penceresini kuruyor ama
   metrik tarafındaki `RateWindowSec` oraya HİÇ gitmiyor. İki panel aynı
   "3 dakikalık pencere" ayarıyla farklı yumuşatma uyguluyor.

3. **`dominantBounds`'ta iki ağırlık ölçüsü.** Kanonik kova seti "en çok
   toplam ağırlık" ile seçiliyor; ağırlık ham yolda NOKTA SAYISI
   (`boundsWeight.Weight = len(pts)`), rollup yolunda satır katlamasından
   gelen GÖZLEM sayısı. Aynı metriğin iki farklı SDK sürümü farklı
   bounds yayınladığında hangi setin kazandığı, sorgunun hangi kademeye
   düştüğüne bağlı — yani ZAMANLA (TTL sınırı geçilince) değişebiliyor.

---

## Birleştirme önerisi (iki sürümlük yol)

**Sorgu motorları AYRI kalır** — biri kova toplar, diğeri tDigest state
birleştirir; ikisi de doğru ve ikisinin de yeri var. Birleşen ŞEKİL
katmanı:

```go
type QuantileRequest struct {
    Name        string
    Service     string
    Filters     []FilterExpr
    GroupBy     []string
    Quantiles   []float64
    From, To    time.Time
    StepSeconds int
    RateWindowSec int
}

type QuantileMeta struct {
    Source string // "bucket" | "tdigest" | "bucket-rollup" | "tdigest-rollup"
    Mode   string // "exact" | "interpolated" | "fixed-ratios"
}

func (s *Store) QueryQuantiles(ctx context.Context, r QuantileRequest) (
    map[float64][]SpanMetricSeries, QuantileMeta, error)
```

`QuantileMeta` UI notuna basılabilir: panel hangi motordan geldiğini
SÖYLER, bugünkü gibi kod okunarak çıkarılmaz.

**Sürüm 1 — bayt-bayt çekirdek çıkarımı.** Yalnız yönlendirme: mevcut iki
motor değişmeden `QueryQuantiles` altına girer, çağıranlar tek imzaya
taşınır. Davranış değişimi SIFIR olmalı (mevcut testler kırmızıya
dönmemeli).

**Sürüm 2 — ayrışmalar tek tek, her biri regresyon testli.** Sırayla:
(a) filtre uygulayıcısını `ApplyMetricFilters`'ta birleştir,
(b) `RateWindowSec`'i tDigest yoluna geçir,
(c) `dominantBounds` ağırlığını tek ölçüye indir. Üçü tek commit'te
gitmemeli: her biri farklı bir yüzeyin sayısını değiştirebilir.

---

## ~~Ayrıca: endpoint kırılımlı METRİK rollup tier'ı YOK~~ — KAPANDI (v0.9.777)

**Durum:** `migrations/0008_rollup_metrics_route.sql` + okuyucusu
(`internal/chstore/metric_rollup_route_read.go`) gemide; kurulum
sihirbazında ayrı hedef ("route"). Aşağıdaki teşhis tarihsel kayıt.

Gerçekleşen kapsam, aday metninden İKİ SAPMAYLA:
1. Yüzdelik YOK. 0008 bucket taşımıyor — gerekçe DDL başlığında (prod'da
   bounds kararsızlığı çözülmeden route boyutuyla çarpmak "p95 var" deyip
   yanlış sayı göstermek olurdu). Kapsam avg · sum · min · max.
2. Boyut `service_key` (= `coalesce(k8s.deployment.name, service_name)`),
   düz `service_name` değil — prod kimlik tuzağı (channel_code v0.9.626).

Bu başlığın ASIL gerekçesi de netleşti: mesele hız değil RETANSİYON.
`metric_points` TTL 7 gün, yani 30 günlük route sorgusu yavaş değil
CEVAPSIZDI; 0008 aynı sayıyı 14g/90g/13ay taşıyor.


`metricRollupPlan` (`metric_rollup_read.go:64`) filtreli VEYA gruplu her
sorguyu reddediyor, ve 0003 şeması attr taşımıyor. Yani "metrik, route
kırılımlı" — v0.9.774'te Overview'un RT panelinin tam olarak yaptığı sorgu —
HER ZAMAN ham `metric_points` okuyor. 3 saatlik pencerede ölçülen maliyet
kabul edilebilir (aşağıya bkz. v0.9.774 ölçümü), ama 7g/30g pencerede
değil.

**0008 adayı (ARTIK GEMİDE — yukarı bkz.):** endpoint kırılımlı metrik rollup tier'ı (route boyutu
`LowCardinality(String)` kolon olarak). `/clickhouse-schema` kapısından
geçmeden açılmaz — MV/ORDER BY/partition kararları oraya ait.

---

## Ayrıca: `avgOrNull(value)`'nun histogram metrikteki matematiksel sınırı

v0.9.774'te Overview'un RT paneli Explore'un `agg=avg` yoluna bağlandı.
O yol `metricquery.go:232` üzerinden `avgOrNull(value)` çalıştırıyor.
Histogram enstrümanında `value` kolonu **per-export ORTALAMA**dır, yani
`avgOrNull(value)` = *ortalamaların ortalaması*: export'lar farklı gözlem
sayısı taşıdığında ağırlıksız ve GERÇEK ortalama değil.

Matematiksel doğrusu `sum(sum_value) / sum(count)` türetmesi (histogram
satırlarının toplam ve sayım kolonlarından). Bugün YAPILMIYOR — bilinçli
borç, iki gerekçeyle:

1. Panel bu haliyle Explore ile **TUTARLI**: aynı metrik, aynı agg, aynı
   sayı. İkisi birlikte yanlışsa bu görünür bir tutarsızlık üretmiyor;
   yalnız Explore'u düzeltmek panelle Explore'u ayrıştırırdı.
2. Düzeltmenin yeri panel değil `metricAggToSQL` — yani Explore'un,
   PromQL'in ve dashboard panellerinin hepsini birden etkiler.

**Düzeltilirse:** `avg` için instrument'a bakıp histogram'da
`sumOrNull(sum_value) / sumOrNull(count)` seçilmeli (sum/gauge'da bugünkü
ifade doğru). Regresyon testi: eşit-gözlemli ve eşitsiz-gözlemli iki
export serisi — ikincisi bugünkü ifadeyle sapar, doğrusuyla sapmaz.
