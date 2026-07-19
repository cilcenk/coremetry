import { fmtSmart } from '@/lib/chartFmt';

// tooltipModel (v0.9.101, Grafana-parity Adım 1) — the pure model behind the
// "all series" hover tooltip every uPlot panel shows. Grafana's time-series
// tooltip lists every series' value at the hovered x, SORTED so the hottest
// line reads first, each value formatted through one unit-aware formatter.
// This is the DOM-free core of that: the preset (OverviewChart / TimeChart /
// MultiLineChart / TimeSeriesPanel) gathers each series' raw value at the
// cursor index and hands them here; we drop the empty ones, sort, and format
// via the shared fmtSmart. Presets render whatever HTML they like from the
// returned rows (they differ: `.ov-tt` classes vs inline styles), but the
// ordering + number formatting are now identical everywhere — and pinned by a
// vitest table (tooltipModel.test.ts) instead of re-implemented per panel.

export interface TooltipItem {
  label: string;
  color: string;
  // Raw value at the hovered x for this series (null/absent = gap → dropped).
  value: number | null | undefined;
  // Per-series unit (dual-axis panels pass different units per series), fed to
  // fmtSmart: 'ms' → "234ms", 'B' → "1.2 MB", '%' → "3.4%", '' → "1.23k".
  unit?: string;
}

export interface TooltipRow {
  label: string;
  color: string;
  // fmtSmart(value, unit) — the human-readable string the preset prints.
  text: string;
  // Raw value kept for callers that highlight the row nearest the cursor Y.
  value: number;
}

// sortedTooltipRows — drop null/non-finite values (a gap in one series must not
// push down the others), then sort by value DESC by default so the largest
// line is on top (matches MultiLineChart's long-standing tooltip). Array.sort
// is stable (ES2019+), so equal values keep input order → a steady 30s poll
// never reshuffles rows. `sort:'none'` preserves the caller's series order for
// panels that want it (e.g. fixed p50/p95/p99 ladders).
export function sortedTooltipRows(
  items: TooltipItem[],
  sort: 'desc' | 'none' = 'desc',
): TooltipRow[] {
  const rows: TooltipRow[] = [];
  for (const it of items) {
    if (it.value == null || !isFinite(it.value)) continue;
    rows.push({
      label: it.label,
      color: it.color,
      value: it.value,
      text: fmtSmart(it.value, it.unit),
    });
  }
  if (sort === 'desc') rows.sort((a, b) => b.value - a.value);
  return rows;
}
