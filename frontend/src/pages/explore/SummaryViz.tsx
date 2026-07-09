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

export function SummaryViz({ panels, mode }: { panels: PanelData[]; mode: 'stat' | 'toplist' | 'pie' }) {
  const rows = useMemo(() => buildGroupRows(panels), [panels]);

  if (rows.length === 0) return null;

  // v0.8.427 (DE5) — donut per query letter: share of each series'
  // CURRENT value within its query (same `last` semantics stat/toplist
  // use, same buildGroupRows labels/colors — zero new fetches). Inline
  // SVG stroke-arc donut; series colors come from seriesColor so the
  // slices match the line chart + table dots exactly.
  if (mode === 'pie') {
    const byLetter = new Map<string, typeof rows>();
    for (const r of rows) {
      const list = byLetter.get(r.letter) ?? [];
      list.push(r);
      byLetter.set(r.letter, list);
    }
    return (
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))', gap: 10 }}>
        {[...byLetter.entries()].map(([letter, list]) => {
          const slices = list
            .map(r => ({ ...r, v: isFinite(r.last) ? Math.max(0, r.last) : 0 }))
            .sort((a, b) => b.v - a.v);
          const total = slices.reduce((a, r) => a + r.v, 0);
          const size = 150, stroke = 18;
          const rad = (size - stroke) / 2;
          const circ = 2 * Math.PI * rad;
          let acc = 0;
          return (
            <div key={letter} style={{
              display: 'flex', gap: 14, alignItems: 'center',
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: '12px 14px', minWidth: 0,
            }}>
              <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img"
                aria-label={`Query ${letter} share of current value`}>
                {total > 0 && slices.map(r => {
                  const frac = r.v / total;
                  const dash = frac * circ;
                  const off = -acc * circ;
                  acc += frac;
                  return (
                    <circle key={r.rowKey} cx={size / 2} cy={size / 2} r={rad} fill="none"
                      stroke={seriesColor(r.label)} strokeWidth={stroke}
                      strokeDasharray={`${dash} ${circ - dash}`}
                      strokeDashoffset={off}
                      transform={`rotate(-90 ${size / 2} ${size / 2})`}>
                      <title>{`${r.label} — ${fmtSmart(r.last, r.unit)} (${(frac * 100).toFixed(1)}%)`}</title>
                    </circle>
                  );
                })}
                {total === 0 && (
                  <circle cx={size / 2} cy={size / 2} r={rad} fill="none"
                    stroke="var(--bg3)" strokeWidth={stroke} />
                )}
              </svg>
              <div style={{ minWidth: 0, flex: 1 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
                  <LetterBadge letter={letter} isFormula={list[0]?.isFormula ?? false} />
                  <span style={{ fontSize: 11, color: 'var(--text3)' }}>güncel değerin payı</span>
                </div>
                {slices.slice(0, 8).map(r => (
                  <div key={r.rowKey} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11.5, marginBottom: 2, minWidth: 0 }}>
                    <i style={{ width: 8, height: 8, borderRadius: 2, background: seriesColor(r.label), flex: 'none' }} />
                    <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.label}>{r.label}</span>
                    <span className="mono" style={{ marginLeft: 'auto', color: 'var(--text2)' }}>
                      {total > 0 ? `${((r.v / total) * 100).toFixed(1)}%` : '—'}
                    </span>
                  </div>
                ))}
                {slices.length > 8 && (
                  <div style={{ fontSize: 11, color: 'var(--text3)' }}>+{slices.length - 8} daha</div>
                )}
              </div>
            </div>
          );
        })}
      </div>
    );
  }

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
