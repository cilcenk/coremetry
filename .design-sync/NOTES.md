# design-sync notes — Coremetry UI (paket şekli)

- `coremetry-ui` bir Vite UYGULAMASI; kütüphane çıktısı `npm run build:ds`
  (frontend/vite.lib.config.ts + tsconfig.ds.json) → `frontend/dist-ds/`
  (`index.es.js` + `types/` + `index.d.ts` yeniden-export). React/router/
  lucide/react-virtual/uplot/react-query dış bağımlılık.
- `package.json.types = dist-ds/index.d.ts` — converter `exportedNames`
  .d.ts girişini `types` alanından bulur; alan yokken `[ZERO_MATCH]` (tokens-only).
- `useConfirm` hook `componentSrcMap: null` ile dışlandı (bileşen değil).
- CSS: tek dosya `src/styles/globals.css` (`cssEntry`); dark varsayılan
  `:root`, `[data-theme="light"|"redhat"]` token yeniden tanımları.
- Font: `Red Hat Text` yalnız redhat temasında ve LOKAL kuruluysa
  (fallback zinciri) — `[FONT_MISSING]` uyarısı. Operatör woff2 ekleyeceğini
  söyledi (2026-08-29); dosyalar gelince `cfg.extraFonts` ile taşınacak.
  Gelene dek uyarı bilinçli açık.
- Chromium: `~/Library/Caches/ms-playwright/chromium_headless_shell-1234`
  (.ds-sync içine `playwright` kuruldu).

- Önizleme yardımcıları `previews/` DIŞINDA durur: `preview-lib/frame.tsx`
  (`Frame`) — `previews/_frame.tsx` converter'da "stale preview" sayılıp
  Button/Field notlarını sıfırlamıştı (2026-08-29).
- `frontend/vite.lib.config.ts`: `copyPublicDir: false` — kütüphane build'i
  aksi hâlde `public/` varlıklarını dist-ds'e kopyalıyor.
- Seri renk token'ları yalnız `--purple --indigo --orange --teal`;
  `--green` YOK (conventions.md'den çıkarıldı, doğrulama betiği:
  token/sınıf/bileşen adlarını `_ds_bundle.css` + build'e karşı grep).
- `readmeHeader: .design-sync/conventions.md` — README başlığı; adları
  yeniden doğrulamadan düzenleme.

## Known render warns
- RouteSkeleton `useLocation()` → `cfg.extraEntries: ["react-router-dom"]`
  (2026-08-29): bundle kendi react-router kopyasını inline'lıyor; önizlemenin
  node_modules'tan aldığı ikinci kopya farklı LocationContext → "may be used
  only in the context of a <Router>". extraEntries aynı esbuild grafiği →
  tek modül, `MemoryRouter` global'de. Yan etki: react-router export adları
  `exported` kümesine girer (Link/Form/Route/Await adlı repo dosyası
  global'e shim'lenir; ui/ altında böyle atom yok).
- Overlay kartları `cfg.overrides.<Name> = {cardMode:"single", primaryStory}`:
  Modal (Confirm), Drawer (EntityDetail), ConfirmProvider (Destructive) —
  grid kartta portal'lı hücreler üst üste biner. Yakalamalar etkilenmez.
- PageControls ≤640px daraltılmış hâli (useIsNarrow → matchMedia) kartta
  YOK — bilinçli: desktop düzeni birincil; dar hâl için `overrides.PageControls
  = {cardMode:"single", primaryStory:"ListFilterBar", viewport:"600x400"}`
  desktop hücrelerini feda ederdi.
- 900×700 yakalama viewport'u globals.css'in tablet bandında: `.grid-3/4/5`
  2 kolona çöker → önizlemede desktop kompozisyon için inline
  `gridTemplateColumns: repeat(N, minmax(0,1fr))`.
- `Frame` zemini `--bg`; sayfa gövdesi `--bg0` — PageShell gibi sayfa
  önizlemeleri `style={{padding:0, background:'var(--bg0)'}}`.

## Önizleme yazım kuralları (dalga-1 dersleri, learnings/A-D.md tam metin)
- Tipler: `MenuItem` → `Menu.d.ts`; `Row` → `Stack.d.ts` (ayrı .d.ts yok).
  Row/Stack gap basamağı 1|2|3|4|6.
- `Badge tone="accent"` dolgusuz düz metin (DS gerçeği, tüketici yok).
- Chrome'suz atomlar (IconButton ghost/bare, LinkButton, Chip sarmalayıcı)
  boş satırda görünmez → tek satırlık gerçek bağlamda göster.
- `DisclosureButton anatomy="section"` Card içinde `style={{padding:0}}`.
- SearchField: değer + `hint` + odaksız → kbd ile ✕ üst üste; statik hücrede
  ikisini birleştirme.
- `.controls` margin-bottom 14px → Frame içinde `marginBottom:0`.
- `useDataTable` (`@/components/DataTable`) önizlemeden import edilir
  (shim yalnız export adlı dosyalar); react-router `useSearchParams` için
  hücreyi `<MemoryRouter>` ile sar. Sıralanabilir başlık bütçesi ≈
  7px/karakter + 36px; öncü 30px chevron td `padding:'9px 0'`.
- FacetMultiSelect açık hâl: mount'ta `.fsel-btn.click()` + 60ms sonra
  `.fsel-row.focus()` (:focus-visible `sadece` düğmesini açar).
- Üretilen satırlar seeded PRNG (mulberry32); hücre başına benzersiz
  `storageKey` (localStorage aynı origin'de kalıcı).
- İkon: unicode glif (⋯ ⟳ ✕ ☆ ★ ⧉ ▸ ▾); emoji token dışı render.
- DS boşlukları (önizleme kusuru değil): `select:disabled`/`input:disabled`
  taban kuralı yok; çıplak `textarea` taban kuralı yok (UA chrome).
- Hücre etiketi = PascalCase export adı; grade.json anahtarları birebir.

## Re-sync risks
- `dist-ds/` gitignore'da: her senkron `cd frontend && npm run build:ds`
  ile yeniden üretilir (buildCmd).
