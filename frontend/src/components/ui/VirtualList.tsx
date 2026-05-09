import { useRef, type ReactNode } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';

// VirtualList — windowed scroller for any flat row collection.
// Replaces `items.map(...)` in long lists so only the visible
// rows touch the DOM. At ~30 rows visible × ~50 rendered (with
// overscan), a 30k-row live-tail stays buttery instead of
// freezing the browser.
//
// Why the prop API is so narrow: 90% of our long-list use cases
// just need (items, rowHeight, renderRow). Tables with sticky
// header columns or dynamic row heights need the lower-level
// useVirtualizer hook directly — we expose that as the escape
// hatch via `import from '@tanstack/react-virtual'` rather than
// plumbing 15 callbacks through this wrapper.

export interface VirtualListProps<T> {
  items: T[];
  // Estimated row height — used by the virtualizer to size the
  // empty space above/below visible rows. Match the actual row
  // height as closely as possible; off-by-2x scroll-bar lurches
  // when the user scrolls fast.
  rowHeight: number;
  // Pixel height of the scroll container. Required because we
  // can't size against a parent flex/grid without JS.
  height: number | string;
  renderRow: (item: T, index: number) => ReactNode;
  // overscan: how many extra rows above/below the visible window
  // to render preemptively, so quick scrolls don't show blank
  // space. Default 8 covers most ergonomic scroll speeds.
  overscan?: number;
  className?: string;
  // Stable key getter so React reconciliation doesn't churn when
  // the item array changes order. Default uses item index, which
  // is fine for stable lists but breaks for live-prepended ones
  // (Logs live-tail) — pass a `getKey` returning the row id then.
  getKey?: (item: T, index: number) => string | number;
}

export function VirtualList<T>({
  items, rowHeight, height, renderRow, overscan = 8, className, getKey,
}: VirtualListProps<T>) {
  const parentRef = useRef<HTMLDivElement>(null);

  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => rowHeight,
    overscan,
    getItemKey: getKey ? (i) => getKey(items[i], i) : undefined,
  });

  const totalSize = virtualizer.getTotalSize();
  const virtualRows = virtualizer.getVirtualItems();

  return (
    <div
      ref={parentRef}
      className={className}
      style={{
        height,
        overflow: 'auto',
        position: 'relative',
        contain: 'strict', // tells the browser this subtree is independent — better paint perf
      }}>
      {/* Spacer: total height = virtualSize so the scrollbar matches the full list. */}
      <div style={{ height: totalSize, width: '100%', position: 'relative' }}>
        {virtualRows.map(vr => (
          <div
            key={vr.key}
            data-index={vr.index}
            ref={virtualizer.measureElement}
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              width: '100%',
              transform: `translateY(${vr.start}px)`,
            }}>
            {renderRow(items[vr.index], vr.index)}
          </div>
        ))}
      </div>
    </div>
  );
}
