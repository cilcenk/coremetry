import { useMemo } from 'react';
import type { SpanMetricSeries, MetricExemplar, ChartAnnotation } from '@/lib/types';
import type { TSSeries, TSThreshold } from '@/components/viz/TimeSeriesPanel';
import { seriesColor } from '@/lib/chartFmt';
import { formulaSeries } from './formulaSeries';
import {
  type BuilderState, type BuilderQuery, produces, queryDesc, queryUnit,
  seriesGroupLabel, effectiveTopN,
} from './model';
import { valueAtCursor } from './cursorBus';
import { QueryPanel } from './QueryPanel';
import type { ExploreOverlay } from './useExploreQueries';

// PanelStack (explore-v2 Phase 2) — one stacked, cursor-synced QueryPanel per
// producing query + a dashed formula panel (the RedPanel stacked-synced
// precedent, generalised). Per-panel series are capped to the biggest
// PANEL_SERIES_CAP by area (plan perf guard: 4 panels × ≤10 series).

export interface PanelData {
  key: string;             // letter, or 'ƒ' for the formula panel
  letter: string;
  desc: string;
  unit: string;
  isFormula: boolean;
  loading: boolean;
  series: TSSeries[];      // capped, labeled, coloured
  more: number;            // series dropped by the cap
  deploys?: number[];      // ▼ deploy markers (Phase 3.3 — pinned-service queries)
  events?: ChartAnnotation[]; // operator-event annotation lines (A7 — v0.8.284)
  thresholds?: TSThreshold[]; // SLO latency threshold lines (Phase 3.3)
}

// buildPanels — pure projection BuilderState + fetch results → panel data.
// Exported so Explore memoises ONE array feeding both the stack and the
// GroupTable (same labels = same row keys).
// exemplarMarkersFor — the ◆ markers for ONE series (identified by label).
// Maps each MetricExemplar's groupKey onto a series label via the shared
// seriesGroupLabel (same derivation the chart series use, so a marker lands on
// exactly the right line), anchors the glyph at the series value nearest the
// bucket time, and prefers the error trace over the slow one when a bucket has
// both (one ◆ per bucket; error is the more actionable doorway).
function exemplarMarkersFor(
  q: BuilderQuery, desc: string, label: string,
  points: { time: number; value: number | null }[],
  exemplars: MetricExemplar[],
): NonNullable<TSSeries['exemplars']> {
  const out: NonNullable<TSSeries['exemplars']> = [];
  for (const e of exemplars) {
    if (seriesGroupLabel(q, e.groupKey, desc) !== label) continue;
    const traceId = e.errorTraceId || e.slowTraceId;
    if (!traceId) continue;
    const v = valueAtCursor(points, e.time / 1e9);
    if (!isFinite(v)) continue;
    out.push({ time: e.time, value: v, traceId, kind: e.errorTraceId ? 'error' : 'slow' });
  }
  return out;
}

export function buildPanels(
  state: BuilderState,
  byLetter: Record<string, SpanMetricSeries[] | undefined>,
  exemplarsByLetter: Record<string, MetricExemplar[]> = {},
  overlaysByLetter: Record<string, ExploreOverlay> = {},
  // letter → pre-trim series count. The server caps a high-cardinality span
  // groupBy to TOP_N_MAX, so ranked.length here is already ≤50; the true
  // "+N more" must come from this total, not the capped slice. Falls back to
  // the received series count when absent (resolver / metric paths).
  totalByLetter: Record<string, number | undefined> = {},
): PanelData[] {
  const out: PanelData[] = [];
  for (const q of state.queries) {
    if (!produces(q)) continue;
    const data = byLetter[q.letter];
    const unit = queryUnit(q);
    const desc = queryDesc(q);
    if (data === undefined) {
      out.push({ key: q.letter, letter: q.letter, desc, unit, isFormula: false, loading: true, series: [], more: 0 });
      continue;
    }
    const exemplars = exemplarsByLetter[q.letter] ?? [];
    const labeled: TSSeries[] = data.map(s => {
      const label = seriesGroupLabel(q, s.groupKey, desc);
      const points = s.points.map(p => ({ time: p.time, value: p.value }));
      const ex = exemplars.length ? exemplarMarkersFor(q, desc, label, points, exemplars) : [];
      return {
        label,
        color: seriesColor(label),
        unit: unit || undefined,
        points,
        exemplars: ex.length ? ex : undefined,
      };
    });
    // Biggest-by-area win the panel slots (MQE precedent).
    const ranked = labeled
      .map(s => ({ s, area: s.points.reduce((a, p) => a + Math.abs(p.value ?? 0), 0) }))
      .sort((a, b) => b.area - a.area);
    const ov = overlaysByLetter[q.letter];
    const cap = effectiveTopN(state.topN);
    // The server may have already trimmed to TOP_N_MAX, so ranked.length
    // undercounts the real series total on a high-card groupBy. Use the
    // reported pre-trim total (defaulting to ranked.length) so "+N more"
    // reflects what actually exists, not just what came over the wire.
    const total = totalByLetter[q.letter] ?? ranked.length;
    const shown = Math.min(cap, ranked.length);
    out.push({
      key: q.letter, letter: q.letter, desc, unit, isFormula: false, loading: false,
      series: ranked.slice(0, cap).map(x => x.s),
      more: Math.max(0, total - shown),
      deploys: ov?.deploys?.length ? ov.deploys : undefined,
      events: ov?.events?.length ? ov.events : undefined,
      thresholds: ov?.thresholds?.length ? ov.thresholds : undefined,
    });
  }
  // Formula panel — dashed, gaps where a referenced bucket is missing.
  const expr = state.formula.trim();
  if (expr) {
    const pts = formulaSeries(expr, byLetter);
    const label = `ƒ: ${expr}`;
    out.push({
      key: 'ƒ', letter: 'ƒ', desc: expr, unit: '', isFormula: true,
      loading: pts.length === 0 && Object.values(byLetter).some(v => v === undefined),
      series: pts.length ? [{
        label, color: seriesColor(label), dash: [6, 4],
        points: pts.map(p => ({ time: p.time, value: p.value })),
      }] : [],
      more: 0,
    });
  }
  return out;
}

export function PanelStack({ panels, viz, hiddenKeys, focusKey, zoomWindow, onZoom, onExemplarClick }: {
  panels: PanelData[];
  viz: 'line' | 'area' | 'bars';
  hiddenKeys: Set<string>;          // `${letter}:${label}`
  focusKey: string | null;          // `${letter}:${label}`
  zoomWindow: { from: number; to: number } | null;
  onZoom: (fromSec: number, toSec: number) => void;
  onExemplarClick?: (traceId: string) => void;   // open an exemplar ◆ trace
}) {
  // Per-panel projections of the global hidden/focus keys.
  const perPanel = useMemo(() => panels.map(p => {
    const hidden = new Set<string>();
    for (const s of p.series) {
      if (hiddenKeys.has(`${p.letter}:${s.label}`)) hidden.add(s.label);
    }
    let focused: string | null = null;
    if (focusKey && focusKey.startsWith(`${p.letter}:`)) {
      focused = focusKey.slice(p.letter.length + 1);
    }
    return { hidden, focused };
  }), [panels, hiddenKeys, focusKey]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      {panels.map((p, i) => (
        <QueryPanel key={p.key} panel={p} mode={viz}
          hiddenLabels={perPanel[i].hidden}
          focusedLabel={perPanel[i].focused}
          zoomWindow={zoomWindow}
          onZoom={onZoom}
          onExemplarClick={onExemplarClick} />
      ))}
    </div>
  );
}
