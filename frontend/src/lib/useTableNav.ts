import { useEffect, useState } from 'react';
import { useShortcuts } from './keyboard';

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

export interface TableNav<T> {
  selected: number;
  setSelected: (i: number) => void;
  selectedItem: T | null;
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
    const sel = document.querySelector(
      `[data-row-idx="${selected}"]`,
    ) as HTMLElement | null;
    if (sel) sel.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [selected]);

  const open = options.onOpen;
  useShortcuts(
    !enabled ? [] : [
      {
        keys: 'j',
        label: 'Move selection down',
        group: 'Lists',
        handler: () => setSelected(s => Math.min(items.length - 1, Math.max(0, s + 1))),
      },
      {
        keys: 'k',
        label: 'Move selection up',
        group: 'Lists',
        handler: () => setSelected(s => s <= 0 ? 0 : s - 1),
      },
      {
        keys: 'G',
        label: 'Jump to last row',
        group: 'Lists',
        handler: () => setSelected(items.length > 0 ? items.length - 1 : -1),
      },
      {
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
    ],
    [items, selected, open, options.pageId],
  );

  return {
    selected,
    setSelected,
    selectedItem: selected >= 0 && selected < items.length ? items[selected] : null,
  };
}
