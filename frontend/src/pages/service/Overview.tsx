import type { Service, Problem, TimeRange } from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';

// Service Overview (v0.7.92) — Dynatrace-style at-a-glance APM view, ported
// from the design handoff. The new tab on /service?name=<svc> (will become
// the default once complete). Reuses the service-bundle data Service.tsx
// already fetched — no extra round-trip here.
//
// Increment 1: KPI row + recent problems. Follow-up increments add the RED
// charts row (uPlot), the service-flow map (SVG wires), the compact
// operations + top-DB-statements row, and the instances + sub-tabs.

interface Props {
  service: string;
  range: TimeRange;
  info: Service | null;
  problems: Problem[];
}

function KpiTile({ lab, val, unit, accent }: {
  lab: string; val: string; unit?: string; accent: string;
}) {
  return (
    <div className="card ov-kpi">
      <div className="ov-kpi-accent" style={{ background: accent }} />
      <div className="ov-lab">{lab}</div>
      <div className="ov-val">{val}{unit && <span className="ov-unit">{unit}</span>}</div>
    </div>
  );
}

// Severity → callout icon glyph + colour. accent-soft tile bg keeps the
// chip subtle; the glyph carries the status colour (meaning is not
// colour-only — the glyph shape differs per severity).
const PROB_ICON: Record<string, { ic: string; fg: string }> = {
  critical: { ic: '▲', fg: 'var(--err)' },
  warning:  { ic: '◆', fg: 'var(--warn)' },
  info:     { ic: '•', fg: 'var(--accent)' },
};

function relTime(ns: number): string {
  const ms = Date.now() - ns / 1e6;
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export function ServiceOverview({ service, range, info, problems }: Props) {
  if (!info) return null;
  const { from, to } = timeRangeToNs(range);
  const windowSec = Math.max(1, (to - from) / 1e9);
  const rps = info.spanCount / windowSec;
  const open = problems.filter(p => p.status !== 'resolved');

  return (
    <div style={{ marginTop: 4 }}>
      {/* KPI row — golden signals. Throughput is neutral/good when up;
          failure-rate / latency are bad when up; Apdex bad when down.
          (Delta lines + full-bleed sparklines land with the series fetch
          in the charts increment.) */}
      <div className="ov-grid ov-kpis ov-mb">
        <KpiTile lab="Throughput" val={rps.toFixed(rps < 10 ? 1 : 0)} unit=" req/s" accent="var(--accent)" />
        <KpiTile lab="Failure rate" val={`${info.errorRate.toFixed(2)}%`} accent="var(--err)" />
        <KpiTile lab="Response time · P99" val={info.p99DurationMs.toFixed(0)} unit=" ms" accent="var(--orange)" />
        <KpiTile lab="Response time · avg" val={info.avgDurationMs.toFixed(1)} unit=" ms" accent="var(--purple)" />
        <KpiTile lab="Apdex" val={(info.apdex ?? 0).toFixed(2)} accent="var(--ok)" />
      </div>

      {/* Recent problems & events */}
      <div className="card">
        <div className="ov-card-h">
          <h3>Recent problems &amp; events</h3>
          {open.length > 0 && <span className="ov-sub">{open.length} open</span>}
        </div>
        {open.length === 0 ? (
          <div className="ov-card-b" style={{ color: 'var(--text2)', fontSize: 13 }}>
            No open problems for {service} in this window.
          </div>
        ) : (
          <div>
            {open.slice(0, 8).map(p => {
              const sk = PROB_ICON[p.severity] ?? PROB_ICON.info;
              return (
                <div className="ov-prob" key={p.id}>
                  <div className="ov-ic" style={{ background: 'var(--accent-soft)', color: sk.fg }}>{sk.ic}</div>
                  <div>
                    <div className="ov-ti">{p.ruleName}</div>
                    <div className="ov-de">{p.description}</div>
                  </div>
                  <div className="ov-tm">{relTime(p.startedAt)}</div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
