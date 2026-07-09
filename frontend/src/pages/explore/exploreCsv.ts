// exploreCsv.ts (v0.8.412 — Data-Explorer parity DE1) — CSV export of
// the Explore result set. Long format (one row per series point):
// every dimension tuple × timestamp × value, the shape Excel/pandas
// pivot naturally — the summary table's Last/Avg/Max are derivable
// from it, the reverse isn't. Pure — vitest beside it.

import type { PanelData } from './PanelStack';

// csvField — RFC 4180: quote when the value contains a comma, quote,
// or newline; double embedded quotes.
export function csvField(v: string): string {
  if (/[",\n\r]/.test(v)) return '"' + v.replace(/"/g, '""') + '"';
  return v;
}

// panelsToCSV — header + one row per (query, series, bucket). Times
// are ISO-8601 UTC (points carry unix nanos); null buckets export as
// an empty value cell so gaps stay distinguishable from zero.
export function panelsToCSV(panels: PanelData[]): string {
  const lines: string[] = ['query,series,unit,time,value'];
  for (const p of panels) {
    if (p.loading) continue;
    for (const s of p.series) {
      for (const pt of s.points) {
        const iso = new Date(pt.time / 1e6).toISOString();
        const val = pt.value == null || !isFinite(pt.value) ? '' : String(pt.value);
        lines.push([
          csvField(p.letter), csvField(s.label), csvField(p.unit), iso, val,
        ].join(','));
      }
    }
  }
  return lines.join('\n') + '\n';
}
