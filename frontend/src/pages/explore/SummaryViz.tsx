import { useMemo } from 'react';
import { fmtSmart, seriesColor } from '@/lib/chartFmt';
import { buildGroupRows } from './GroupTable';
import type { PanelData } from './PanelStack';

// SummaryViz (explore-v2 Phase 4) — non-timeseries renderings of the SAME
// per-series data the charts use (buildGroupRows, so labels/values match the
// GroupTable exactly). 'stat' = a tile per series with its current value;
// 'toplist' = series ranked by current value as proportion bars (the Elastic
// APM / Grafana toplist idiom). No extra fetch — projects the loaded panels.

function LetterBadge({ letter, isFormula }: { letter: string; isFormula: boolean }) {
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
      width: 16, height: 16, borderRadius: 3, flexShrink: 0,
      background: isFormula ? 'var(--bg3)' : 'var(--accent2)',
      color: isFormula ? 'var(--text2)' : 'var(--bg)',
      fontSize: 10, fontWeight: 700,
    }}>{letter}</span>
  );
}

export function SummaryViz({ panels, mode }: { panels: PanelData[]; mode: 'stat' | 'toplist' }) {
  const rows = useMemo(() => buildGroupRows(panels), [panels]);

  if (rows.length === 0) return null;

  if (mode === 'stat') {
    return (
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(190px, 1fr))', gap: 10,
      }}>
        {rows.map(r => (
          <div key={r.rowKey} style={{
            background: 'var(--bg1)', border: '1px solid var(--border)',
            borderRadius: 8, padding: '12px 14px',
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8 }}>
              <LetterBadge letter={r.letter} isFormula={r.isFormula} />
              <span style={{ fontSize: 11, color: 'var(--text2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.label}>
                {r.label}
              </span>
            </div>
            <div className="mono" style={{ fontSize: 26, fontWeight: 600, lineHeight: 1.1 }}>
              {fmtSmart(r.last, r.unit)}
            </div>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
              son · ort {fmtSmart(r.avg, r.unit)} · maks {fmtSmart(r.max, r.unit)}
            </div>
          </div>
        ))}
      </div>
    );
  }

  // toplist — rank by current value, proportion bar relative to the top row.
  const sorted = [...rows].sort((a, b) => (isFinite(b.last) ? b.last : -Infinity) - (isFinite(a.last) ? a.last : -Infinity));
  const max = Math.max(1, ...sorted.map(r => (isFinite(r.last) ? r.last : 0)));
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: '8px 12px', display: 'flex', flexDirection: 'column', gap: 5,
    }}>
      {sorted.map(r => {
        const pct = isFinite(r.last) ? (r.last / max) * 100 : 0;
        return (
          <div key={r.rowKey} style={{ display: 'flex', alignItems: 'center', gap: 10, fontSize: 12 }}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, width: 200, flexShrink: 0 }}>
              <LetterBadge letter={r.letter} isFormula={r.isFormula} />
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--text2)' }} title={r.label}>
                {r.label}
              </span>
            </span>
            <span style={{ flex: 1, position: 'relative', height: 18, background: 'var(--bg2)', borderRadius: 3, overflow: 'hidden' }}>
              <span style={{ position: 'absolute', inset: 0, width: `${pct}%`, background: seriesColor(r.label), opacity: 0.5 }} />
            </span>
            <span className="mono" style={{ width: 96, textAlign: 'right', flexShrink: 0 }}>
              {fmtSmart(r.last, r.unit)}
            </span>
          </div>
        );
      })}
    </div>
  );
}
