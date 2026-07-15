# Audit — TraceWaterfall süre etiketlerinin sert kesilmesi

2026-07-15 · HEAD `816fd667` (v0.8.535) · Salt-okunur inceleme, hiçbir dosya
değiştirilmedi. Kod: `frontend/src/components/TraceWaterfall.tsx`,
`frontend/src/components/AggregatedStructure.tsx`,
`frontend/src/styles/globals.css`.

## Kısa hüküm

1. **Her iki iddia da birebir doğru, drift YOK.** `.wf-bar-label`'da
   `text-overflow` ve `min-width` gerçekten yok (`globals.css:784`);
   dışarıdaki etiket gerçekten koşulsuz sağa konumlanıyor
   (`TraceWaterfall.tsx:633-634`).
2. **Brief'in kaçırdığı şey: aynı iki class'ı `AggregatedStructure.tsx`
   de kullanıyor** (`:157`, `:161-165`) — **aynı kodla, aynı bug'la**.
   CSS düzeltmesi (madde 1) ikisini birden onarır; JS tarafı (madde 2)
   orada uygulanamaz (aşağıda).
3. **Kutu modeli matematiği düzeltilmeli:** `barAreaWidth` =
   `containerWidth - colWidth`, `- 8` **değil**. Global
   `box-sizing: border-box` (`globals.css:180`) + `.wf-row-bar`'ın
   padding'i olmaması bunu tam eşitlik yapıyor. `-8` zararsız (sola
   çevirmeye biraz daha istekli olur) ama türetilmiş değil.
4. **CSS kaynak sırası kritik:** `.wf-bar-label-outside-left`,
   `.wf-bar-label-outside`'ın `transform: translateY(-50%)`'ini ezmek
   zorunda. İkisi de tek-class seçici → **eşit specificity** → kazananı
   sıra belirler. Yeni kural `:785`'ten **sonra** gelmeli.
5. Doğrulama: `npx tsc --noEmit` + `npx eslint src` + manuel. `.tsx`
   dosyasında **zaten** bir `react-refresh/only-export-components`
   uyarısı var (`:84` `svcColorToken`) — saf yardımcı eklenecekse ayrı
   modüle koymak gerek.

---

## 1. İddiaların doğrulanması

### 1.1 Bar İÇİ etiket — ellipsis imkânsız ✅ DOĞRU

`globals.css:784`:
```css
.wf-bar-label { font-size: 10px; color: rgba(255,255,255,.92); white-space: nowrap;
                font-family: monospace; overflow: hidden; pointer-events: none;
                text-shadow: 0 1px 1px rgba(0,0,0,.25); }
```
`globals.css:782`:
```css
.wf-bar { position: absolute; top: 50%; transform: translateY(-50%); height: 12px;
          border-radius: 2px; min-width: 2px; display: flex; align-items: center;
          overflow: hidden; padding: 0 5px; transition: filter .1s; }
```

Zincir birebir brief'in dediği gibi: `.wf-bar` bir **flex container**,
`.wf-bar-label` bir flex item ve flex item'ların `min-width` varsayılanı
`auto` → içeriğinden dar olamıyor → `text-overflow` olsa bile tetiklenemez.
Kırpmayı yapan `.wf-bar`'ın `overflow: hidden`'ı, ve `.wf-bar-label`'da
`text-overflow` **yok** → kesik karakterin ortasından geçiyor.

Tetikleyici: `labelInside = parseFloat(widthPct) > 6` (`TraceWaterfall.tsx:510`).
Yani bar genişliği trace'in %6'sını geçtiği anda etiket içeri giriyor —
ama %6'lık bir bar dar bir ekranda 40px bile olmayabilir; `fmtNs(dur)`
("1.23ms", "456µs") monospace 10px'te ~36-48px. Artı `.wf-bar`'ın
`padding: 0 5px`'i. Yani kesilme **sık**.

### 1.2 Dışarıdaki etiket — koşulsuz sağa ✅ DOĞRU

`TraceWaterfall.tsx:632-637`:
```tsx
{!labelInside && (
  <span className="wf-bar-label-outside"
        style={{ left: `calc(${startPct}% + ${widthPct}% + 4px)` }}>
    {fmtNs(dur)}
  </span>
)}
```
`globals.css:785`:
```css
.wf-bar-label-outside { position: absolute; top: 50%; transform: translateY(-50%);
                        font-size: 10px; color: var(--text2); font-family: monospace;
                        pointer-events: none; white-space: nowrap; }
```
Kırpan: `#wf-outer { … overflow: hidden; … }` (`globals.css:578`).
`.wf-row-bar`'da (`:781`) `overflow` **yok**, yani etiket satırdan taşıp
konteynerin kenarında kesiliyor.

Brief'in "neredeyse her trace'in son işlemi kısa ve sona yakın olur"
tespiti yapısal olarak doğru: `startPct + widthPct → 100` olan her span
için etiket görünür alanın dışına düşüyor.

## 2. Kutu modeli — `barAreaWidth`'in doğru türetimi

| Kanıt | Sonuç |
|---|---|
| `globals.css:180` — `* { box-sizing: border-box; margin: 0; padding: 0; }` | Tüm genişlikler border dahil |
| `containerWidth` ← `#wf-outer`'ın `clientWidth` / `contentRect.width` (`TraceWaterfall.tsx:217-218`) | **content box** — `#wf-outer`'ın 1px border'ı zaten hariç |
| `.wf-row-name { flex-shrink: 0; … border-right: 1px … }` (`:675`), JSX'te `style={{ width: colWidth }}` | border-box sayesinde 1px border `colWidth`'in **içinde** |
| `.wf-row-bar { flex: 1; position: relative; }` (`:781`) — padding/border yok | Kalan alanın tamamı |
| `.wf-row` (`:598`) — yatay padding yok | Ek pay yok |

→ **`barAreaWidth = containerWidth - colWidth`** (tam eşitlik).

Brief'in `- 8`'i türetilmiş değil. Etkisi: `spaceRightPx`'i 8px küçük
gösterir → sola çevirmeye biraz daha istekli olur. **Zararsız** (asla
"daha kötü değil" garantisini bozmaz) ama dokümante edilmeli. Öneri:
fudge'ı at, eşiği açıkça yaz. `defaultNameWidth`'teki `- 6`
(`TraceWaterfall.tsx:345`) başka bir amaca hizmet ediyor, buraya
kopyalanmamalı.

## 3. Tasarım

### 3.1 CSS — iki kural

`globals.css:784`, ekleme:
```css
.wf-bar-label { … text-overflow: ellipsis; min-width: 0; }
```
`min-width: 0` flex-item kilidini açar, `text-overflow: ellipsis` de
artık **tetiklenebilir** hale gelir. İkisi birlikte gerekli; tek başına
`text-overflow` hiçbir şey yapmaz (asıl bulgu bu).

`globals.css`, **`:785`'ten SONRA** yeni kural:
```css
.wf-bar-label-outside-left { transform: translate(-100%, -50%); }
```
⚠️ Sıra zorunlu: `.wf-bar-label-outside` ile eşit specificity, sonra
gelmezse `translateY(-50%)` kazanır ve etiket bar'ın üstüne biner.

### 3.2 TraceWaterfall — taraf kararı

`:510` civarında, `labelInside` ile aynı yerde:
```tsx
const barAreaWidth = Math.max(0, containerWidth - colWidth);
const startFrac = parseFloat(startPct) / 100;
const endFrac   = startFrac + parseFloat(widthPct) / 100;
const spaceRightPx = barAreaWidth * (1 - endFrac);
const spaceLeftPx  = barAreaWidth * startFrac;
// Sağ dar VE sol gerçekten daha ferah → sola çevir. Aksi halde bugünkü
// davranış — hiçbir durumda eskisinden kötü olmasın.
const labelLeft = !labelInside
  && spaceRightPx < OUTSIDE_LABEL_MIN_PX
  && spaceLeftPx > spaceRightPx + OUTSIDE_LABEL_MIN_PX;
```
`OUTSIDE_LABEL_MIN_PX = 60` (modül sabiti). Gerekçe: `fmtNs` en uzun
hâli ~7 karakter monospace 10px ≈ 48px + 4px boşluk + pay.

Render (`:632-637`):
```tsx
<span className={`wf-bar-label-outside${labelLeft ? ' wf-bar-label-outside-left' : ''}`}
      style={{ left: labelLeft
        ? `calc(${startPct}% - 4px)`
        : `calc(${startPct}% + ${widthPct}% + 4px)` }}>
```
`translate(-100%, …)` etiketin **sağ kenarını** bar başlangıcının 4px
soluna oturtur.

`containerWidth` başlangıçta `0` (`:211`) → ilk render'da `barAreaWidth=0`
→ `spaceLeftPx = spaceRightPx = 0` → `labelLeft = false` → **bugünkü
davranış**. ResizeObserver ölçtükten sonra doğru tarafa geçer. Regresyon
yok.

### 3.3 `AggregatedStructure` — kapsam kararı

Aynı bug, aynı kod (`:157`, `:161-165`), farklı eşik
(`labelInside = widthPct > 18`, `:93`).

| | Madde 1 (CSS ellipsis) | Madde 2 (sola çevirme) |
|---|---|---|
| Uygulanır mı? | ✅ **Otomatik** — aynı class | ❌ Doğrudan değil |
| Neden | Paylaşılan `.wf-bar-label` | `containerWidth` **yok**; `colWidth` sabit (`NAME_COL_WIDTH`, `:69`), ResizeObserver yok → piksel alanı bilinmiyor |

**Öneri:** bu release madde 1'i her ikisine verir (bedava kazanç, aynı
kusur), madde 2'yi **yalnız TraceWaterfall**'a uygular.
`AggregatedStructure`'a ResizeObserver eklemek ayrı bir kalem — istenirse
`kuyruk`'a. Bu, "hiçbir durumda eskisinden kötü olmasın" kuralını
bozmuyor: orada davranış aynen kalıyor, sadece ellipsis kazanıyor.

## 4. Değişecek dosyalar

| Dosya | Değişiklik |
|---|---|
| `frontend/src/styles/globals.css` | `:784`'e `text-overflow: ellipsis; min-width: 0;`; `:785`'ten **sonra** `.wf-bar-label-outside-left` |
| `frontend/src/components/TraceWaterfall.tsx` | `OUTSIDE_LABEL_MIN_PX` sabiti; `:510` civarına taraf hesabı; `:632-637` className + `left` |

**Backend yok. Tip/API değişikliği yok. Yeni bağımlılık yok.**
`AggregatedStructure.tsx` **düzenlenmiyor** — CSS'ten faydalanıyor.

## 5. Doğrulama

### Otomatik
```
cd frontend && npx tsc --noEmit      # tip kapısı (CLAUDE.md ship listesi #9)
cd frontend && npx eslint src        # mevcut durum: TraceWaterfall.tsx'te
                                     # 1 uyarı (react-refresh, :84 svcColorToken),
                                     # 0 hata. Bu sayı ARTMAMALI.
```
Not: taraf kararı saf bir fonksiyona çıkarılıp vitest'lenebilir, ama
`.tsx`'ten export etmek yukarıdaki react-refresh uyarısını büyütür.
Gerekirse ayrı modül (`traceWaterfall.labels.ts`). **Bu release için
önermiyorum** — mantık üç satır ve asıl risk CSS tarafında; testin
yakalayacağı şey az.

### Manuel (`/trace/<id>`, deploy sonrası)
1. **Kesilme gitti mi:** dar ama `labelInside` eşiğini geçen bir bar bul
   (%6-10 arası) → süre metni artık "…" ile bitiyor, karakter ortasından
   kesilmiyor.
2. **Sona yakın kısa span:** trace'in son işlemine bak (`startPct+widthPct
   ≈ 100`) → etiket artık bar'ın **solunda**, görünür.
3. **Regresyon — başa yakın kısa span:** `startPct ≈ 0` olan bir span →
   etiket **sağda** kalmalı (sol yer yok; `spaceLeftPx > spaceRightPx +
   60` tutmaz).
4. **Regresyon — ortadaki span:** her iki taraf ferah → sağda kalmalı.
5. **Name kolonunu genişlet** (resize handle) → `colWidth` büyüyünce
   `barAreaWidth` daralır → daha çok etiket sola çevrilir, hiçbiri
   kesilmez.
6. **`AggregatedStructure`** (`/services/<svc>` → Structure): etiketler
   ellipsis'li; taraf davranışı **değişmemiş** olmalı.
7. **Tema:** `.wf-bar-label-outside` `var(--text2)` kullanıyor; sola
   çevrilen etiket dark/light/redhat üçünde de okunur olmalı.

### Kapı
`npx tsc --noEmit` → `npx eslint src` → `npm test` → `go build ./...` →
`go test ./...` → `make audit` → commit → tag → push → deploy.

## 6. Açık sorular — operatör

1. **`- 8` fudge'ı kalsın mı?** Kutu modeli `containerWidth - colWidth`
   diyor. Öneri: fudge'sız, eşik `OUTSIDE_LABEL_MIN_PX = 60`'ta açık.
2. **`AggregatedStructure`'a da sola-çevirme istiyor musun?**
   ResizeObserver gerektirir → ayrı kalem önerisi.
3. **Eşik 60px** yeterli mi, yoksa `fmtNs` uzunluğuna göre dinamik mi
   (`text.length * 6 + 8`)? Dinamik daha isabetli ama üç satır daha.

## Öneri

§3.1 + §3.2 tek release (`v0.8.53X`). İki dosya, backend'e dokunmuyor,
`containerWidth=0` ilk render'da bugünkü davranışa düşüyor, sol taraf
yetersizse mevcut konumda kalıyor — "asla daha kötü değil" garantisi
kod seviyesinde sağlanıyor. Asıl incelik CSS kaynak sırası (§3.1 uyarısı);
manuel maddeler 2-4 onu izole ediyor.

**Onay bekliyor — implementasyona geçilmedi.**
