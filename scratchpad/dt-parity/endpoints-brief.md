# /endpoints — DT-parite mockup brief'i

Mockup: `scratchpad/dt-parity/endpoints-mockup.html` (tek dosya, dark+light, statik SVG).
Kaynak sürüm: v0.9.256. **Hiçbir ürün dosyası değiştirilmedi.**

Kural: ana tablonun düzeni, kolon sırası, sıralaması ve satır etkileşimi **aynen kalır**
(v0.9.61 Operations redesign ve v0.8.428 kart-feed reddi geçerli). Altı yüzeyin hepsi
**ek**: yeni kolon (varsayılan gizli), yeni sekme, yeni drawer bölümü, satır-içi kısayol.

Ortak ship kuralları (her yüzey için geçerli, tekrar etmiyorum):
`lib/types.ts` tip → `lib/api.ts` istemci → `s.serveCached` (anahtar TÜM girdileri FNV ile
hash'ler, `len()` değil — v0.5.187) → `auth.RequireAnyRole` okuma kapısı → loading/error/empty
→ `npx tsc --noEmit` + `go build ./...` + `go test ./...` → saf yardımcılar için tablo-güdümlü
regresyon testi.

---

## N1 — Giriş-noktası sekmeleri: `HTTP` | `RPC & Messaging`

**Ne gösteriyor.** Tablo kabuğunun ÜSTÜNE bir `.tab-strip`. `HTTP` sekmesi bugünkü tabloyla
bit-bit aynı. İkinci sekme `(service_name, name, kind)` anahtarıyla aynı kolon setini
(Calls / Errors / Err rate / Req/min / P50 / P95 / P99 / Trend) gRPC server ve Kafka consumer
giriş noktaları için gösterir. Bugün bu satırlar sayfada **hiç yok** ve alt not bunu söylemiyor
— operatör "giriş noktalarımın hepsi bu" diye eksik bir gerçek okuyor.

**Veri.** `spanmetrics_1m` (`internal/chstore/store.go`, MV tanımı ~2387-2403) ORDER BY'ı
zaten `(service_name, name, kind, status_code, http_route, time_bucket)`. Yani `name` ve `kind`
dim'leri hazır, sadece projeksiyona hiç girmiyor. MV yolu bugün sert `http_route != ''`
filtresi koyuyor (`internal/chstore/endpoints.go` ~415). İkinci sekme aynı sorgu, iki farklı
WHERE: `kind IN ('server','consumer') AND http_route = ''`. Kuyruk tarafı zenginleştirmesi
istenirse `messaging_summary_5m` / `messaging_caller_summary_5m` hazır.
**Yeni MV / yeni kolon GEREKMİYOR.**

**API.** Yeni uç yok. `/api/endpoints`'e `entry=http|rpc` parametresi (varsayılan `http`,
mevcut davranış). Yanıt `EndpointRow`; `path` yerine `name` + yeni `kind` alanı (`?:`).

**Efor.** ~1 gün (backend WHERE + projeksiyon dalı, FE sekme + kolon eşlemesi + URL state).

**Riskler.**
- `name` kardinalitesi: kötü enstrümante edilmiş serviste span adı ID taşıyabilir (`GET /orders/8421`).
  Sunucu-taraflı `LIMIT` + `endpointsOrderBy` whitelist'i zaten var; "group by shape"in
  `opSigWrap` dönüşümü `name`'e de uygulanmalı yoksa ikinci sekme uzun kuyruğa boğulur.
- `cluster`/`env` seçiliyse MV devre dışı (`forcesRaw`, endpoints.go:159) — ham yolda karşılığı
  `rpc_system`/`rpc_method` + `messaging_*` attr'ları; ya ham dal da yazılır ya sekme o modda
  "cluster/env filtresiyle RPC sekmesi MV'siz" uyarısıyla kapatılır. Sessizce boş dönmesin.
- Sekme URL'e yazılmalı (`?entry=rpc`, `replace:true`) — tek-yönlü okuma tekrar eden hata sınıfı
  (v0.8.253/256/265/267).

---

## N2 — Üç yeni kolon: `P90` · `Spread (p99/p50)` · `vs Baseline`

**Ne gösteriyor.** Üçü de **varsayılan gizli**, ColumnToggle'dan açılır (`?cols=` sözleşmesi
zaten var, `endpointCols.ts`). Görünen varsayılan tablo bugünküyle birebir aynı kalır.
`P90` eksik percentile'ı kapatır; `Spread` "herkese mi yavaş, yoksa kuyruk mu ağır" sorusunu
tek bakışta çözer; `vs Baseline` p99'un aynı saat/gün-sınıfındaki 7 günlük medyana göre
z-skorunu rozetler (drawer paneli N5).

**Veri.**
- **P90 bedava:** MV `quantilesTDigestState(0.5, 0.9, 0.95, 0.99)` üretiyor (store.go ~2399 ve
  10s tier'ı ~2417); `GetEndpointsMV` yalnız `arrayElement` 1/3/4'ü okuyor (endpoints.go ~456-458).
  **index 2 hiç okunmuyor.** Yeni tarama yok.
- **Spread:** saf istemci hesabı, `p99Ms / p50Ms`. Sıfır maliyet.
- **vs Baseline:** dönen top-N `(service, route)` kümesi için sınırlı IN-list ile `spanmetrics_1m`
  üzerinde "aynı saat + aynı gün-sınıfı, önceki 7 gün" p99 medyanı. MV TTL'i 30 gün, 7 gün geriye
  güvenli.

**API.** `/api/endpoints` yanıtına `p90Ms?: number`. Baseline ayrı uç: **yeni**
`GET /api/endpoints/baseline` (`from,to,service?,routes[]hash,env?,cluster?`) → satır başına
`baselineP99Ms`, `z`, `samples`. Ayrı uç olması şart: liste okumasının p99 bütçesini (15s
`max_execution_time`) baseline taramasıyla paylaştırmak hot endpoint'i yavaşlatır.

**Efor.** P90 + Spread ~1 saat. Baseline kolonu ~1 gün (N5 ile ortak backend).

**Riskler.**
- **Percentile-özdeşlik tuzağı:** ham yol (`cluster`/`env` açıkken) bugün `(0.5, 0.95, 0.99)`
  üretiyor. 0.9 eklenmezse P90 kolonu ham modda ya boş kalır ya **kaydırılmış index** okur —
  sessiz yanlış sayı. Unit-mixing disiplini (`feedback-unit-mixing-needs-both-branches`):
  MV yolu + ham yol + "group by shape" açık/kapalı, DÖRT kombinasyon da test edilir.
- Baseline cache anahtarı route kümesini **sıralı + FNV digest** ile hash'lemeli
  (`fmt.Sprintf("…:%x", fnvDigest(sortedSlice(routes)))`); `n=%d len(routes)` çapraz zehirler.
- Düşük trafikli route'ta z-skoru gürültü: slot başına minimum örnek (ör. ≥100 çağrı) altında
  rozet `—` kalmalı, uydurma σ basılmamalı.
- Deploy günü / incident günü baseline'ı kirletir: medyan (ortalama değil) bunu büyük ölçüde
  emer, notta söylenir.

---

## N3 — Satır-içi exemplar kısayolları: `⚡ en yavaş` · `✖ en kötü hata`

**Ne gösteriyor.** Sağa sabitlenmiş `Traces` kolonundaki mevcut `view →` linkinin yanına iki
mikro-link. Bugün `Traces` hücresi ARAMA tabanlı bir `/traces` filtresine gidiyor
(`tracesLink`, Endpoints.tsx:897) — operatör listeyi tekrar tarayıp yavaş olanı kendisi buluyor.
Oysa o (service, route, pencere) için en yavaş ve en kötü hata `trace_id`'si MV'de hazır.

**Veri.** `spanmetrics_1m.slow_exemplar_state` / `error_exemplar_state` (store.go ~2400-2401).
Bunları route-kapsamlı çözen `EndpointExemplars` **zaten gemide** (`endpoints_detail.go` ~354-380)
ama yalnız drawer kullanıyor. Liste okuması aynı MV'yi zaten tarıyor: `argMaxMerge` /
`argMaxIfMerge` iki ek aggregate state, **yeni tarama yok**.

**API.** Yeni uç yok. `/api/endpoints` yanıtına `slowTraceId?: string`,
`errorTraceId?: string`. Link `/trace?id=`.

**Efor.** ~2 saat.

**Riskler.**
- Exemplar state'leri **forward-only** (MV oluşturulduktan sonra yazılanlar). Boş string =
  "pencerede exemplar yok" → link **render edilmez**, `—` bile basılmaz (yumuşak degrade,
  drawer'daki mevcut davranışın aynısı).
- `spanmetrics_1s` tier'ı `http_route`'u DÜŞÜRÜYOR; `endpointsSparkGrid` (endpoints.go:347) kısa
  pencerede 10s'e düşüyor — 10s tier'ında state'ler var, sorun yok; ama tier seçimi 1s'e
  kayarsa exemplar route-kapsamlı çözülemez. Tier seçimini exemplar okuması da bilmeli.
- Ham yol (`cluster`/`env`) exemplar state taşımaz → o modda linkler gizlenir.
- MV TTL'i 30 gün, `spans` retention'ı daha kısa olabilir: link TTL'lenmiş trace'e gidebilir.
  `/trace` sayfası "bulunamadı" halini zaten yönetiyor; tooltip'te pencere hatırlatılır.

---

## N4 — Drawer'a yeni bölüm: "Where the time goes" (`Downstream` | `Callers`)

**Ne gösteriyor.** Latency dağılımının ALTINA, iki `.tab-strip` sekmeli tek bölüm.
`Downstream`: bu route'un çağırdığı servis/DB satırları — çağrı/istek, avg, p99, err%, ve
**süre payı** bar'ı (`self` dahil). `Callers`: bu route'u kim çağırıyor — caller servis +
operasyon, çağrı, pay, err%, p99.
Bugün en yakın yüzey `DependencyStrip` (Endpoints.tsx, `DependencyStrip` fonksiyonu).

> **v0.9.257 güncellemesi — bu paragrafın yarısı artık geçersiz.** Brief v0.9.256'da yazıldı ve
> şeridin "sabit son 1 saat / sayfanın range'ini yok sayıyor / pencere-kör önbellek" kusurlarını
> N4'ün gerekçesi olarak sayıyordu. Üçü de v0.9.257'de düzeltildi: şerit artık sayfa range'ini
> (24 sa'e sınırlı) kullanıyor, önbellek `${service}@${since}` ile anahtarlı, ve etiketler
> sayımların ≤100 **en büyük** trace'lik yanlı örneklemden geldiğini söylüyor.
> `+N` linki de kırık değil: `/topology` emekli ama `App.tsx:118` query string'i koruyan bir
> redirect tutuyor, yani link `/service-map?focus=` olarak iniyor — yalnız fazladan bir sıçrama
> (doğrudan `/service-map`'e çevirmek 1 satırlık temizlik, kendi sürümünü hak etmiyor; N4 ya da
> #1 dilimiyle birlikte gider).

Geriye N4'ün ASIL gerekçesi kalıyor ve o hâlâ tamamen açık: şerit **servis** seviyesinde —
route seviyesinde değil — ve **süre payı taşımıyor**. Yani "checkout p99 900ms — bunun 600ms'i
nerede" sorusu bu sayfada hâlâ cevapsız. Şerit "bu endpoint'in servisi postgres + redis +
payments-api'ye dokunuyor" der; N4 "bu route'un 900ms'inin 600ms'i postgres'te" demeli.

**Veri.** Hiçbir MV route→downstream taşımıyor: `topology_op_edges_5m` span-ADI keyli ve yalnız
`calls` (duration/error yok), `service_callers_5m` route dim'siz, `db_*_summary_5m` service_name
keyli. Uygulanabilir yol **yeni sorgu**, `neighbors.go:42-87` deseninin route'a daraltılmışı:
1. `spans` PK prefix'i (`service_name` + `time`) + `http_route` ile ≤200 route-kapsamlı
   `trace_id` örnekle;
2. tek sınırlı `WHERE trace_id IN (…) AND time BETWEEN ? AND ?` geçişiyle
   `(service_name, db_system, peer_service, parent_id)` topla; downstream tarafı çocuk
   kenarlardan, caller tarafı parent kenarlardan çıkar — **iki sekme tek örnekleme**.

MV alternatifi (`WriteTopologyOpBucket`'a route dim'i eklemek, topology.go ~810): 180s exec +
grace_hash + 4GB join maliyeti ve route×downstream kardinalitesi — **önerilmiyor.**

**API.** **Yeni uç**: `GET /api/endpoints/downstream` (`service, path, sig?, from, to, env?, cluster?`)
→ `{ downstream: [...], callers: [...], sampledFrom, samplingNote }`. Tek payload, iki sekme.

**Efor.** ~1 gün (yarım gün downstream + ~2 saat callers aynı örneklemeden + FE).

**Riskler.**
- **Maliyet.** İki ham `spans` sorgusu. Zorunlu: her ikisinde zaman-sınırlı WHERE
  (`neighbors.go` v0.9.231 dersi: ikinci sorguda zaman yüklemi yoksa retention'daki TÜM günlük
  partition'larda bloom-filter analizi çalışır), `LIMIT`, `SETTINGS max_execution_time` (10s) +
  `heavyScanSpill`, ve dağıtık CH'de `shardSkipSetting()`.
- **Örnekleme yanlılığı.** `neighbors.go` `ORDER BY count() DESC` ile "şişman" trace'lere
  kayıyor. Latency sorusu için sıralama süreye göre olmalı (yavaş kuyruğu temsil etsin) ya da
  karma (yarı en-yavaş + yarı rastgele). UI **"örnek tabanlı (N trace)"** demek zorunda —
  Drain templating'de olduğu gibi dürüst etiket.
- Cache: 60s `serveCached`, anahtar `(service, route, sig, from/to snap, env, cluster)`.
  DependencyStrip'in 24 saatlik cache'i bu bölüme taşınmaz — range'e duyarlı olmak bütün mesele.
- `self` payı = giriş span süresi − çocuk span süreleri toplamı; paralel çocuklarda negatife
  düşebilir → 0'a clamp + tooltip'te "paralel çağrılar örtüşüyor" notu.

---

## N5 — Drawer'a yeni panel: `Baseline` (7 gün aynı-slot medyan bandı)

**Ne gösteriyor.** Bugünkü p99 serisi + aynı saat/gün-sınıfı için 7 günlük medyan çizgisi +
p25–p75 bandı; bandın dışına taşan bölüm `--err` ile boyanır. Mevcut deploy çizgileri
(`EventMarkers`) aynı eksende. Bugün tek karşılaştırma manuel `compare=prior` (hemen önceki eşit
pencere) — pazartesi 09:00'da p99'un 400ms olması normal mi, 3× sapma mı ayırt edilemiyor.
Anomali dedektörü route'u hiç görmüyor (`internal/anomaly/anomaly.go` ~522-543 ve ~635-676,
ikisi de `GROUP BY service_name`).

**Veri.** `spanmetrics_1m`, 30 gün TTL, (service, route, dakika) granülü. Tek sorgu:
7 günlük aralık + `toHour(time_bucket)`/gün-sınıfı filtresi + `quantilesTDigestMerge`.
`GetServiceDeploys` (deploys.go:331, v0.9.249 MV fast-path) çizgileri zaten besliyor.

**API.** N2 ile **aynı uç**: `GET /api/endpoints/baseline?...&series=1` — tablo kolonu skaler
z-skor, drawer paneli seri ister.

**Efor.** ~1 gün (N2 baseline kolonuyla ortak; panel FE'si ~2 saat).

**Riskler.**
- Grafik **uPlot** (yeni kütüphane yok); mockup'taki SVG temsilî. Band için uPlot'un iki-seri +
  `fill` yolu kullanılır, tema `data-theme` değişiminde yeniden çözülmeli (chart re-resolve kuralı).
- Filo geneli "her route için baseline dedektörü" AYRI iş: o zaman `endpoint_baseline_1h` benzeri
  bir rollup gerekir (route × saat × gün-sınıfı; servis başına ~50 route × 168 slot — ucuz ama
  ayrı dilim). Bu turda **sadece açılan endpoint + görünür top-N** için hesaplanır.
- 7 günlük okuma bütçesi: `SETTINGS max_execution_time = 10`, sonuç 5 dk cache. Drawer açılışında
  değil, panel görünür olduğunda fetch (ES-cost disiplininin CH karşılığı).
- Yeni servis / yeni route'ta 7 gün yok → panel "yeterli geçmiş yok (N gün)" boş hâli, uydurma band yok.

---

## N6 — Kapsam chip'i + `Open in Explore →` + kaydedilmiş görünümler

Üç küçük, hepsi mevcut veriyle; ikisi düzeltme, biri eksik bağlantı.

### 6a. Drawer scope chip'i — **sessiz-filtre hatası**
`env=uat` seçiliyken tablo uat sayılarını gösteriyor, satıra tıklayınca açılan drawer o route'un
**TÜM env'lerini** (prod dahil) topluyor. Operatör aynı ekranda iki farklı gerçek görüyor.
`spans.deploy_env` typed LowCardinality kolon zaten var ve `endpointSplitDims`'te
(`endpoints_detail.go` ~402-404) kullanılıyor; `clusterExpr` derive'ı hazır. Eksik olan tek şey
**taşıma**: `api.ts` `endpointDetail` (~1613) / `endpointSplit` (~1618) imzalarında env/cluster
yok, `endpointDetailWhere` (~64-72) sadece time+service+kind+route kapsıyor.
→ Üç detay okumasına (detail / split / exemplar) `env` + `cluster` parametresi + drawer başlığına
`scope: env=uat · cluster=…` chip'i. **Yeni MV/kolon gerekmiyor** — bu üç okuma zaten ham spans.
**Efor ~2 saat. Risk:** cache anahtarına env/cluster girmezse çapraz zehirlenme (v0.5.187 sınıfı);
rolling deploy'da eski backend bilinmeyen paramı yok sayıp yine tüm env'leri toplar → **backend
önce**, FE chip'i sonra.

### 6b. `Open in Explore →`
`http.route` zaten resolver tier dim'i (`lib/resolverEligibility.ts:24`, `TIER_DIM_KEYS`),
`metricExploreHref(mq)` hazır (`lib/metricQuery.ts:122`), `EXEMPLAR_AGGS` p50/p90/p95/p99/
error_rate/apdex'i kapsıyor. Link deseni `pages/service/OperationsTable.tsx:533`'teki
`?filters=…&agg=…&field=duration_ms&result=metric` şeklinin birebiri, iki chip'le:
`service.name` + `http.route`. **Sıfır yeni sorgu. Efor ~1 saat.**
**Risk:** aktif `env` filtresi Explore'a da taşınmalı, yoksa 6a'daki tutarsızlık bu sefer
Explore'da tekrarlanır.

### 6c. `<SavedViewsBar page="endpoints">`
Bileşen var ve `/traces`, `/logs`, `/inbox`, `/anomalies`'de render ediliyor; `Endpoints.tsx`'te
**yalnız yorumda** geçiyor (satır 163 ve 268) — import bile edilmemiş. Kalıcılık zaten
`saved_views(page='…')` tablosunda, **yeni şema yok**. Sayfanın tüm seçimleri (service, search,
cluster, limit, compare, shape, `?cols=`, `?endpoint=`, sort) URL'de olduğu için görünüm
kutudan çıktığı gibi çalışır. **Efor ~1 saat. Risk:** yok denecek kadar az; tek dikkat, `?entry=`
(N1) ve baseline kolon görünürlüğü de query string'e yazılmalı ki kaydedilen görünüm aynı ekranı
geri getirsin.

### 6d. Sayfaya İÇERİ pivot (aynı dilimde önerilir)
`/service`, `/problems`, `/service-map`'ten `/endpoints`'e **tek bir link yok** — sayfa yalnız
sidebar'dan giriliyor, yani bir olay araştırması asla endpoint tablosuna inmiyor.
`<n> endpoint →` çıkış linki, ilgili servis + range önceden doldurulmuş.
**Efor ~1 saat. Risk:** yok.

**N6 toplam efor:** ~yarım gün.

---

## Bu mockup'ta ÇİZİLMEYENLER (gap listesinde var, bilinçli dışarıda)

| Gap | Neden bu turda değil |
|---|---|
| Deploy/versiyon karşılaştırması (before→after) | Yeni panel değil; mevcut "Split by attribute" tablosunun İÇİNE `service.version` seçiliyken ikinci kolon grubu olarak giriyor. Görsel olarak yeni bir yüzey üretmiyor, ~yarım gün. `service_version_5m` route dim'siz ve duration/error state taşımıyor → dürüst v1 iki pencereli aynı split okuması. |
| Endpoint bazlı apdex / SLO rozeti | Tam çözüm `spanmetrics_1m`'e iki `countIfState` kolonu eklemeyi gerektiriyor = MV state kolonu değişimi = rolling-deploy okuma-hatası penceresi (dual-column geçiş şart). Kısmi çözüm (`operation_summary_5m` apdex'ini span-adı=route olduğunda eşlemek) delikli; delikli bir SLI rozeti operatörü yanıltır. Ayrı dilim, ayrı karar. |
| KPI şeridinin pencere-gerçeği olmaması | `limit=100`'de "Total calls" pencere toplamı değil, dönen satırların toplamı. Düzeltme ayrı bir sorgu (window aggregate) gerektiriyor; mockup mevcut davranışı **olduğu gibi** gösteriyor ki yanlış bir "düzeldi" izlenimi doğmasın. |

## Önerilen sıra

1. **N3** (~2 saat) — en sık istenen tek hareket, sıfır ek tarama.
2. **N2'nin P90+Spread yarısı** (~1 saat) — bedava, ama ham/MV dört kombinasyon testiyle.
3. **N6** (~yarım gün) — 6a bir doğruluk hatası kapatıyor (sessiz-filtre sınıfı).
4. **N4** (~1 gün) — en büyük triage boşluğu; maliyet guard'ları kritik.
5. **N5 + N2 baseline kolonu** (~1 gün) — Dynatrace'in ayırt edici sinyali.
6. **N1** (~1 gün) — kapsam dürüstlüğü; şu an eksik gerçek gösteriliyor.
