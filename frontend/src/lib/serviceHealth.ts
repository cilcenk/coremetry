// serviceHealth.ts — the ONE health-color helper, used identically across every
// surface (topology node dot, Services table pill, Service Overview KPIs, the
// Upstream/Downstream neighbors card). Per the design handoff: a single
// threshold rule, no per-page variation — err > 5% → red, > 1% → amber, else
// green. Build new design-parity surfaces against this; do not re-derive the
// thresholds locally.

export type HealthLevel = 'green' | 'amber' | 'red';

export function healthLevel(errPct: number): HealthLevel {
  return errPct > 5 ? 'red' : errPct > 1 ? 'amber' : 'green';
}

// healthColor maps a level to its token var() for inline text/fills.
export function healthColor(level: HealthLevel): string {
  return level === 'red' ? 'var(--err)' : level === 'amber' ? 'var(--warn)' : 'var(--ok)';
}
