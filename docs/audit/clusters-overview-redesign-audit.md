# Audit — /clusters yeniden tasarımı: kart grid'i → cluster detayı (Nodes → Namespace → Pods)

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Akış (mockup):** genel görünüm = cluster kartları; karta tıkla →
detay: geri linki → Node'lar → Namespace rollup → Pod tablosu.

## 1. Mevcut durum — teyit (brief'e göre İKİ GÜNCELLEME var)

| Konu | Durum |
|---|---|
| Clusters.tsx yapısı | ⚠️ Brief'ten İLERİDE: v0.8.584 az önce **Pods \| Nodes tab-strip'i** ekledi (fetch-on-open, ?tab=). Düz pod tablosu artık "Pods sekmesi". Redesign bu sekmeleri detay görünümünün bölümlerine dönüştürür (tab GEÇİCİYDİ — §4 S2'de kalkar, ?tab=nodes eski linkleri detaydaki Nodes bölümüne düşer). |
| Node CPU/mem/sayı backend'i | ⚠️ Brief'ten İLERİDE: **VAR** — v0.8.582 NodeMetrics (node-exporter ailesi, 5 sorgu) + v0.8.583 `GET /api/clusters/nodes`. Detay görünümünün Node bölümü hazır veriyle başlar, "yakında" durumu gerekmez. |
| Node/pod **network** | ✅ brief doğru: HİÇ YOK (internal/thanos'ta network geçmiyor). Aday metrikler `container_network_{receive,transmit}_bytes_total` (pod) / `node_network_*_bytes_total` (node) — **erişilebilirlikleri TEYİTSİZ**, probe disiplinine tabi (§5 probe seti; kart network alanı probe onayına kadar gizli). |
| Namespace rollup | ✅ yok — §3.3'te karar. |
| Fan-out/hata-çipi/URL prensipleri | ✅ useQueries per-cluster, 60s slot, kısmi sonuç; ?cluster= + ?namespace= composable; Drawer/DrawerTrendRow aynen. |
| Kart deseni mevcut mu | ✅ `components/ui/Card.tsx` (default/tight yoğunluk, header/footer slotları) + `.card`/`.card:hover` CSS + KPI mini-bileşen emsalleri — YENİ kart bileşeni gerekmez. |

## 2. Yönlendirme modeli (URL kaynak-of-truth, kırılma yok)

- `?cluster` YOK → **genel görünüm** (kart grid'i).
- `?cluster=X` → **X'in detayı** (geri linki `?cluster`'ı siler).
- `?namespace=Y` detayda pod tablosunu süzer (bugünkü davranış) +
  namespace rollup satırını seçili gösterir. `?service=` (v0.8.579
  pivotu — pod-service correlation işi ayrıca onaylıysa) aynı şekilde
  composable kalır.
- Servis→Cluster pivot linki (`?cluster=X&namespace=Y`) değişmeden
  ARTIK detay görünümüne düşer — iyileşme, kırılma değil.
- `?pod=` drawer kimliği aynen (detay içinde açılır).

## 3. Tasarım kararları

### 3.1 Genel görünüm kartları — ÖNERİ: ayrı hafif summary ucu

| Yaklaşım | Değerlendirme |
|---|---|
| Mevcut pods ucunu çekip client'ta özetlemek | N cluster × topk(500) satır sırf kart için — gövde ~yüzlerce KB/cluster, üstelik topk kesmesi pod SAYISINI yanlışlaştırır (500 tavanlı sayım). Reddedildi. |
| **`GET /api/clusters/summary?cluster=X` (ÖNERİLEN)** | Skaler cevaplı 4-6 mini sorgu: `count by ()` node sayısı (idle-seri sayımından değil `count(count by (instance)(node_cpu_seconds_total{mode="idle"}))`), pod sayısı `count(count by (namespace,pod)(container_cpu_usage_seconds_total{...ns}))` (TAM sayı — topk'siz, vektör değil skaler), toplam CPU çekirdek kullanımı, toplam mem; network toplamları PROBE SONRASI eklenir. serveCached 60s, digest'e nsFilter dahil (pod sayısı ondan etkilenir). Fan-out yine istemcide — kart başına bir istek, bozuk cluster kartı "erişilemiyor" durumuna düşer. |

Kart durum rozeti üç değer: **erişilebilir** (summary 200) /
**erişilemiyor** (fetch error — çip dili) / **telemetride görülmüyor**
(Settings sekmesindeki mevcut rozetin aynısı: ad, gözlenen cluster
listesinde yok — `useClusters` verisi, ek maliyet yok).

### 3.2 Detay lazy'liği

`?cluster=X` görünümünde useQueries fan-out'u KURULMAZ; yalnız X'in
summary+nodes+namespaces+pods sorguları koşar (enabled bayrakları
görünüme bağlı — v0.8.584'ün fetch-on-open deseninin devamı). Genel
görünümde de yalnız summary'ler koşar; pods/nodes fan-out'u tamamen
kalkar (bugünkü "All clusters" düz tablosunun N×topk maliyeti
REDESIGN'LA ORTADAN KALKIYOR — sayfanın en pahalı yolu siliniyor).

### 3.3 Namespace rollup — ÖNERİ: ayrı sorgu (client-side türetme değil)

topk(500) pod listesinden client-side rollup, büyük cluster'da
kesilmiş listeden toplar → namespace toplamları SESSİZCE eksik (500.
pod'dan sonrası yok sayılır). Ayrı sorgu kesin ve ucuz:
`sum by (namespace) (rate(container_cpu_usage_seconds_total{...}[5m]))`
+ mem karşılığı + `count by (namespace)(count by (namespace,pod)(...))`
pod sayısı — 3 sorgu, satır sayısı = namespace sayısı (≤yüzler).
`GET /api/clusters/namespaces?cluster=X`, aynı cache deseni. Rollup
satırı tıklaması `?namespace=` yazar (URL üzerinden pod tablosu süzülür
— yeni state yolu YOK).

### 3.4 Bileşenler — yeni atom YOK

- Cluster kartı: `ui/Card` (default) + `.badge` rozetleri + KPI mini
  satırları; grid `repeat(auto-fill, minmax(260px, 1fr))` (Endpoints
  KPI grid'i emsali). Tıklama karta `onClick` → setParams cluster.
- Node kompakt listesi: `ui/Card density="tight"` içinde MEVCUT
  clusternodes useDataTable tablosu aynen taşınır (sort/resize
  korunur — kısıt); "kompakt kart/liste" görsel isteği Card
  sarmalayıcısı + mevcut tabloyla karşılanır, yeni liste deseni
  üretilmez.
- Pod tablosu: POD_COLS + (probe sonrası) network kolonları; sort/
  resize/contentVisibility aynen.
- Erişilemeyen cluster detayı: `<Empty icon="✗">` + hata mesajı +
  "Settings → Remote clusters" linki (boş tablo YOK); kısmi-sonuç
  felsefesi korunur (genel görünümde diğer kartlar dolu).

## 4. Dilim/tag planı (onaya sunulan)

| Dilim | İçerik | Tahmin |
|---|---|---|
| S1 | `GET /api/clusters/summary` (skaler sorgular; network HARİÇ) + testler | ~45 dk |
| S2 | Frontend yönlendirme: kart grid'i (genel görünüm) + detay iskeleti (geri linki + mevcut Nodes/Pods bölüm taşıması, tab-strip kalkar, ?tab=nodes düşüşü) | ~1.5 saat |
| S3 | `GET /api/clusters/namespaces` + rollup tablosu + satır→?namespace= süzme | ~1 saat |
| S4 | **PROBE SONRASI**: network sorguları (pod+node+summary) + kolonlar/kart alanı | ~1 saat |

S1-S3 probe'suz güvenli (mevcut doğrulanmış metrik ailesi + skaler
sayımlar); S4 §5 probe'una tabi. Her dilim kendi tag'i (v0.8.585+),
her dilimde tam kapı + deploy.

## 5. Network probe seti (S4 ön şartı — runbook'a da girecek)

```bash
probe 'count(container_network_receive_bytes_total)'   # pod net (cAdvisor)
probe 'count(node_network_receive_bytes_total{device!="lo"})'  # node net
```
İkisi de ✓ değilse S4'ün ilgili yarısı düşer; kart network alanı
veri yokken hiç render edilmez ("—" bile değil — alan yokluğu
yanlış sıfır okutmaz).

## 6. Kısıt teyitleri

- useDataTable sort/resize her tabloda aynen (taşıma, yeniden yazma
  değil); ?cluster=/?namespace= composable; kısmi-sonuç davranışı
  genel görünümde kart durumuna, detayda hata paneline eşlenir.
- Pod drawer (?pod=) ve dakikalık trend dokunulmadan detay içinde.
