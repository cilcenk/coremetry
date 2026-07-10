# Exemplar Extraction — Audit (Faz 1: Metric → Trace Pivoting)

*Tarih: 2026-07-10 · Kod durumu: v0.8.430 · Yöntem: 5 bağımsız okuma ajanı
(ingest / şema / API+frontend / test envanteri) + tamlık eleştirmeni; her
bulgu dosya:satır ile doğrulandı.*

## Yönetici özeti

Brief'in çekirdek varsayımı — "exemplar'lar ingestion'da hiç çıkarılmıyor" —
**bayat**: bu, v0.8.328 öncesini anlatan `docs/pivot-audit.md:60`'ın bulgusudur
ve o audit'in motive ettiği iş **v0.8.328–335 + 348'de uçtan uca şipariş
edilmiştir**. Bugün exemplar'lar dört taşıyıcı data-point tipinden de
çıkarılıyor (`internal/otlp/convert.go:275,283,308,320`), policy gate +
backpressure ile batch'leniyor, ClickHouse'daki adanmış `exemplars` tablosuna
yazılıyor (`store.go:1197-1210`), `GET /api/exemplars` üzerinden okunuyor
(`internal/api/pivot.go:50` — istenen ince-handler/registrar deseniyle) ve
Explore chart'larında ◆ olarak çizilip trace detail'e link'leniyor
(`TimeSeriesPanel.tsx:423-455`). `internal/otlp` test kapsamı sıfır değil:
4 test dosyası, 8'i exemplar'a özgü 12+ test. Bu doküman dolayısıyla bir
"yeşil alan tasarımı" değil, **doğrulama + kalan boşlukların envanteri**dir;
fazlandırma yalnızca gerçek boşlukları kapsar (en önemlisi: gruplu/çok-serili
chart'larda OTLP ◆ yok, çünkü `series_fingerprint` hiçbir metric read
path'inden client'a dönmüyor).

## 1. Mevcut durum bulguları

### 1.1 Ingestion akışı (`internal/otlp`)

| Adım | Yer |
|---|---|
| Receive | gRPC `metricsGRPC.Export` `grpc.go:222` · HTTP `handleMetrics` `http.go:297` |
| Convert | `ConvertMetrics(req) ([]*MetricPoint, []*ExemplarRow)` `convert.go:208` → `convertMetric` `convert.go:250` |
| Enqueue | points `ing.addMetric` (backpressure, v0.8.345) · exemplar'lar `ing.addExemplar` `grpc.go:237-239`, `http.go:315-317` |
| Batch | `consumer.NewSized("exemplars", …, store.InsertExemplars)` `main.go:366-381` (BatchSize 10k, Buffer 500k, Flush 2s, Workers 8, ByteBudget 512MB — `config.go:376`) |
| Write | `InsertExemplars` `exemplar_otlp.go:59-85`, `asyncInsertCtx` sarmalı `repo.go:59-65` |

**Data-point tipi kapsamı** (`convert.go`):

| Tip | Exemplar okunuyor mu? | Satır |
|---|---|---|
| Gauge | ✅ `appendExemplars` | :270-276 |
| Sum | ✅ | :277-284 |
| Histogram | ✅ (+ `ExplicitBounds`/`BucketCounts` korunur :303-306) | :285-309 |
| ExponentialHistogram | ✅ exemplar OK; ⚠ bucket yapısı + min/max KAYIP (yalnız avg/count/sum, literal `0,0` :317-318) | :310-321 |
| Summary | – proto'da Exemplars alanı yok; doğru şekilde atlanıyor | :322-332 |

Brief'in "öncelik Histogram + Sum" sorusu fiilen aşılmış: dördü de destekli;
maliyeti üretici sınırlıyor (SDK'ler seri başına export başına ~1 exemplar
tutar — tasarım notu `convert.go:337-340`).

**Politika gate'i:** trace-context'siz exemplar default DÜŞÜRÜLÜR
(`exemplars.require_trace_context`, default true — `config.go:392`;
gate `http.go:185-195`); sayaçlar `exemplarsIngested`/`exemplarsDroppedNoTrace`.
Exemplar buffer taşması batch'i REDDETMEZ (datapoint'ler zaten kabul edildi;
retry çift yazardı — `grpc.go:232-236`).

**Kimlik:** `otlp.SeriesFingerprint` (`fingerprint.go:52`) — xxhash64(metric ·
sıralı dp attr'ları · `service.name`+`service.instance.id`); hem
`metric_points.series_fingerprint` hem `exemplars.series_fingerprint`'e
yazılır; byte-layout testlerle sabitlenmiş ("değiştirmek her saklanan
exemplar'ı öksüz bırakır" — `fingerprint_test.go:11-13`).

**Ayrım:** kod tabanında İKİ exemplar sistemi var, karıştırılmamalı —
(1) span-TÜREVLİ exemplar state'leri (spanmetrics MV'lerindeki
`argMaxState(trace_id, duration)`, `store.go:2158-2159`; resolver ◆'ları
buradan gelir) ve (2) gerçek OTLP metric exemplar'ları (`exemplars` tablosu).
`exemplar_otlp.go:3-9` bunu "additive truth, not a replacement" diye belgeler.

### 1.2 Brief'in kaynağı

`docs/pivot-audit.md` (satır 42, 60) — v0.8.328 ÖNCESİ durumu anlatan,
implementasyonu motive etmiş tarihi audit. İç satır referansları da bayat
(ör. `:44` argMax state'leri `store.go:1846-1883`'te gösterir; bugün
`store.go:2158-2159`). Bu doküman onu tarihsel kaynak olarak kaydeder.

## 2. ClickHouse şeması (mevcut; öneri değil, doğrulama)

Brief'in "yeni tablo mu, kolon mu" sorusu v0.8.328'de **adanmış tablo** ile
cevaplanmış — doğru karar, gerekçesi sorgu deseninden:

- Pivot okuması "şu serilerin şu penceredeki exemplar'ları" →
  `ORDER BY (series_fingerprint, timestamp)` ile **saf PK taraması**
  (`ExemplarsForSeries`). `metric_points`'e kolon eklemek, exemplar'sız
  milyarlarca satırı aynı taramadan geçirir ve sparse Array kolonlarıyla
  her metric insert'ini şişirirdi.
- Ayrı tablo, ayrı TTL'e izin verir: **exemplar TTL'i `retention.spans`'a
  biner, MetricsDays'e değil** — "trace'inden uzun yaşayan exemplar ölü
  tıktır" (`store.go:1193-1196`; enforcement `retention_enforce.go:50`,
  purge listesi `purge.go:25-30`).

```sql
-- store.go:1197-1210 (verbatim)
CREATE TABLE IF NOT EXISTS exemplars (
    series_fingerprint UInt64,
    metric_name   LowCardinality(String),
    service_name  LowCardinality(String),
    timestamp     DateTime64(9) CODEC(Delta, ZSTD(3)),
    value         Float64      CODEC(Gorilla, ZSTD(3)),
    trace_id      String       DEFAULT '' CODEC(ZSTD(3)),  -- bilerek LC değil
    span_id       String       DEFAULT '' CODEC(ZSTD(3)),
    filtered_attributes Map(LowCardinality(String), String)
) ENGINE = MergeTree()
PARTITION BY toDate(timestamp)
ORDER BY (series_fingerprint, timestamp)
TTL toDate(timestamp) + INTERVAL <SpansDays> DAY
```

**Distributed-mode uyumu** (yeni tablo/kolonların uyması gereken kalıp —
ikisi de yapılmış):
- Tablo: `highVolumeTables` üyeliği (`cluster.go:889-892`) + shard policy
  `cityHash64(series_fingerprint)` — bir serinin tüm satırları aynı shard'da,
  pivot shard-local (`cluster.go:355-361`); DDL `adaptDDL`'den akar.
- Kolon: `metric_points.series_fingerprint` ALTER'ı
  `tableIsExternalDistributed` guard'lı (`store.go:1933-1948`) + boot probe
  `hasSeriesFpCol` + koşullu named INSERT (`repo.go:172-230`) — v0.8.185/186
  prod kırılmalarının dersi; regression testi `exemplar_otlp_test.go:20-65`.
- ⚠ Boşluk: `chmigrate` (tek-node→cluster tarihsel kopya) tablo setinde
  `exemplars` yok (`migrate.go:34`) — migration yapan operatör exemplar
  geçmişini taşıyamaz (kabul edilebilir: TTL kısa; nota değer).

**Kardinalite / hacim:** satır ≈ 200-250 B buffer'da
(`approx_bytes.go:33,77-85`), diskte codec'lerle çok daha küçük. Üretim
modeli: satır/gün ≈ aktif_seri × export/gün × gate-geçiş-oranı; ör. 100k
exemplar taşıyan seri × 10s export ≈ **864M satır/gün tavan** (SDK'lerin
seri başına 1 exemplar tutması pratikte bunu ciddi düşürür). **Ingest
tarafında bilinçli olarak örnekleme/tavan yok** (`convert.go:337-340`);
okuma tarafı clamp'li (default 100, max 1000 — `exemplar_otlp.go:39-53`).
Fingerprint çakışması: xxhash64, 10M eşzamanlı seri için P≈2.7×10⁻⁶ —
ihmal edilebilir; çakışma yalnız ◆ yanlış-atfı yapar, veri bozmaz.

## 3. Query API + frontend (mevcut sözleşme)

- **`GET /api/exemplars`** (v0.8.330, `pivot.go:50`) — kısıt 1'e zaten uygun:
  route + ince handler `internal/api/pivot.go`'da, `registerPivotRoutes`
  registrar deseniyle api.go'ya tek satır maliyet (`api.go:595`); sorgu
  katmanı `internal/chstore/exemplar_otlp.go`. İki mod:
  `?fingerprints=fp1,fp2` (PK scan) ve `?metric=&service=` fallback.
  Yanıt: `{items:[{ts, value, traceId, spanId, attrs?}]}` (`pivot.go:64-70`).
  Cache: hash-all-inputs anahtar + dakika-bucket'lı pencere, 30s
  (`pivot_key_test.go` ile sabit).
- **Frontend zinciri:** `api.exemplars` (`api.ts:411-415`, staleTime ≥ 30s) →
  `useExploreQueries.ts:151-167` (yalnız metric-source + tek servis +
  splitBy boş — fetch-on-need, ES/CH maliyet disiplini) →
  `PanelStack.otlpMarkersFor` (◆ değeri seri üzerine oturtulur) →
  `TimeSeriesPanel` çizim sözleşmesi: `{time(ns), value(seri ölçeğinde),
  traceId, kind:'otlp'}` — mor ◆, tıklama `onExemplarClick(traceId)` →
  CorrelationContextDrawer.
- **MCP paritesi:** `get_exemplar_traces` (`mcptools/pivots.go:171-205`);
  clamp'i bilerek farklı (default 20 / max 100) — hiçbir yerde
  belgelenmemişti, artık burada.
- **`/api/v1/` sapması:** brief'in orijinali `/api/v1/exemplars` istemişti;
  `pivot-audit.md:265-268` kendi önerisiyle versiyonsuz `/api/exemplars`e
  karar verdi — drift değil, kayıtlı karar.

**Gerçek boşluklar:**
1. **`?fingerprints=` modunun frontend'de kullanıcısı yok** — kök neden
   sunucuda: hiçbir metric read path'i `series_fingerprint` SELECT edip
   döndürmüyor (`metricquery.go`/`metricresolve.go`'da sıfır geçiş). Seri →
   fingerprint eşlemesi client'a inmediği için **gruplu / çok-serili
   chart'larda OTLP ◆ hiç yok** (gate `useExploreQueries.ts:153-155` +
   `PanelStack.tsx:123` tek-seri guard'ı).
2. **AdminStats UI exemplar sayaçlarını çizmiyor** — backend
   `SystemStats.ExemplarIngest` dolu (`sysstats.go:25-36`), ama
   `AdminStats.tsx:206-230` ingest kartları spans/logs/metrics ile bitiyor.
3. `NO_RECORDED_VALUE` (DataPointFlags) staleness işaretleri hiç işlenmiyor —
   staleness noktası `metric_points`'e gerçek 0 değeri olarak girer
   (exemplar-dışı ama convert katmanı boşluğu).

## 4. Test planı

**Mevcut** (envanter): `internal/otlp` — `convert_test.go` (4 tip için
exemplar'lı fixture'lar, Summary-kapsam-dışı, traceless/zero-trace-id/
int-value/zero-timestamp kenarları, Ingester gate ×2), `fingerprint_test.go`
(byte-layout pinleme), `grpc_backpressure_test.go` (exemplar drop'u batch'i
reddetmez), `convert_bench_test.go`. `chstore` — `exemplar_otlp_test.go`
(INSERT kolon hizası ×2 durum + clamp). `api` — `pivot_key_test.go` (cache
key, dakika bucket, fp digest, isHex32). Altyapı gerçeği: **testcontainers /
integration tag / canlı CH yok** — ev stili saf table-driven + SQL-shape
testleri; öneriler buna uyar.

**Eksikler (öneri):**
- `ExemplarsForSeries`/`ExemplarsForMetric` (exemplar_otlp.go:93,121) için
  SQL-shape testleri (LIMIT, max_execution_time, `series_fingerprint IN`,
  zaman bound'ları — `messaging_e2e_test.go` şablonu).
- `NO_RECORDED_VALUE` davranış testi (bugünkü davranışı pinle, sonra karar).
- Exp-histogram min/max kaybının pinlenmesi (bilinçli sınır olarak).

## 5. Fazlandırma (yalnız kalan boşluklar)

| Faz | Kapsam | Dosyalar | Risk | Rollback |
|---|---|---|---|---|
| **A — Görünürlük + test borcu** | AdminStats exemplar ingest kartı; exemplar read-SQL shape testleri; MCP/HTTP clamp farkının kod yorumuna işlenmesi | `AdminStats.tsx`, `chstore/exemplar_otlp_test.go`, `mcptools/pivots.go` (yorum) | Düşük — salt UI + test | Önemsiz (UI kartı geri al) |
| **B — Gruplu chart'lara ◆** | Metric read path'lerine `series_fingerprint` döndürtme (`metricquery.go`/`metricresolve.go` + `hasSeriesFpCol` fallback), tip + client eşlemesi, `useExploreQueries`'in `?fingerprints=` moduna geçişi, `PanelStack` çok-seri atfı | `chstore/metricquery.go`, `metricresolve.go`, `lib/types.ts`, `lib/api.ts`, `useExploreQueries.ts`, `PanelStack.tsx` | Orta — sorgu şekli değişir; distributed fallback (fp=0) yolu korunmalı | Kolay — client gate'i eski moda döndürmek yeter |
| **C — Ingest tavanı (karar bekliyor)** | Seri×pencere başına exemplar cap (yaz-tarafı sampling) — bugün bilinçli yok | `internal/otlp/http.go` (gate genişletme) + sayaç | Düşük-Orta | Config default'u kapalı tutmak |
| **D — Exp-histogram sadakati (exemplar-dışı, ayrı iş)** | scale/bucket yapısı + min/max persist | convert + şema | Orta — şema işi | — |

Fazlar bağımsız merge edilebilir; B, A'ya bağımlı değil.

## 6. Açık sorular

1. **Faz B'yi istiyor muyuz?** Gruplu chart'larda OTLP ◆ değeri sana ne kadar
   önemli? (Span-türevli ◆'lar gruplu chart'larda zaten çalışıyor; B yalnız
   catalogue-metric chart'larını zenginleştirir.)
2. **Faz C (ingest tavanı):** 864M satır/gün teorik tavan seni endişelendiriyor
   mu, yoksa üretici-sınırlı model + kısa TTL yeterli mi? (Mevcut duruş: tavan
   yok, bilinçli.)
3. `docs/pivot-audit.md`'ye "TARİHSEL — v0.8.328 öncesi durum" başlık notu
   düşülsün mü? (Bu brief'in yanlış öncülle gelmesinin kökü o dokümandı.)
4. `chmigrate`'e `exemplars` eklensin mi, yoksa "kısa TTL'li veri taşınmaz"
   diye belgelensin mi?
