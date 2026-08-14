import { useEffect, useRef, useState } from 'react';
import { useShortcuts, type Shortcut } from './keyboard';

// useTableNav adds Vim/Datadog-style row navigation to any
// table-like list page:
//
//   j   move selection down
//   k   move selection up
//   gg  jump to first row    (g pressed twice in sequence — handled
//                              via the global keyboard subsystem)
//   G   jump to last row
//   Enter / o   open the selected row (calls onOpen)
//   Esc clear selection
//
// The hook owns the selected-index state. Pages render a
// .row-selected CSS class on the matching row to surface the
// selection visually. Auto-scrolls the selected row into view
// when navigation moves it offscreen.
//
// Items can change (filter / refresh / search). When the new
// list is shorter than the prior selection, we clamp; when it
// changes identity, we keep the index (operator's mental
// "I was on row 5" stays consistent across a refresh).

// navStep — j/k'nın SINIRDA ne yapacağı. Saf ve ayrı, çünkü çivilenmesi
// gereken karar tam olarak bu: listenin son satırında `j`, sayfalanmış bir
// yüzeyde "hiçbir şey" DEĞİL "sonraki sayfa" demeli. v0.9.1018'e kadar
// klavye gezinmesi sayfanın sonunda sessizce duruyordu — operatör 50.
// satırda j'ye basıp basıp hiçbir şey olmamasını izliyordu, oysa fare ile
// üç satır aşağıda bir "Next" butonu vardı. Klavye yolu, fare yolunun
// yapabildiğini yapamıyordu.
//
// Sınır YALNIZ gerçekten sınırdayken bildiriliyor: boş listede (count 0)
// ne hareket var ne sınır — orada j/k'nın sayfa çevirmesi, operatörün
// göremediği bir veri kümesinde körlemesine gezinmek olurdu.
export type NavStep =
  | { kind: 'move'; to: number }
  | { kind: 'boundary'; dir: 'next' | 'prev' }
  | { kind: 'none' };

export function navStep(selected: number, count: number, dir: 'down' | 'up'): NavStep {
  if (count <= 0) return { kind: 'none' };
  if (dir === 'down') {
    // Seçim yokken (-1) ilk j ilk satırı seçer, sayfa çevirmez.
    if (selected < 0) return { kind: 'move', to: 0 };
    if (selected >= count - 1) return { kind: 'boundary', dir: 'next' };
    return { kind: 'move', to: selected + 1 };
  }
  if (selected < 0) return { kind: 'move', to: 0 };
  if (selected === 0) return { kind: 'boundary', dir: 'prev' };
  return { kind: 'move', to: selected - 1 };
}

export interface TableNav<T> {
  selected: number;
  setSelected: (i: number) => void;
  selectedItem: T | null;
  // v0.9.928 — tablonun kimliği (options.pageId), tüketiciye GERİ verilir.
  // Kendi <tr>'sini basan sayfalar (Services, LogTable) kimliği buradan
  // alıp `data-table-id` olarak damgalıyor; ayrı bir prop uydurmak iki
  // kimlik kaynağı yaratır ve biri sessizce bayatlar.
  pageId?: string;
}

export function useTableNav<T>(
  items: T[],
  options: {
    onOpen?: (item: T, index: number) => void;
    // pageId scopes the bindings to a single page so multiple
    // list pages mounted simultaneously (a future side-by-side
    // layout) don't fight over j/k.
    pageId?: string;
    // enabled=false registers NO key bindings (used when a table
    // opts out, or when useDataTable wires nav only because an
    // onOpen was supplied). Default true. (v0.7.129)
    enabled?: boolean;
    // v0.9.1018 — sayfa sınırı. Son satırda `j` / ilk satırda `k`
    // çağırır. Sayfa GERÇEKTEN değiştiyse true dönmeli; false dönerse
    // seçim yerinde kalır (son sayfada j hiçbir şey yapmaz — yalancı
    // bir "ilk satıra atladım" hareketi yapmaktansa durmak dürüst).
    // Opsiyonel: vermeyen tüm mevcut tablolar bit bit aynı davranır.
    onPageBoundary?: (dir: 'next' | 'prev') => boolean;
  } = {},
): TableNav<T> {
  const [selected, setSelected] = useState(-1);
  const enabled = options.enabled !== false;

  // Clamp the selection when the items shrink. Don't reset on
  // identity change — refresh that returns the same data
  // shouldn't lose the operator's place.
  useEffect(() => {
    if (selected >= items.length) {
      setSelected(items.length === 0 ? -1 : items.length - 1);
    }
  }, [items.length, selected]);

  // Auto-scroll: find the selected element by data-row-idx and
  // ensure it's in view. Cheap; only fires when `selected`
  // changes.
  useEffect(() => {
    if (selected < 0) return;
    // v0.9.926 — kapsamlı arama. Öncesinde `document.querySelector` ile
    // BELGEDEKİ İLK `[data-row-idx]` bulunuyordu: iki tablolu bir sayfada
    // j/k bir tabloda seçim yaparken kaydırma DİĞERİNDE oluyordu.
    // `pageId` tablonun kabına `data-table-id` olarak basılıyor.
    const sel = document.querySelector(
      options.pageId
        ? `[data-table-id="${options.pageId}"][data-row-idx="${selected}"]`
        : `[data-row-idx="${selected}"]`,
    ) as HTMLElement | null;
    if (sel) sel.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [selected, options.pageId]);

  // v0.9.1018 — sayfa sınırı makinesi.
  //
  // Seçim bir REF'ten okunuyor, setState güncelleyicisinden değil: sınır
  // geçişi bir YAN ETKİ (sayfa değiştirir) ve React güncelleyiciyi iki kez
  // çağırabilir (StrictMode) — sayfayı iki kez atlatırdı.
  const selRef = useRef(selected);
  useEffect(() => { selRef.current = selected; }, [selected]);

  // Sayfa değişince odak yeni sayfanın ilk/son satırına düşer. Ama
  // UYGULAMA yeni veri GELDİĞİNDE: sayfalar `keepPreviousData` ile
  // çalışıyor, yani `items` bir süre ESKİ diziyi taşıyor. Referans
  // değişimini beklemezsek seçimi eski listenin son satırına koyar,
  // sonra yeni liste gelir ve odak yanlış satırda kalır.
  const [pendingEdge, setPendingEdge] = useState<null | 'first' | 'last'>(null);
  const itemsRef = useRef(items);
  useEffect(() => {
    if (itemsRef.current === items) return;
    itemsRef.current = items;
    if (!pendingEdge) return;
    setSelected(pendingEdge === 'first' ? 0 : Math.max(0, items.length - 1));
    setPendingEdge(null);
  }, [items, pendingEdge]);

  const onBoundary = options.onPageBoundary;
  const step = (dir: 'down' | 'up') => {
    const next = navStep(selRef.current, items.length, dir);
    if (next.kind === 'move') { setSelected(next.to); return; }
    if (next.kind === 'none') return;
    // Sınır: sayfa gerçekten döndüyse odağı karşı uca hazırla.
    if (onBoundary?.(next.dir)) {
      setPendingEdge(next.dir === 'next' ? 'first' : 'last');
    }
  };

  const open = options.onOpen;
  // v0.9.928 — HER binding kapsamı taşır. Tek tek yazmak yerine map:
  // sekiz kaydın birinde `scope` unutulursa o tuş arbitrajın dışında
  // kalır ve "j çalışıyor ama Enter yanlış tabloyu açıyor" gibi yarım
  // bozuk bir durum doğar — en pahalı hata tipi.
  const scoped = (list: Shortcut[]): Shortcut[] =>
    options.pageId ? list.map(sc => ({ ...sc, scope: options.pageId })) : list;
  useShortcuts(
    !enabled ? [] : scoped([
      {
        keys: 'j',
        label: 'Move selection down',
        group: 'Lists',
        handler: () => step('down'),
      },
      {
        keys: 'k',
        label: 'Move selection up',
        group: 'Lists',
        handler: () => step('up'),
      },
      {
        // v0.9.949 (E1/Ö27) — TEK kayıt. Öncesinde hem 'G' hem 'shift+g'
        // kayıtlıydı ve İKİSİ de ölüydü: comboFromEvent Shift+G'yi 'g'ye
        // katlıyordu, yani ne 'G' ne 'shift+g' üretiliyordu. Üstelik 'g'
        // dizi öneki olduğu için Shift+G bir `g s`/`g t` sekansı
        // başlatıyordu (Ö27'nin ikinci yarısı).
        //
        // Katlama düzeldikten sonra ÜRETİLEN combo 'shift+g'dir; 'G'
        // kaydı ulaşılamaz olurdu, o yüzden kaldırıldı — çalışmayan bir
        // kısayolu yardım ekranında listelemek operatöre yalan söylemek.
        keys: 'shift+g',
        label: 'Jump to last row (Shift+G)',
        group: 'Lists',
        handler: () => setSelected(items.length > 0 ? items.length - 1 : -1),
      },
      {
        keys: 'g g',
        label: 'Jump to first row (gg)',
        group: 'Lists',
        handler: () => setSelected(items.length > 0 ? 0 : -1),
      },
      {
        keys: 'Enter',
        label: 'Open selected row',
        group: 'Lists',
        handler: () => {
          if (selected >= 0 && selected < items.length && open) {
            open(items[selected], selected);
          }
        },
      },
      {
        keys: 'o',
        label: 'Open selected row (o)',
        group: 'Lists',
        handler: () => {
          if (selected >= 0 && selected < items.length && open) {
            open(items[selected], selected);
          }
        },
      },
      {
        keys: 'Escape',
        label: 'Clear row selection',
        group: 'Lists',
        evenInInputs: true,
        handler: () => setSelected(-1),
      },
    ]),
    [items, selected, open, options.pageId],
  );

  return {
    selected,
    setSelected,
    selectedItem: selected >= 0 && selected < items.length ? items[selected] : null,
    pageId: options.pageId,
  };
}
