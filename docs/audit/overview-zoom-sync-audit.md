# Audit — Servis Overview grafiklerinde zoom senkronu yok

2026-07-15 · Overview sekmesinde bir grafikte drag-zoom yapınca yalnız o
grafik zoom oluyor; diğer grafikler ve global tarih seçici güncellenmiyor.
Kod referansları frontend/src altında.

## Kısa hüküm

Mimari **beklenen çözüme çok yakın** — eksik tek şey: `OverviewChart`'ın
`onZoom` prop'u yok ve `ChartCard`/`ServiceOverview` global `setRange`'i
ona geçirmiyor. Aynı sayfanın **Performance bölümü** (`ServiceCharts`)
bu wiring'i **zaten** doğru yapıyor (`Service.tsx:484`). Fix = o deseni
Overview sekmesine kopyalamak. Düşük risk.

## 1. Global time range nerede + güncelleme fonksiyonu

- **URL query param `?range=`** — tek doğruluk kaynağı. `useUrlRange`
  (`lib/useUrlRange.ts:76`) `[range, setRange]` döndürür; yorumu
  (satır 7) "the SINGLE source of truth for a page's time range" der.
- **`setRange`** (`lib/useUrlRange.ts:88`) `setSearchParams` ile
  `?range=`'i URL'e yazar → global tarih seçici de `?range=`'i okuduğu
  için otomatik güncellenir.
- Service sayfası `range`'i sahiplenir ve `ServiceOverview`'a **prop
  olarak** geçer (`Service.tsx:456` → `range={range}`); Overview
  `from/to`'yu `windowNs ?? timeRangeToNs(range)` ile türetir
  (`Overview.tsx:171-172`).
- Custom aralık şekli (zoom sonucu): `setRange({ preset: 'custom',
  fromMs, toMs })` — `Dashboard.tsx` ve `Service.tsx:485` bunu kullanır.

## 2. Grafiklerin zoom/select callback'i

- **`OverviewChart`** (Overview'un kullandığı, `pages/service/charts/
  OverviewChart.tsx`): **`onZoom` prop'u YOK**, açık `cursor.drag`
  config'i YOK. `cursor: { x: true, y: false }` (satır 141) yalnız
  crosshair. Drag-zoom uPlot'un **VARSAYILAN `cursor.drag` (local
  setScale)**'ine düşer → yalnız o instance'ın ölçeğini rescale eder,
  hiçbir yere dispatch etmez. **Tamamen izole.** Kullanıcının gördüğü
  "sadece o grafik zoom oluyor" tam olarak bu.
- **`MultiLineChart`** (`components/MultiLineChart.tsx`): `onZoom?:
  (fromUnixSec, toUnixSec) => void` (satır 186), ref'te tutulur (249),
  `drag: { x: true, y: false, setScale: !!onZoom }` (472) + `setSelect`
  hook'u seçimi yakalayıp `onZoom` çağırır ve seçim kutusunu temizler
  (491-502). **Propagasyon VAR.**
- **`TimeSeriesPanel`** (`components/viz/TimeSeriesPanel.tsx:85`) ve
  **`TimeChart`** (`onBrush`): ikisinde de yukarı-dispatch VAR.
- **Sonuç:** Adı geçen üç bileşenden yalnız `OverviewChart` propagasyon
  yoksun. (Soru 2'deki "TimeChart/OverviewChart/TimeSeriesPanel" içinde
  eksik olan **sadece** OverviewChart.)

## 3. Çalışan senkron örneği (referans)

**İki** çalışan örnek var:
- **`Dashboard.tsx:353`** `onZoom={handleZoom}` → `handleZoom`
  `setRange({ preset:'custom', fromMs, toMs })` çağırır; yorum: "any
  panel updates the dashboard's time range so every panel re-fetches".
  Kanonik global-sync.
- **AYNI Service sayfasının Performance bölümü** (`Service.tsx:484`):
  `<ServiceCharts … onZoom={(f,t) => setRange({ preset:'custom',
  fromMs: Math.round(f*1000), toMs: Math.round(t*1000) })} />`. Yani
  fix deseni **birebir bir bileşen ötede, aynı sayfada** zaten mevcut —
  Overview sekmesi sadece kullanmıyor.

Wiring modeli: parent global `setRange`'i sahiplenir; her grafik
`onZoom(from,to)` ile yukarı bildirir; `setRange` `?range=`'i günceller;
`range` prop'u tüm grafiklere iner → veri yeniden çekilir. Overview'da
eksik olan tam bu zincirin son halkası (grafik→onZoom).

## 4. Overview parent'ı + shared state

- `ServiceOverview` (`Overview.tsx:167`) üç grafiği `ChartCard`
  (`Overview.tsx:123`) → `OverviewChart` (`Overview.tsx:158`) ile
  render eder.
- **`ChartCard` `OverviewChart`'a hiçbir shared callback GEÇMİYOR** —
  yalnız `times/series/unit/mode/deployAtSec/deployLabel`. Her grafik
  bağımsız prop setiyle mount. `onZoom` zinciri hiç yok.
- **`ServiceOverview` Props'unda `onZoom`/`setRange` YOK**
  (`Overview.tsx:36-41`: `{ windowNs, service, range, info,
  operations }`). Yani callback şu an parent'a kadar bile inmiyor.

## 5. Beklenen mimariye uzaklık — ÇOK YAKIN

Mevcut durum beklenen mimarinin neredeyse tamamına sahip:

| Beklenen | Mevcut |
|---|---|
| parent'ta `activeTimeRange` state | ✅ `range` (URL-backed `useUrlRange`), `ServiceOverview`'a prop olarak iniyor |
| global range güncelleme fonksiyonu | ✅ `setRange` mevcut, kardeş `ServiceCharts`'ta zaten wire (`Service.tsx:485`) |
| range değişince tüm grafiklerin yeniden beslenmesi | ✅ tek batched query `range`'e keyed (bkz. Soru 7) |
| grafik zoom → range'i çağırır | ❌ **eksik tek halka** — `OverviewChart` `onZoom` yoksun + threading yok |

**Minimal fix (3 küçük dokunuş):**
1. `OverviewChart`'a `onZoom?: (fromSec, toSec) => void` ekle —
   `MultiLineChart` desenini birebir kopyala: `drag: { x:true, y:false,
   setScale: !!onZoom }` + `setSelect` hook'u range'i yakalayıp `onZoom`
   çağırsın ve seçim kutusunu temizlesin. onZoom ref'te (v0.8.520 deseni).
2. `ChartCard` prop olarak `onZoom` alıp `OverviewChart`'a geçirsin.
3. `ServiceOverview` `onZoom`'u ya `Service.tsx`'ten prop olarak alsın
   (ServiceCharts ile birebir tutarlı) ya da içeride `useUrlRange()` ile
   `setRange`'e erişsin; her grafiğe `onZoom={(f,t)=>setRange({preset:
   'custom', fromMs, toMs})}` geçsin.

## 6. setData sonrası zoom davranışı

v0.8.531'de TSP için "setData sonrası kontrollü zoom yeniden uygulama"
deseni, POLL sırasında operatörü drag-zoom'dan atmamak içindi (aynı veri,
alt-aralık zoom'u korunur). **Overview'daki senaryo FARKLI ve daha
basit:** zoom `setRange`'i günceller → tek query YENİ from/to için
refetch eder → OverviewChart YENİ (dar) veri prop'u alır → v0.8.531
setData fast-path'i veriyi swap eder → yeni pencere tam-genişlikte
gösterilir. Yani "zoom = yeni veri penceresi" (Dynatrace/Dashboard
modeli), "mevcut verinin alt-aralığı" değil. Bu yüzden **zoom
re-apply'a GEREK YOK** — ama MLC drag config'i (`setScale:!!onZoom` +
setSelect'in seçim kutusunu temizlemesi) birebir kopyalanmalı ki refetch
tamamlanana kadar geçici görsel davranış kardeş bölümle aynı olsun.

## 7. Senkron zoom sırasında fetch — DUPLICATE YOK

- Overview **TEK batched query** kullanıyor: `seriesQ = useQuery({
  queryKey: ['service-overview-red', service, from, to], queryFn: () =>
  api.spanMetricBatch({…}) })` (`Overview.tsx:177-180`). rate/error_rate/
  latency HEP BİRLİKTE tek çağrıda gelir; üç `ChartCard` de bu tek
  sonuçtan (`s = seriesQ.data`) beslenir.
- Zoom → `setRange` → `?range=` → `range` prop → `from/to` yeniden
  hesaplanır → `queryKey` değişir → **tek batched query BİR kez** refetch
  → üç grafik de yeni veriden re-render. **Panel-başına duplicate/paralel
  fetch YOK.**
- KPI stat tile'ları (`MetricPanel` + `mkThroughput('stat')` vb.,
  `Overview.tsx:248-255`) kendi küçük stat query'lerine sahip; onlar da
  `range`'e keyed olduğundan zaten her range değişiminde refetch eder —
  fix YENİ bir duplication getirmiyor, mevcut davranış.
- **Öneri:** onZoom yalnız `setRange`'i çağırsın; veri yenileme tümüyle
  mevcut range→query mekanizmasına bırakılsın. Grafiklere ayrıca manuel
  fetch tetikletme; tek batched query + KPI tile'ları zaten sayfayı
  besliyor.

## Öneri

Fix minimal + geri döndürülebilir + kanıtlı: aynı sayfadaki
`ServiceCharts` wiring'ini (`Service.tsx:484-490`) Overview sekmesine
taşı. Üç küçük dokunuş (§5). Yeni fetch mimarisi gerekmez; tek batched
query zaten senkronu sağlar. Görsel zoom davranışı MLC deseninden
kopyalanır.
