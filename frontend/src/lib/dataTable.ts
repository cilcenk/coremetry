// dataTable — the pure, testable core of Coremetry's shared
// sortable + resizable table primitive (v0.7.53). The project
// principle: EVERY data table is column-sortable and
// column-resizable. The React glue lives in
// components/DataTable.tsx; the sort math + click semantics live
// here so they're unit-tested in isolation (CLAUDE.md #11), the
// same way tableColumns.ts backs the Traces reorder feature.

export type SortDir = 'asc' | 'desc';

// DataTableColumn describes one column for the shared primitive.
// A column with no `sortValue` is not clickable-to-sort (e.g. an
// expand-chevron or an actions column); it still participates in
// the fixed layout + resize.
export interface DataTableColumn<T> {
  id: string;
  label: string;
  // Accessor used for client-side sorting. Omit → column isn't sortable.
  sortValue?: (row: T) => number | string | null | undefined;
  // Direction applied when this column is FIRST clicked. Defaults to
  // 'desc' (numeric tables want biggest-first); set 'asc' for names.
  naturalDir?: SortDir;
  align?: 'left' | 'right';
  // Apply the .num class (right-align + tabular-nums). Implies right
  // align unless `align` overrides.
  numeric?: boolean;
  // Default column width (px) for the fixed-layout colgroup + the
  // resize starting point.
  width?: number;
  // Resize floor (px).
  minWidth?: number;
  // Sortable-only dimension that is NOT rendered as a header/column —
  // e.g. a composite "impact" score a preset button sorts by. Excluded
  // from <DataTableHead>/<DataTableColgroup>; still resolvable by
  // sortRows + setSort. Keeps body cells aligned to visible headers.
  headerHidden?: boolean;
  // Pin this column to the RIGHT edge of the scroll container
  // (v0.8.573 — Endpoints "Traces"). Wide tables overflow .table-wrap's
  // horizontal scroll and the scrollbar sits below the rows, so a
  // trailing action column is effectively invisible on laptop widths.
  // DataTableHead emits the .sticky-right th; the caller must mirror
  // the class on the matching body <td> (and give it an OPAQUE
  // background when the row carries an inline tint — sticky cells
  // float over scrolled content). Only meaningful on the LAST visible
  // column: multiple pinned columns would need per-column right
  // offsets, which nothing needs yet.
  stickyRight?: boolean;
  // flex — v0.9.542. Bu kolon ARTAN genişliği emer; diğerleri kendi
  // genişliğinde kalır.
  //
  // Neden gerekti (operatör: "boşluklar güzel durmuyor, fit olsun"):
  // table-layout:fixed, kolon genişlikleri toplamı tablodan darsa
  // artanı TÜM kolonlara orantılı dağıtır. Geniş ekranda bu, her
  // kolonun arasına birkaç on piksel boşluk serpiyor ve tablo
  // "dağılmış" görünüyor — v0.9.501'de Trend kolonunda yaşanan sınıfın
  // aynısı. Tek bir kolon esner ve artan oraya giderse düzen kasıtlı
  // görünür.
  //
  // Emici kolon içerik olarak EN ÇOK yer isteyen olmalı (Traces'te
  // Operation). Operatör o kolonu elle sürüklerse kalıcı genişlik
  // kazanır ve esneklik biter — beklenen davranış, sürüklenen genişlik
  // her zaman kazanır.
  flex?: boolean;
  // mobileHide — v0.9.988 (dar ekran denetimi D6). Telefon
  // genişliğinde (<640px) bu kolon düşer.
  //
  // OPT-IN ve VARSAYILAN false. 68 tablo bu primitifi besliyor; bir
  // "akıllı" varsayılan (ör. "numerik olmayan üçüncü kolondan sonrası
  // düşsün") masaüstünde kimsenin istemediği kolonları kaybettirirdi.
  // Kolonun düşüp düşmeyeceği bir ÜRÜN kararıdır, tabloyu tanıyan
  // sayfa verir.
  //
  // KAPSAM SINIRI — bu bayrak `<colgroup>` ve `<thead>`i kapsar
  // (DataTableColgroup/DataTableHead `visibleColumns`tan okur). GÖVDE
  // hücrelerini sayfa basıyor: bir kolonu işaretleyen sayfa kendi
  // `<td>`sini de `dt.visibleColumns`/`dt.narrow` üzerinden atlamak
  // ZORUNDA, yoksa hücreler bir kolon kayar. Bugün hiçbir kolon
  // işaretli değil, yani bu sürümde davranış değişikliği YOK — mekanizma
  // kuruldu, ilk tüketici (log tablosunun kolon alt-kümesi, D5.7'nin
  // ikinci yarısı) kendi sürümünde gelecek.
  mobileHide?: boolean;
}

// visibleColumns — `<colgroup>`/`<thead>`de GERÇEKTEN çizilen kolonlar.
//
// İki süzgeç tek yerde: `headerHidden` (sıralanabilir ama çizilmeyen
// bileşik boyut) ve `mobileHide` (yalnız dar ekranda düşen kolon).
// Saf ve tablo-güdümlü test edilebilir olması için burada; React
// yapıştırıcısı components/DataTable.tsx'te.
//
// Genişlik-kalıcılığı imzasına (columnLayoutSig) BİLEREK girmiyor:
// imza "beyan edilen GENİŞLİK niyeti"ni damgalıyor ve bir kolonun dar
// ekranda düşmesi onun masaüstü genişliğini değiştirmiyor. İmzaya
// eklemek, hiçbir kolon işaretli olmasa bile her tablonun sürüklenmiş
// genişliklerini bu sürümde bir kez atardı — bedava regresyon.
export function visibleColumns<T>(
  columns: DataTableColumn<T>[],
  narrow: boolean,
): DataTableColumn<T>[] {
  return columns.filter(c => !c.headerHidden && !(narrow && c.mobileHide));
}

export interface SortState {
  id: string | null;
  dir: SortDir;
}

// nextSort encodes the click semantics shared by every Coremetry
// table (matches the long-standing Services.tsx toggleSort): clicking
// the active column flips direction; clicking a new column selects it
// at its natural starting direction.
export function nextSort<T>(cur: SortState, col: DataTableColumn<T>): SortState {
  if (cur.id === col.id) {
    return { id: col.id, dir: cur.dir === 'desc' ? 'asc' : 'desc' };
  }
  return { id: col.id, dir: col.naturalDir ?? 'desc' };
}

// compareValues — type-aware comparator for two PRESENT (non-null)
// values, returning the pre-direction ordering. Numbers compare
// numerically; everything else via locale string compare. Null handling
// lives in sortRows (nulls are direction-independent — always last).
function compareValues(a: number | string, b: number | string): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  return String(a).localeCompare(String(b));
}

// sortRows — STABLE client-side sort by a column's accessor, returning
// a NEW array (never mutates input). Two invariants:
//  • Stability — re-sorting by a column with many ties (e.g. all the
//    same db_system) preserves the server's original order within each
//    group (tiebreak on original index).
//  • Nulls last — null/undefined accessor values sink to the BOTTOM
//    regardless of direction (missing data shouldn't jump to the top
//    when sorting ascending; matches the SLO list's intent).
// Returns the input unchanged when the column is absent or non-sortable.
export function sortRows<T>(
  rows: T[],
  col: DataTableColumn<T> | undefined,
  dir: SortDir,
): T[] {
  if (!col || !col.sortValue) return rows;
  const acc = col.sortValue;
  const mul = dir === 'asc' ? 1 : -1;
  return rows
    .map((r, i) => ({ r, i, v: acc(r) }))
    .sort((x, y) => {
      const a = x.v;
      const b = y.v;
      if (a == null && b == null) return x.i - y.i;
      if (a == null) return 1;  // x is null → sinks below y
      if (b == null) return -1; // y is null → x stays above
      const c = compareValues(a, b) * mul;
      return c !== 0 ? c : x.i - y.i; // stable tiebreak on original index
    })
    .map(x => x.r);
}

// ---------------------------------------------------------------------------
// v0.8.251 — serverSort support. The hook (components/DataTable.tsx) gained
// an optional serverSort mode for server-paged tables (Services first): the
// sort STATE machinery — URL `s_<storageKey>` param, localStorage
// persistence, header click semantics — is shared with client mode, but the
// ordering itself is the backend's ORDER BY. The pure halves live below so
// both modes stay unit-tested in the node vitest harness; nextSort/sortRows
// above are untouched (contract unchanged) — mode selection COMPOSES them.

// parseSortParam — decode the URL sort param "<colId>.<dir>" → SortState.
// Returns null for a missing / malformed value so the caller falls back to
// localStorage. colId may itself contain dots, so split on the LAST one.
// (Moved here from components/DataTable.tsx in v0.8.251 so the URL codec
// round-trip is pinned by unit tests.)
export function parseSortParam(s: string | null): SortState | null {
  if (!s) return null;
  const i = s.lastIndexOf('.');
  if (i <= 0) return null;
  const dir = s.slice(i + 1);
  if (dir !== 'asc' && dir !== 'desc') return null;
  return { id: s.slice(0, i), dir: dir as SortDir };
}

// formatSortParam — SortState → "<colId>.<dir>" URL value; null when no
// column is active (the hook deletes the param instead of writing it).
// Inverse of parseSortParam for every non-empty id, including ids that
// themselves contain dots.
export function formatSortParam(s: SortState): string | null {
  return s.id ? `${s.id}.${s.dir}` : null;
}

// resolveToggle — the pure half of the hook's toggleSort: look up the
// clicked column and produce the next sort state, or null when the column
// is unknown / not sortable (the hook no-ops and onSortChange never fires).
// Mode-independent: in serverSort mode this exact value is what the hook
// reports via onSortChange / the returned `sort`, so the page re-fetches
// with the new ORDER BY.
export function resolveToggle<T>(
  columns: DataTableColumn<T>[],
  cur: SortState,
  id: string,
): SortState | null {
  const col = columns.find(c => c.id === id);
  if (!col || !col.sortValue) return null;
  return nextSort(cur, col);
}

// computeSortedRows — the row pipeline behind the hook's sortedRows memo.
// serverSort=true returns `rows` VERBATIM (reference-equal): the backend
// already applied its ORDER BY, and any client-side reorder — or even a
// defensive copy — would contradict the server page and defeat memoized
// children. Client mode is the existing sortRows path, contract unchanged.
export function computeSortedRows<T>(
  rows: T[],
  columns: DataTableColumn<T>[],
  sort: SortState,
  serverSort: boolean,
): T[] {
  if (serverSort) return rows;
  const col = sort.id ? columns.find(c => c.id === sort.id) : undefined;
  return sortRows(rows, col, sort.dir);
}

// columnLayoutSig — kalıcı kolon genişliklerinin HANGİ kolon tanımına
// karşı yakalandığını damgalar (v0.9.695).
//
// Operatör-bildirimi: "Manage users sayfasında user tablosunun kolonları,
// ilk header kullanıcının üzerinde çıkıyor."
//
// KÖK NEDEN — v0.9.660'ın kendi düzeltmesi operatöre HİÇ ULAŞMADI.
// O sürümde Users kolonları yeniden tanımlandı (team: flex → sabit 145,
// email tek emici). Ama DataTableColgroup şunu yapıyor:
//
//     const w = dt.colWidths[c.id] ?? (c.flex ? 'auto' : c.width ?? …)
//
// `colWidths` localStorage'dan (`dt.<key>.widths`) geliyor ve SÜRÜMSÜZDÜ.
// Kolonları bir kez sürüklemiş bir tarayıcıda ESKİ harita yeni tanımı
// tamamen eziyor: düzeltilmiş bütçe hiç uygulanmıyor, tablo eskisi gibi
// bozuk kalıyor. Kod doğru, ekran yanlış — ve `git log` düzeltmenin
// gemide olduğunu söylediği için bu tuzak sessiz.
//
// v0.9.660 bu katmanı BİLİYORDU ("Users tablosunun kaymasında bu ikinci
// katmandı") ama yalnız elle basılan bir Reset butonu ekledi. Kaçış
// kapısı, düzeltme değil: operatörün önce butonu keşfetmesi gerekiyor.
//
// İMZA NEYİ KAPSIYOR: id + beyan edilen genişlik niyeti (width/flex/
// minWidth) + headerHidden. Kolon EKLENMESİ/ÇIKMASI kadar bir kolonun
// genişlik niyetinin DEĞİŞMESİ de imzayı değiştiriyor — operatörün
// vakasında değişen tam olarak buydu, id kümesi aynı kalmıştı.
//
// TAKAS, açıkça: beyan edilen bir genişliği değiştirdiğimde o tablonun
// sürüklenmiş genişlikleri sıfırlanır. İSTENEN bu — bir genişliği
// elle değiştirmemin nedeni neredeyse her zaman düzenin taşmasıdır ve
// tam o durumda bayat sürükleme GİTMELİ. Kalıcı düzen yalnız yakalandığı
// kolon kümesine göre anlamlıdır.
export function columnLayoutSig<T>(columns: DataTableColumn<T>[]): string {
  // FNV-1a — cache anahtarlarındaki ile aynı aile (uzunluk değil, içerik).
  let h = 0x811c9dc5;
  for (const c of columns) {
    const part = `${c.id}:${c.width ?? ''}:${c.flex ? 'f' : ''}:${c.minWidth ?? ''}:${c.headerHidden ? 'h' : ''};`;
    for (let i = 0; i < part.length; i++) {
      h ^= part.charCodeAt(i);
      h = Math.imul(h, 0x01000193);
    }
  }
  return (h >>> 0).toString(36);
}

// PersistedLayout — diskteki şekil. `sig` uyuşmazsa genişlikler ATILIR.
//
// ESKİ ŞEKİL (çıplak Record<string, number>) bilinçli olarak atılıyor:
// tam da onu geçersiz kılmak için bu imza yazıldı, taşımak amacı
// bozardı (CLAUDE.md: kaldırırken geriye-uyum şimi ekleme).
export interface PersistedLayout {
  sig: string;
  widths: Record<string, number>;
}

export function readPersistedWidths(
  stored: unknown,
  sig: string,
): Record<string, number> {
  if (!stored || typeof stored !== 'object') return {};
  const p = stored as Partial<PersistedLayout>;
  if (typeof p.sig !== 'string' || p.sig !== sig) return {};
  if (!p.widths || typeof p.widths !== 'object') return {};
  return p.widths as Record<string, number>;
}

// ── fitColumnWidths — v0.9.1030 ─────────────────────────────────────────
//
// Operator-reported: /inbox'ta Assignee tarafında tablo "bozuluyor",
// sayfa iframe gibi görünüyor. Kök: `.table-wrap.is-fit` masaüstünde
// `overflow: visible` (D2.1'in yapışkan-başlık kazancı) ve
// `table-layout:fixed` bir tablo, kolon genişliklerinin TOPLAMI
// `width:100%`ü aşarsa yine de büyür. Sürüklenen genişlikler px olarak
// kalıcı olduğundan geniş monitörde kaydedilen düzen dar laptopta
// toplam > kap eder; taşma is-fit kabında değil `#content`te kaydırma
// üretir — sol kenar kırpılır, sayfa "iframe" gibi iç-kaydırmalı olur
// (v0.9.660 Users taşmasının yapısal hâli; o gün gelen çare yalnız
// reset butonuydu).
//
// Sözleşme: kap yatay kaydırMIYORsa (is-fit, masaüstü) beyan+kalıcı px
// genişlikler kabı AŞAMAZ. Aşan küme, minWidth tabanlarına saygılı
// oransal küçültmeyle kaba sığdırılır. Sığıyorsa null döner — çağıran
// beyan edilen genişlikleri AYNEN kullanır (davranış değişmez).
// Ölçüm yoksa (containerPx ≤ 0: jsdom, ilk mount) null = fail-open.

export interface FitColumnInput {
  id: string;
  /** Beyan/kalıcı px; sürüklenmemiş flex kolon için null ('auto'). */
  px: number | null;
  /** Küçültme tabanı (resize'ın kullandığı minWidth ?? DEFAULT_MIN). */
  min: number;
}

export function fitColumnWidths(
  cols: FitColumnInput[],
  fixedExtraPx: number,
  containerPx: number,
): Record<string, number> | null {
  if (!(containerPx > 0)) return null;
  // 'auto' kolonlar kalan alanı alır ama en az min ister — o payı ayır.
  const flexMin = cols.reduce((s, c) => s + (c.px == null ? c.min : 0), 0);
  const avail = containerPx - fixedExtraPx - flexMin;
  const fixed = cols.filter(c => c.px != null) as (FitColumnInput & { px: number })[];
  const sum = fixed.reduce((s, c) => s + c.px, 0);
  if (sum <= avail) return null;
  // Tabanlar tek başına sığmıyorsa yapılabilecek en iyi şey tabanlar:
  // taşma sınırlı kalır ve ≤1024 bandında D2'nin kaydırma ağı devreye
  // girer zaten.
  const minSum = fixed.reduce((s, c) => s + c.min, 0);
  const out: Record<string, number> = {};
  if (minSum >= avail) {
    for (const c of fixed) out[c.id] = c.min;
    return out;
  }
  // Oransal küçültme, taban kilitlemeli (waterfall): tabana çarpan
  // kolon kilitlenir, kalan pay kilitsizlere yeniden oranlanır.
  const locked = new Set<string>();
  for (;;) {
    const freePx = fixed.reduce((s, c) => s + (locked.has(c.id) ? 0 : c.px), 0);
    const target = avail - fixed.reduce((s, c) => s + (locked.has(c.id) ? c.min : 0), 0);
    const f = target / freePx;
    let relocked = false;
    for (const c of fixed) {
      if (locked.has(c.id)) continue;
      if (c.px * f < c.min) { locked.add(c.id); relocked = true; }
    }
    if (!relocked) {
      for (const c of fixed) out[c.id] = locked.has(c.id) ? c.min : Math.floor(c.px * f);
      return out;
    }
  }
}
