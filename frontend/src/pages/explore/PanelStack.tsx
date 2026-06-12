import { useMemo } from 'react';
import type { SpanMetricSeries } from '@/lib/types';
import type { TSSeries } from '@/components/viz/TimeSeriesPanel';
import { seriesColor } from '@/lib/chartFmt';
import { formulaSeries } from './formulaSeries';
import {
  type BuilderState, produces, queryDesc, queryUnit, PANEL_SERIES_CAP,
} from './model';
import { QueryPanel } from './QueryPanel';

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
}

// buildPanels — pure projection BuilderState + fetch results → panel data.
// Exported so Explore memoises ONE array feeding both the stack and the
// GroupTable (same labels = same row keys).
export function buildPanels(
  state: BuilderState,
  byLetter: Record<string, SpanMetricSeries[] | undefined>,
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
    const labeled: TSSeries[] = data.map(s => {
      const grp = s.groupKey
        .map((val, gi) => `${(q.splitBy[gi] ?? 'g').replace(/^.*\./, '')}=${val}`)
        .join(', ');
      const label = grp || desc;
      return {
        label,
        color: seriesColor(label),
        unit: unit || undefined,
        points: s.points.map(p => ({ time: p.time, value: p.value })),
      };
    });
    // Biggest-by-area win the panel slots (MQE precedent).
    const ranked = labeled
      .map(s => ({ s, area: s.points.reduce((a, p) => a + Math.abs(p.value ?? 0), 0) }))
      .sort((a, b) => b.area - a.area);
    out.push({
      key: q.letter, letter: q.letter, desc, unit, isFormula: false, loading: false,
      series: ranked.slice(0, PANEL_SERIES_CAP).map(x => x.s),
      more: Math.max(0, ranked.length - PANEL_SERIES_CAP),
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

export function PanelStack({ panels, viz, hiddenKeys, focusKey, zoomWindow, onZoom }: {
  panels: PanelData[];
  viz: 'line' | 'area' | 'bars';
  hiddenKeys: Set<string>;          // `${letter}:${label}`
  focusKey: string | null;          // `${letter}:${label}`
  zoomWindow: { from: number; to: number } | null;
  onZoom: (fromSec: number, toSec: number) => void;
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
          onZoom={onZoom} />
      ))}
    </div>
  );
}
