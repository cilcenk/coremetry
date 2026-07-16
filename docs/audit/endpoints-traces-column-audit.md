# Audit — /endpoints "Traces" kolonu görünmüyor / sayfaya sığmıyor

**Tarih:** 2026-07-16 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Rapor:** Operator-reported — Traces kısmı net görünmüyor, sayfaya sığmıyor.

## 1. Kök neden — TEYİT EDİLDİ

Ön incelemedeki dört tespitin dördü de doğru; kod okumasıyla doğrulandı:

1. **Tablo geometrisi:** `Endpoints.tsx:382` → `tableLayout:'fixed', width:'100%'`,
   sarmalayıcı `.table-wrap` (`globals.css:527`) `overflow-x:auto`. CSS 2.1
   §17.5.2.1 gereği fixed layout'ta tablo genişliği = max(belirtilen genişlik,
   kolon toplamı) — tarayıcı kolonları SIKIŞTIRMAZ, tablo 1454px render edilir
   ve fazlası `.table-wrap` yatay scroll'una düşer.
2. **Kolon toplamı:** 22 (expander) + 1432 (14 veri kolonu) = **1454px**
   (Service 150, Path 260, Method 68, Calls 84, Errors 76, Error rate 92,
   Status 140, Req/min 84, Avg 78, P50/P95/P99 72×3, Trend 120, Traces 64).
3. **Kullanılabilir alan** (sidebar 220 + content padding 40 düşülünce):

   | Viewport | Tabloya kalan | 1454'e göre | Traces ilk açılışta |
   |---|---|---|---|
   | 1366 | ~1106 | −348 | scroll gerisinde |
   | 1440 | ~1180 | −274 | scroll gerisinde |
   | 1536 | ~1276 | −178 | scroll gerisinde |
   | 1920 | ~1660 | +206 | görünür |

4. **Neden "hiç fark edilmiyor":** yatay scrollbar `.table-wrap`'in EN ALTINDA —
   2000 satırlık tablonun dibinde. macOS overlay scrollbar'ları da ancak scroll
   sırasında görünür. Operatör açısından sağdaki kolonlar "yok".

**Bonus bulgular (ilişkili, aynı kolonda):**
- Traces kolonu **64px kendi içeriğini de klipsliyor**: "view →" (11px font
  ≈ 42px) + hücre padding 24px ≈ 66px > 64. 1920'de bile ok karakteri taşıyor.
- Trend kolonu 120px < Sparkline 100px + padding 24px = 124px → 4px klips
  (kozmetik; bu audit'in kapsamı dışında bırakılabilir).

## 2. Seçenekler

### Seçenek 1 — Varsayılan genişlikleri daraltmak

Path 260→220, Status 140→116, Service 150→140, Error rate 92→84,
Req/min 84→76, Avg 78→70, P50/P95 72→64, Trend 120→110 (Sparkline 100→84),
Traces 64→76 (klips fix'i, +12). Yeni toplam: **1342px**.

| + | − |
|---|---|
| En basit diff (tek dosya + Sparkline width) | **Matematiksel olarak yetmiyor**: 1342 > 1276 → üç laptop genişliğinde de Traces yine scroll gerisinde |
| Resize etmiş kullanıcıların localStorage'ı dokunulmaz | Sığdırmak için ~240px daha traş gerekir → Calls/Errors 6-haneli sayılarda, Path'te okunabilirlik feda |
| | Sorunu çözmeden yoğunluk hissini artırır |

### Seçenek 2 — Traces kolonunu sağda sabitlemek (`position: sticky; right: 0`)

Datadog/PatternFly'ın "pinned action column" deseni. `DataTableColumn`'a
opsiyonel `stickyRight` alanı; `DataTableHead` th'ye `.sticky-right` sınıfı
basar; Endpoints body td'si sınıfı aynalar; `globals.css`'e ~15 satır
(taban `--bg0`, hover `--bg2`, `.row-selected` accent flatten). ~4 dosya,
~40 satır — diff hazır: `scratchpad/endpoints-sticky-option2.patch`.

| + | − |
|---|---|
| Her viewport'ta ve her scroll pozisyonunda görünür — kök nedeni doğrudan çözer | Sticky hücre opak arkaplan ister → hover / err-tint / row-selected durumları CSS'te aynalanmalı (seam riski; err satır tint'i sticky td'de `color-mix(... var(--bg0))` ile flatten edilir) |
| Endüstri deseni (Datadog aksiyon kolonları, PatternFly pinned columns) | `border-collapse:collapse` + sticky'de satır alt çizgisi scroll sırasında hücre arkasında kalabilir (bilinen minör Chrome davranışı) |
| Resize / sort / localStorage persist davranışı dokunulmaz | Primitife yeni alan (`stickyRight`) — şimdilik tek kullanıcı |
| Tema token'ları üzerinden üç temada (dark/light/redhat) çalışır | Taşma yokken (1920) de hafif sol gölge görünür ("pinned" göstergesi olarak okunur) |
| Traces 64→76 klips fix'i dahil | |

### Seçenek 3 — Kolon göster/gizle (Logs `onToggleColumn` emsali)

Operatör ihtiyacına göre kolon seti; görünür set localStorage'da persist.

| + | − |
|---|---|
| 14+ kolon büyümesine yapısal cevap; operatör kontrolü | En geniş kapsam: 14 elle yazılmış body td'nin koşullu render'ı + DependencyStrip `colSpan` dinamikleşmesi + görünürlük persist + DataTableHead/Colgroup filtre desteği (~2 saat) |
| Logs'ta çalışan emsal var | **Tek başına ilk açılış sorununu çözmez**: default hepsi açıksa taşma aynen kalır; default bazıları kapalıysa "hangileri?" kararı bilgi kaybıdır |
| | Bug fix değil feature — ayrı iş olarak ele alınmalı |

### Seçenek 4 — Kombinasyon

2 (sticky) + 1'in hafif versiyonu (yalnız Path 260→230, Status 140→120;
toplam 1454→1404) → scroll miktarı azalır, Traces zaten sabit. 3 istenirse
ayrı feature olarak kuyruğa.

## 3. Öneri

**Seçenek 2** (gerekirse 4'teki hafif traşla). Gerekçe: kök neden "son
kolon scroll gerisinde + scrollbar keşfedilemez"; 1 matematiksel olarak
çözmüyor, 3 ayrı bir feature ve default'u ne olursa olsun bu bug'ı tek
başına kapatmıyor. 2, dar diff'le sorunu her genişlikte kapatıyor ve
Traces'ın kendi 64px klipsini de düzeltiyor.

Riskler bilinçli: seam riski üç durumun (hover/err/selected) CSS'te
aynalanmasıyla sınırlandı; doğrulama operatörde (Playwright yok) —
kontrol listesi release notunda verilecek.

## 4. Kapsam dışı (bilinçli)

- Trend/Sparkline 4px klipsi (kozmetik; istenirse 4'e eklenir).
- Diğer DataTable sayfalarına stickyRight yaygınlaştırması — önce
  Endpoints'te operatör onayı.
- Kolon sırası değişikliği (Traces'ı öne almak) — düzen değişikliği,
  mockup-first kuralına girer; sticky varken gereksiz.
