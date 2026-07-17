# Audit — Thanos trend görünümlerini MultiLineChart'a yükseltme

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Hedef:** tekil-pod ve multi-pod (namespace) trendleri Sparkline'dan
MultiLineChart'a; deploy işaretçileri + limit/request referans
çizgileri. Yerleşim-bağımsız tasarım (ardından gelecek sekmeli detay
layout'una taşınmadan otursun).

## 1. Mevcut durum — teyit (bir düzeltme, bir bağımlılık bulgusu)

| İddia | Durum |
|---|---|
| MultiLineChart yetenekleri | ✅ series/unit/deploys/thresholds/syncKey/compare/logScale/onBucketClick — DeployMarker {timeUnixNs, label, description?}, Threshold {value, label?, severity?} (MultiLineChart.tsx:46-60); mevcut kullanıcılar (ServiceCharts, TimeSeriesPanel, OverviewChart, Dashboard) dokunulmaz |
| Sparkline tek-serili, tablo satırları için doğru | ✅ Sparkline.tsx:43 — tablo-satırı kullanımlarına (Endpoints Trend kolonu vb.) dokunulmuyor; Hosts drawer'ı da (OTel-kaynaklı, Thanos değil) kapsam dışı |
| **PodRow/NodeRow'da cpuLim/memLim var** | ⚠️ **DÜZELTME: YOK.** Ham limit/request değerleri PodMetrics'in iç `acc`'ında kalıyor (client.go:373), satıra yalnız YÜZDeler iniyor (CPUPct/CPUPctOfReq…). Threshold çizgisi mutlak değer ister (cores/bytes ekseninde) → T1'de PodRow'a ham alanlar eklenir (additive, omitempty): CPULimitCores/MemLimitBytes/CPURequestCores/MemRequestBytes. NodeRow'da da yok ama node trendi bu kapsamda değil. |
| RecentDeploy servis bazında var | ✅ `GET /api/services/{name}/deploys` + api.ts:658 — **ama köprü eksik:** pod satırı SERVİS kimliği taşımıyor; pod↔servis korelasyon audit'i (clusters-pod-service-correlation-audit.md) hâlâ ONAYSIZ. Deploy marker'lar o iş inmeden beslenemez → §5 T4 bağımlılık-kapılı. |
| PodTrend şablonu | ✅ artık ortak `rangeTrend` gövdesi (v0.9.2) — multi-pod varyantı aynı decode/bucket yolunu genişletir |
| v0.9.1'in tıkla-büyüt'ü | Bu iş onu GEÇERSİZ KILAR: grafik artık tıklama arkasında değil, ANA görünüm — expand toggle kalkar (bilinçli, aynı gün iterasyonu) |

## 2. Tasarım — yerleşim-bağımsız ThanosTrendPanel

`pages/clusters/TrendPanel.tsx` — kendi kendine yeten modül:

```tsx
<ThanosTrendPanel
  cluster="prod-ist" namespace="payments"
  pod="api-1"                      // varsa tekil-pod modu
  mode="pod" | "pods-by-namespace" // multi-pod: pod başına seri
  window={tw}                      // rung'lı pencere ('' = sayfa range'i)
  thresholds={{cpuLimit, cpuRequest, memLimit, memRequest}}  // opsiyonel
  deploys={deployMarkers}          // opsiyonel (T4'e kadar boş)
/>
```

- Panel KENDİ fetch'ini yapar (React Query, staleTime 60s = sunucu
  TTL'i, fetch-on-mount → drawer'da fetch-on-open korunur); CPU ve
  Memory AYRI iki MultiLineChart (birimler farklı — tek eksene
  sığmaz), `syncKey` ile crosshair senkron (Endpoints modal emsali).
- Drawer bugün paneli mount eder; yarınki sekmeli layout aynı paneli
  sekme gövdesine mount eder — taşıma sıfır.

### 2.1 Veri dönüşümü — paylaşılan saf yardımcı (+vitest)

`pages/clusters/trendSeries.ts`:
```ts
thanosTrendToSeries(trend: TrendPoint[], label): SpanMetricSeries[]        // tekil
thanosPodSeriesToSeries(series: {pod, points}[]): SpanMetricSeries[]       // multi-pod
limitThresholds(cpuLimit?, cpuRequest?): Threshold[]  // limit=err, request=warn
```
bucket (unix s) → time (ns); tekil ve multi-pod AYNI dönüştürücüden.

### 2.2 Tekil pod (T2)

- Mevcut `/api/clusters/pods/detail` verisi + PodRow'un yeni ham
  limit/request alanları → CPU grafiğine `cpuLimit` (err) +
  `cpuRequest` (warn) yatay çizgileri; Memory'ye mem karşılıkları.
  Limit yoksa çizgi yok (0 = bilinmiyor sözleşmesi — çizgisiz grafik).
- DrawerTrendRow/Sparkline pod drawer'ından ÇIKAR (Hosts drawer'ında
  yaşamaya devam eder); v0.9.1 expand toggle'ı kalkar.

### 2.3 Multi-pod / namespace (T3)

- Yeni uç: `GET /api/clusters/namespaces/pods-trend?cluster=&namespace=&from=&to=`
  → `topk(10, sum by (pod) (rate(container_cpu_usage_seconds_total{...,namespace="X"}[5m])))`
  range step=60 (+ mem karşılığı). **N=10 sabit** (legend okunabilirliği;
  uPlot 10 seriyi rahat taşır, 50'yi taşısa da operatör okuyamaz) —
  kalan pod'lar "top 10 by CPU" notuyla belirtilir. Aynı serveCached/
  clamp/deadline sözleşmesi. NamespaceTrend (v0.9.2 toplam çizgisi)
  panelde "Total" serisi olarak eklenebilir (colorOf ile soluk).
- Namespace satırındaki drawer affordance'ı (önceki audit B2) bu
  panelle gelir — ayrı eski-stil NamespaceDrawer HİÇ yapılmaz.

### 2.4 Adaptif pencere × zoom — öncelik kararı

İki mekanizma FARKLI katmanlarda tutulur, çakışma tasarımla çözülür:
- **?tw= rung seçici = FETCH penceresi** (cache-key bounded, v0.9.1'den
  kalır). AnomalyDetailDrawer'ın olay-çapalı lead-in chartRange deseni
  BENİMSENMEZ: clusters drawer'ında çapa alınacak "olay" yok —
  spike-lead-in'in anlamlı olduğu tek yer anomali bağlamı, orada zaten
  var.
- **Drag-zoom = GÖRÜNÜM keşfi** (uPlot, fetch tetiklemez — onZoom
  bağlanmaz); pencere değişince zoom sıfırlanır (yeni veri).
  Öncelik: zoom görünümü daraltır, tw veriyi belirler — hiyerarşi net.

## 3. Kapsam dışı (bilinçli)

- Node trend grafiği (node satırlarında trend ucu yok — ayrı iş).
- Sekmeli detay layout'u (ayrı brief — panel ona hazır).
- Hosts drawer'ı + tablo Sparkline'ları — dokunulmaz.
- Deploy marker'lar korelasyon inene kadar (T4).

## 4. Dilim/tag planı (onaya sunulan)

| Dilim | İçerik | Tahmin |
|---|---|---|
| T1 | Backend: PodRow ham limit/request alanları + pods-trend ucu (topk sum by pod) + testler | ~45 dk |
| T2 | trendSeries.ts (+vitest) + ThanosTrendPanel + PodDrawer geçişi (threshold'lu, deploy'suz) | ~1 saat |
| T3 | Namespace drawer/panel: multi-pod seriler + Total çizgisi + ikon affordance | ~45 dk |
| T4 | **BAĞIMLILIK-KAPILI:** deploy marker'lar — pod↔servis korelasyon audit'i onaylanıp Service alanı satıra indikten sonra (`GET /api/services/{name}/deploys` → DeployMarker[]) | ~30 dk |

T4'ün kapısı: clusters-pod-service-correlation-audit.md hâlâ onay
bekliyor — bu iş ona bugün bir neden daha ekledi (Service alanı hem
etiket hem deploy köprüsü).

## 5. Kısıt teyitleri

- MultiLineChart'ın diğer kullanıcıları: yalnız TÜKETİLİYOR, props
  değişikliği yok.
- Sparkline tablo kullanımları + Hosts drawer'ı: dokunulmuyor.
- serveCached/fetch-on-open: panel fetch'i mount'ta (drawer açılışı),
  yeni uç aynı cache sözleşmesinde; tw rung'ları bounded.
