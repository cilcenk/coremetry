---
name: frontend-design-system
description: Coremetry's UI component contract — find the existing primitive BEFORE writing a new one (the ui/ barrel is incomplete, which is how half the duplicates were born), pick the right Button/table variant, respect the @grafana/ui boundary (2 files, 2 jobs, CI-enforced), route every chart through CorePanel, and keep styling in tokens instead of inline style. Carries the measured duplicate map (Stat ×6, Field ×7, secondary button ×4 spellings) and the tech-lead review bar. Use BEFORE adding or restructuring any component, table, chart, badge, form field or CSS class under frontend/src/. Do NOT use for backend work, for a pure copy change, or for diagnosing a slow page (use /perf-triage).
---

# /frontend-design-system — önce ara, sonra yaz

Ölçüm tabanı: **288 non-test `.tsx`**. Aşağıdaki sayılar bu skill yazılırken
ölçüldü ve doğrulandı; bileşen adları + dosya yolları gerçek.

**Tek cümlelik teşhis:** atom disiplini iyi (`Button.variant` 451/451 sitede
yazılı, 89 tabloda `storageKey` 0 çakışma), ama **atomların bir kısmı
barrel'da yok** ve kopyaların çoğu bu yüzden doğdu.

## 1. ARAMA ZORUNLULUĞU

> **Adım 0: barrel YETMEZ.** `components/ui/index.ts` `PageShell`,
> `Spinner`, `Empty`, `Pager`, `Skeleton*`, `CopyButton`, `PageLoader`
> **export etmiyor** (doğrulandı). Barrel'a bakıp "yok" demek, bu
> dökümdeki kopyaların yarısının doğum sebebi.

```bash
cd frontend/src

# ADIM 0 — İKİ envantere birden bak
cat components/ui/index.ts
ls components/ui/*.tsx components/*.tsx | grep -v '\.test\.'

# ADIM 1 — isim çakışması (Stat/Field/Chip vakalarının üçünü de yakalar)
grep -rnE "(export )?(function|const) (Stat|Field|Chip|Badge|Empty|Section|Row|Pill|Card|Toggle|Pager|Tooltip)\b" \
  --include="*.tsx" . | grep -v '\.test\.'

# ADIM 2 — JSX kullanımını SAY (generic-farkında: <X<T> ve <X{…} kaçmasın)
python3 - <<'PY'
import os, re
TAG = 'Stat'                                    # ← aradığın bileşen
pat = re.compile(r'<' + TAG + r'(?=[\s/>{<])')
for dp, _, fs in os.walk('.'):
    for f in fs:
        if not f.endswith('.tsx') or '.test.' in f: continue
        p = os.path.join(dp, f); n = len(pat.findall(open(p).read()))
        if n: print(f'{n:4}  {p}')
PY

# ADIM 3 — CSS adı sahipli mi (Chip bu yüzden .btn-chip'e kaçmak zorunda kaldı)
grep -nE "^\.(chip|pill|stat|badge|sec|facet)" styles/globals.css

# ADIM 4 — görsel iş kopyalanmış mı (isim farklı olabilir → İMZA ara)
grep -rn "padding: '8px 10px'" --include="*.tsx" . | head
grep -rn 'className="sec"'     --include="*.tsx" . | head

# ADIM 5 — hangisi kanonik: RAPOR değil, git söyler
git log -S"function Stat" --format="%ad %h" --date=short --reverse -- <dosya> | head -1
```

**Arama sırası:** ① `ui/index.ts` → ② `components/ui/*.tsx` →
③ `components/*.tsx` → ④ Adım 1 çakışma taraması → ⑤ `globals.css`
sınıf sahipliği.

**Bulamazsan:** (1) yaz — **ama `ui/` altına**, sayfanın yanına değil
(`Stat`'ın 6'ya çıkma sebebi `features/dependencies/panels/` altında
saklanmasıydı); (2) **barrel'a ekle**; (3) CSS adını sahiplen.

## 2. Kanonik envanter

**`ui/` barrel içi (kullan):** `Button` · `Field`/`SelectField`/
`TextareaField` · `Stack`/`Row` · `Card` · `IconButton` · `Chip` ·
`Modal` · `Drawer` · `Badge` · `MenuItem` · `PageControls` ·
`DisclosureButton` · `LinkButton` · `SearchField` · `ActionRow` ·
`ConfirmProvider`/`useConfirm` · `FacetMultiSelect` · `VirtualList` ·
`VirtualTable` · `RouteSkeleton`

**Kanonik ama barrel DIŞINDA** (aramayı ıskalatan grup):
`PageShell` (`ui/PageShell.tsx`, 59 kullanım — **barrel'a eklenmeli**) ·
`Spinner` (202) · `Empty` (204) · `Pager` (7) · `Skeleton*` ·
`CopyButton` · `PageLoader` — hepsi `components/` altında.

**🔴 Atom BOŞLUĞU (kanonik yok):** `Stat` (95 kullanım, **6 tanım**) ·
`Tooltip` (0 bileşen, 7 DOM implementasyonu + 1299 native `title=`) ·
gezinme butonu (`Button` polimorfik değil, 36 sahipsiz site) ·
`Section` (5 tanım).

**☠️ Ölü:** `ListSkeleton` (0 kullanım) · `ServiceMap.tsx` içindeki 5.
`Chip` tanımı · `ChartCard`→`OverviewChart` zinciri (117 satır, 0 JSX
tüketicisi) · 59 öksüz CSS sınıfı (globals.css'in ~%4,9'u) · `mobileHide`
kolon alanı (mekanizma kurulu, 0 tüketici).

## 3. Tutarsızlık raporu

| # | Aile | Kopya | Kullanım | Kanonik |
|---|---|---:|---:|---|
| K1 | **`Stat`** | **6 tanım** | 95 | `features/dependencies/panels/shared.tsx` → **`ui/Stat.tsx`'e taşı** |
| K2 | **`Field`** | **7 tanım** | 203 | **`ui/Field.tsx`** — tek a11y taşıyan sürüm |
| K3 | **İkincil buton** | **4 yazım** | 274 | `<Button variant="secondary">` (gezinme hariç) |
| K4 | **`Badge`** | atom vs elle sınıf | 14 vs **297** | `ui/Badge.tsx` (union eksik — AS-2) |
| K5 | Boş durum / yükleniyor | 29 + 9 elle | — | `Spinner`/`Empty` |
| K6 | Chart lejantı | 3 kopya | — | `chart/StatsLegend.tsx` |
| K7 | Tooltip | 7 DOM impl. | 1299 `title=` | **yok** |
| K8 | `Chip`/`Pill` | 5 tanım + 5 CSS pill | 25 | `ui/Chip.tsx` |
| K9 | **Sayfalama** | **1** ✅ | 7 | `components/Pager.tsx` — **şablon budur** |

**K2'nin bedeli somut:** altı `Field` kopyasının hiçbirinde `useId` /
`htmlFor` / `aria-describedby` / `error` yok → **171 form alanı (%84)
etiket-input bağı ve hata bildirimi olmadan** çiziliyor. Ayarlar, Alerts,
Runbook, PanelEditor — operatörün **veri girdiği** her yüzey.

**K3'ün 36 sitesi disiplinsizlik değil, atom boşluğu:** `Button` polimorfik
değil (`as`/`href` yok), `LinkButton` bilinçli olarak gezinmiyor ("looks
like a link, IS a button"). Karar bekliyor (AS-1).

**K9 neden şablon:** `Pager` v0.9.1014'te 6 yüzeyi 1'e indirdi ve **iki
prop'u tip düzeyinde zorunlu** tuttu (`mode`, `count`) — `Button.variant`
hilesinin aynısı. `count: exact|capped|approx|skip` ES'in
`track_total_hits` tavanının yalan söylemesini **tipe** çeviriyor.
K1-K8'in kapatılma yolu birebir budur.

**Kopya OLMAYAN üç aile (dokunma):** tablolar (23 ham `<table>`, **1 gerçek
kaçak**) · Modal/Drawer (4 elle `role="dialog"`, hepsi popover sınır
vakası) · pickerlar.

## 4. Buton karar tablosu

| Durum | Kullan |
|---|---|
| Birincil eylem | `<Button variant="primary">` |
| İkincil eylem | `<Button variant="secondary">` |
| Yıkıcı | `variant="danger"` / `ghost-danger` |
| Vurgulu ama birincil değil | `variant="accent"` |
| Yalnız ikon | `<IconButton aria-label>` — **`aria-label` zorunlu** |
| Link görünümlü, gezinme YAPMAYAN | `<LinkButton>` |
| **Gerçek gezinme** | `<Link>` — **atom yok**, AS-1 |
| Onay isteyen | `useConfirm()` — native `confirm()` testle yasak |

`variant` **zorunlu prop** (451/451 sitede yazılı) — bu aileyi bozma.
**Ham `<button>` meşru tek yer:** `.tab-strip` sekmesi, `.facet` (belgeli),
primitif gövdeleri. Bugün `ui/` dışında **136 ham `<button>`** var;
yenisi eklenmez.

## 5. Tablo karar tablosu

| Durum | Mod |
|---|---|
| İstemci verisi, tam liste elde | `useDataTable` — sort + resize |
| Server-paged (Services/Traces/Logs) | resize-only; **client sort YASAK** — bir sayfalık server-ordered küme üzerinde sıralama yanıltır |
| >100 satır | + `contentVisibility:'auto'` |
| Çok büyük liste | `VirtualTable` (`Traces.tsx` emsali) |
| Sabit ≤10 satır, sıralanmayacak | ham `<table>` meşru |

`storageKey` **zorunlu ve benzersiz** — 89 tablo, 0 çakışma. Kolon tanımı
`COLS` dizisi: `id`/`label`/`width`/`sortValue`/`numeric`. Başlık
özelleştirmesi `DataTableHead`'in `renderLabel` hook'u ile; pure core'un
`label: string` sözleşmesi korunur.

## 6. @grafana/ui sınırı

**Fiili sınır: 2 paket, 2 dosya, 3 iş.** Prod kodda `@grafana/*` import
eden dosya sayısı **2** (doğrulandı):

| Dosya | Ne alıyor |
|---|---|
| `components/chart/CorePanel.tsx` | 9 sembol, hepsi chart primitifi (`UPlotChart`, `UPlotConfigBuilder`, enum'lar) |
| `lib/chart/dataFrame.ts` | `getDisplayProcessor`, `createTheme`, `ThresholdsMode`, `FieldType` |

| Grafana'dan | Bizim |
|---|---|
| uPlot config kurulumu + canvas render | Panel iskeleti, lejant, tooltip, overlay, zoom geçmişi, eksen ölçüsü |
| Veri modeli + **birim/eşik/ondalık çözümü** | **TEMA** (`data-theme` + `resolveVar` + `useThemeTick`) |

**K1 — `@grafana/*` yalnız bu iki dosyadan.** Tercih değil, **CI'da kırmızı
olan kapı**: `corePanelContracts.test.ts` tüm `src` ağacını tarar,
`dataFrame.test.ts` köprü tekelini pinler. `@grafana/schema` importu ve
`GraphDrawStyle` adı **yasak** (hayalet bağımlılık, prod build'de patlar).

**K2 — Yeni bir UI parçası için Grafana'ya BAKILMAZ.** `@grafana/ui`'den
tek bir genel UI bileşeni import edilmiyor: `Button`, `Select`, `Table`,
`Tooltip`, `Modal`, `Icon` — hiçbiri. Gerekçe: tema modelleri uyuşmuyor
(DOM-push vs React-context-pull; **provider'sız kullanım sessizce Grafana
dark ile çizer** = v0.9.398 bug sınıfı), emotion ikinci CSS-in-JS
runtime'ı, `Icon` SVG'yi runtime'da fetch ediyor, react-router major
çakışması.

**K3 — Grafana'dan bir şey almak, onu TEK DOSYAYA hapsetmeyi göze
almaktır.** Alınan semboller `@internal -- not a public API`; tazminat tek
tüketim noktası olması: kırılırsa değişecek dosya sayısı **bir**. Yeni bir
Grafana yüzeyi alan, aynı anda tekel kapısını da yazar.

**Sınır durumu:** ihtiyaç *birim/format/ondalık* ise → `dataFrame.ts`
köprüsüne ek; elle yazmak yasak (*"bizim yazdığımız her satır, oradaki
davranışın kopyası ve gelecekteki uyumsuzluğudur"*). İhtiyaç
*etkileşim/görsel davranış* ise → bizim saf çekirdek.

**Bundle:** grafana ayrı chunk (1.15 MB raw / 173 KB gzip), **lazy**. Bir
sayfa `@grafana/data`'ya **statik bağlanamaz** — ölçüldü: vendor 35 KB →
1 MB. Yalnız `import type` serbest.

## 7. uPlot

**Tek motor: `CorePanel`.** Yeni grafik = `CorePanelMulti` (lazy giriş
`corePanelEntry.tsx`). `MultiLineChart` bir adaptör — çizim yapmaz.

**Doğrudan uPlot yasağı fiilen tutuyor:** tüm repoda `new uPlot(` **1**
(`lib/chart/engine.ts`, doğrulandı), `<UPlotChart>` JSX **1**
(`CorePanel.tsx`). İskeleti atlayan sıfır kod var. Kalan borç mimari değil
**çokluk**: 2 çizim hattı, eski preset tarafında 6 canlı site.

> ⚠️ `viz/TimeSeriesPanel`'in **2 tüketicisi** var (`RuntimeCharts.tsx`
> **ve** `MetricQueryEditor.tsx`), 1 değil. Motor sökme turunda "tek site
> kaldı" diye temizlik yapan `MetricQueryEditor` önizlemesini derlenmez
> hale getirir.

**Tema/renk — iki kanal, bilinçli ayrı:**

| Kanal | Kaynak | Tema-canlı |
|---|---|---|
| Semantik rol | `lib/chart/seriesRole.ts` → `var(--err)`/`var(--ok)` | ✅ |
| Veri serisi paleti | `lib/chartFmt.ts` — 10 sabit hex, FNV-1a hash | ❌ **bilinçli tema-bağımsız** |

`data-theme` flip'inde yeniden çözüm: `lib/useThemeTick.ts` — modül
düzeyinde **tek** `MutationObserver` + `useSyncExternalStore`; 4 tüketici,
hepsi uPlot. Canvas CSS değişkeni okuyamadığı için `resolveVar` şart;
fallback hex meşru (`|| '#hex'`).

**Rol etiketten TAHMİN EDİLMEZ** — `seriesRole.ts` tek karar noktası.

**Eksen:** birim `@grafana/data` display processor'ından;
`formatValue(v, decimals)` ikinci parametreyi **geçirmeli** (yutulunca
"0 req/s" oluyor, v0.9.799). UCUM→Grafana eşlemesi
`lib/chart/metricUnit.ts`. **Bilinmeyen birim → `undefined` → ham sayı ve
bunu SÖYLE** — *"sessizce 'ms' varsaymak yanlış sayıya güven üretir."*
Y oluğu genişliği kendi processor'ımızla ölçülür (Grafana sabit 'Inter'
ile ölçüyor, bizde Inter yok → kırpma).

**Tooltip:** saf çekirdekler paylaşılıyor (`tooltipModel.ts`,
`placeTooltip`, `tooltipPin.ts`) — bu iyi. HTML üretimi paylaşılmıyor:
`.ov-tt` şablonu 3 kez byte-benzer yazılmış. **Yeni tooltip yazma**,
çekirdeği kullan.

## 8. Token disiplini

**Envanter:** tek CSS dosyası, 3589 satır, **70 değişken**, 3 tema (dark
varsayılan, light, redhat). Merdivenler: `--sp-1..8` (2-24px),
`--fs-3xs..xl` (9-18px), `--radius-xs..lg`, `--z-*` 18 katman.

**Inline style — ölçüm: 5010 blok / 250 dosya (%86,8 kirli).**

| Sınıf | Hüküm |
|---|---|
| Blokta **en az bir** hesaplanmış değer (`width: pct+'%'`, ternary, `${}`) | ✅ meşru |
| Spread (`...x`) · canvas'a verilecek çözümlenmiş string | ✅ |
| Blok **tamamen statik** | 🔴 **yasak** — sınıfa taşı |
| Yalnız layout (`display:flex`, `overflow:auto`) | ⚪ gri; tekrar ediyorsa sınıf |

Blokların **%91,6'sı %100 statik** — satır içi olmak için hiçbir
çalışma-zamanı gerekçesi yok. Dönüşüm sabit: `fontSize: 11` → `--fs-xs`,
`gap: 8` → `--sp-4`, `borderRadius: 6` → `--radius-sm`. `ui/` dışında
**4.480 ham geometri sayısı** var, **4.044'ü tam olarak bir rung'a eşit**.

**Hardcode renk:** `.tsx`'te yeni hex/rgba **yasak**. Tek istisna canvas
`getPropertyValue` fallback'i — ve **fallback token'ın GERÇEK değeriyle
eşleşmeli** (`var(--warn, #facc15)` reddedilir; gerçek `--warn` =
`#d29922`). Motor marka rengi ise dosya-içi gerekçe yorumu şart.
🔴 **Aktif drift:** `#0d1117` 6 yerde — v0.9.167'de terk edilen eski dark
zemin; flame graph'lerde kontur+metin rengi olarak basılıyor, yani
light/redhat temada yazı neredeyse siyah.

**Tek kullanımlık CSS:** tek-dosya kullanımı **normal** (bileşen-yerel BEM
önekleri: `cb-*`, `ch-*`, `dash-*`). İhlal olan **öksüz** sınıf: 79 tane,
~59'u gerçek ölü kod. **Kural: bir bileşen silinirken CSS ailesi de
silinir** — 59 ölü sınıfın tamamı bu adımın atlanmasından.

**Kapılar ve kör noktaları:** `styles/` altında 15 test / 159 assertion.
Ama `colorLeaks` yalnız **15 dosyalık donmuş liste** tarıyor (bugün 53
dosya renk literali taşıyor, 52'si kapısız) · `geometryTokens` yalnız
`components/ui/*` (kirli 250 dosyanın 8'i) · `undefinedCssRefs` yalnız
TSX→CSS yönü (ölü sınıf görünmez) · **inline style'ın kendisi HİÇBİR
kapıda yok** — `fontSize:11` tsc/eslint/vitest/`make audit` dörtlüsünden
sessizce geçiyor.
Muafiyet anahtarı **gerekçe**, satır numarası değil (satıra bağlı muafiyet
import eklenince kayar, v0.9.887).

## 9. Review çıtası — reddedilecek 10 madde

1. **Yeni ham `<button>`** (`.tab-strip`/`.facet`/primitif gövdesi hariç).
2. **Yeni `Field`/`Stat`/`Chip` tanımı** — kanonik varken 8./7./6. kopya.
3. **`ui/` dışına atom yazmak** — yeni primitif `ui/` altına + barrel'a.
4. **`aria-label`'sız `IconButton`**, `htmlFor`/`useId`'siz form alanı.
5. **Tamamen statik inline style bloğu.**
6. **`.tsx`'te yeni hex/rgba** (canvas fallback'i hariç, o da token
   değeriyle eşleşmeli).
7. **`@grafana/*` importu** `CorePanel.tsx`/`dataFrame.ts` dışında.
8. **Sayfanın `@grafana/data`'ya statik bağlanması** (vendor 35 KB → 1 MB).
9. **`useDataTable` atlayan yeni tablo**, `storageKey`'siz tablo, ya da
   server-paged tabloda client sort.
10. **Bileşen silinirken CSS ailesinin bırakılması.**

Bonus ret: native `confirm()` · `chartsV2` kelimesi (yorumda bile) ·
şablonla kurulan `className` (tip kapısı yok, sessiz kırılır).

## 10. Açık sorular

- **AS-1:** Gezinme butonu atomu — `Button`'a `as`/`href` mi, `NavButton`
  mu? 36 sahipsiz site bekliyor.
- **AS-2:** `Badge` union'ında `b-watcher` karşılığı yok; atom bugün 2
  siteyi karşılayamıyor → geçiş tamamlanamaz.
- **AS-3:** `PageShell` barrel'a eklensin mi (tek satır, 59 kullanım).
- **AS-4:** İki yarım birim haritası — `metricUnit.ts` (tam) vs
  `routeSeries.ts` (yalnız `s|ms`). Birleştirilsin mi?
- **AS-5:** Kapı genişletmeleri: `colorLeaks` donmuş listesi → tüm ağaç?
  `geometryTokens` → `ui/` dışına? Inline style kapısı kurulsun mu?
