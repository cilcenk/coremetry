import { ArrowUp, ArrowDown } from 'lucide-react';
import { LIST_NEW_LABEL, LIST_NEW_TITLE } from '@/lib/endpointHonesty';

// TrendDelta — small arrow + % change next to a metric value.
// kind='lowerBetter' → red when current > prior (regression),
//                       green when current < prior (improvement).
// kind='neutral' → just direction tint, no value judgement
//                  (used for calls — more traffic isn't inherently
//                   bad, less isn't inherently good).
// Threshold: |delta| < 5% renders as a neutral "·" so noise
// doesn't paint every cell colorful.
//
// v0.9.818 — the prior===0 chip said "NEW", which was a claim the data
// cannot support. Every surface that feeds this component compares two
// TOP-N reads (endpoints LIMIT n, databases LIMIT 5000, messaging LIMIT
// 200): a row missing from the prior payload may simply have ranked
// below that window's cut. The honest sentence is "new to this LIST",
// and the title now says why.
//
// v0.8.360 — moved verbatim out of Endpoints.tsx so the detail
// drawer's header RED strip shares the exact same delta affordance
// as the table cells (one design language; an import cycle
// Endpoints ↔ DetailDrawer is avoided by hosting it here).
// v0.8.364 (Stage-2 M1) — promoted verbatim from pages/endpoints/
// to components/: the messaging overview (DependenciesTable, a
// components/-level file) adopted compare=prior, and components/
// must not import from pages/. Props unchanged; still the single
// implementation.
export function TrendDelta({ cur, prior, kind }: {
  cur: number; prior?: number; kind: 'lowerBetter' | 'neutral';
}) {
  if (prior === undefined || prior === null) return null;
  if (prior === 0) {
    if (cur === 0) return null;
    return (
      <span className="badge b-info" title={LIST_NEW_TITLE}
        style={{ marginLeft: 4, fontSize: 9, textTransform: 'none', letterSpacing: 0 }}>
        {LIST_NEW_LABEL}
      </span>
    );
  }
  const pct = ((cur - prior) / prior) * 100;
  const abs = Math.abs(pct);
  if (abs < 5) {
    return (
      <span style={{ marginLeft: 4, color: 'var(--text3)', fontSize: 9 }}>·</span>
    );
  }
  const up = pct > 0;
  let color = 'var(--text3)';
  if (kind === 'lowerBetter') {
    color = up ? 'var(--err)' : 'var(--ok)';
  } else if (kind === 'neutral') {
    color = up ? 'var(--accent2)' : 'var(--text3)';
  }
  return (
    <span style={{
      marginLeft: 4, fontSize: 9, color,
      fontFamily: 'ui-monospace, monospace',
      display: 'inline-flex', alignItems: 'center', gap: 1,
    }}
      title={`Prior window: ${prior.toLocaleString(undefined, { maximumFractionDigits: 1 })}`}>
      {up ? <ArrowUp size={9} strokeWidth={2.5} /> : <ArrowDown size={9} strokeWidth={2.5} />}
      {abs.toFixed(0)}%
    </span>
  );
}
