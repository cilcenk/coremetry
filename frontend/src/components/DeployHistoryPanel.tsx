import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { DeployHistoryRow } from '@/lib/types';

// Per-row Copilot explain state (v0.5.192). Keyed by deploy
// version+time inside the panel so each row remembers its own
// answer + loading flag without all rows sharing state.
type ExplainState =
  | { kind: 'idle' }
  | { kind: 'busy' }
  | { kind: 'ok'; text: string }
  | { kind: 'err'; msg: string };

// DeployHistoryPanel — "Recent deploys" panel on the service
// detail page (v0.5.189). Continuous benchmarking: for each of
// the last N deploys, show the before/after RED diff so an
// operator can read "the last release regressed p99 by 12%"
// at a glance — no chart-shoulder eyeballing, no AI Copilot
// round-trip needed.
//
// Layout: vertical timeline (newest first). Each row =
// version + age + 3 delta chips (p99 / error rate / avg).
// Colour: red >5% worse, amber 1-5% worse, green if better
// or stable. Click expands to show absolute before/after.
//
// Empty state: many services don't emit service.version (no
// SDK env var). Show a one-line hint instead of breaking the
// service page layout.
export function DeployHistoryPanel({ service }: { service: string }) {
  const [rows, setRows] = useState<DeployHistoryRow[] | null | undefined>(undefined);
  const [expanded, setExpanded] = useState<number | null>(null);
  // Per-deploy explain state, keyed by version+time so the
  // map stays correct even if the deploy list re-fetches.
  const [explains, setExplains] = useState<Record<string, ExplainState>>({});
  const [copilotEnabled, setCopilotEnabled] = useState<boolean | null>(null);

  useEffect(() => {
    api.deployHistory(service, 5, 600)
      .then(r => setRows(r ?? []))
      .catch(() => setRows(null));
    // Probe the copilot once — if it's not configured, hide
    // the Explain button rather than show a button that always
    // 503's. Same gate other CopilotExplain surfaces use.
    api.copilotConfig().then(c => setCopilotEnabled(c.enabled)).catch(() => setCopilotEnabled(false));
  }, [service]);

  const askCopilot = async (row: DeployHistoryRow) => {
    const key = `${row.deploy.version}-${row.deploy.timeUnixNs}`;
    setExplains(s => ({ ...s, [key]: { kind: 'busy' } }));
    try {
      const r = await api.copilotDeployImpact({
        service,
        version: row.deploy.version,
        deployTimeNs: row.deploy.timeUnixNs,
        windowSec: 600,
      });
      setExplains(s => ({ ...s, [key]: { kind: 'ok', text: r.explanation } }));
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setExplains(s => ({ ...s, [key]: { kind: 'err', msg } }));
    }
  };

  // Don't render anything while loading — keeps the service
  // page layout stable rather than flashing a spinner.
  if (rows === undefined) return null;
  if (rows === null) return null;
  if (rows.length === 0) {
    // v0.5.308 — Operator-reported: clicking "history →" from
    // /deploys lands here with no visible panel, which looks
    // broken. Render a soft hint instead of silent-hiding so
    // the operator knows the panel WAS rendered + the data
    // just isn't in the lookback. Same rationale as the
    // empty-ops CTA on the Operations tab.
    return (
      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 16,
        fontSize: 12, color: 'var(--text3)',
      }}>
        <div style={{ fontWeight: 700, color: 'var(--text2)', marginBottom: 4 }}>
          ↑ Deploy history
        </div>
        No deploys observed in the last 30 days. Either this service
        doesn't emit a recognised version attribute
        (service.version / container.image.tag /
        app.kubernetes.io/version label / helm.chart.version), or
        its last release predates the lookback window.
      </div>
    );
  }

  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 12, marginBottom: 16,
    }}>
      <div style={{
        fontSize: 11, color: 'var(--text2)',
        fontWeight: 600, letterSpacing: '0.5px',
        textTransform: 'uppercase', marginBottom: 8,
        display: 'flex', alignItems: 'baseline', gap: 8,
      }}>
        <span>↗ Recent deploys</span>
        <span style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 400, textTransform: 'none', letterSpacing: 0 }}>
          ±10 min window
        </span>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column' }}>
        {rows.map((r, i) => {
          const open = expanded === i;
          return (
            <div key={`${r.deploy.version}-${r.deploy.timeUnixNs}`}
              style={{
                padding: '8px 0',
                borderTop: i > 0 ? '1px solid var(--border)' : 'none',
              }}>
              <div onClick={() => setExpanded(open ? null : i)}
                style={{
                  display: 'flex', alignItems: 'center', gap: 10,
                  cursor: r.impact ? 'pointer' : 'default',
                }}>
                <span style={{ fontSize: 10, color: 'var(--text3)', width: 14 }}>
                  {r.impact ? (open ? '▼' : '▶') : ''}
                </span>
                <span style={{
                  fontFamily: 'ui-monospace, monospace',
                  fontSize: 12, fontWeight: 600,
                }}>{r.deploy.version || '(no version)'}</span>
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  {fmtAgo(r.deploy.timeUnixNs)}
                </span>
                <span style={{ flex: 1 }} />
                {r.impact ? <DeltaChips imp={r.impact} /> : (
                  <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                    impact pending
                  </span>
                )}
              </div>
              {open && r.impact && (
                <>
                  <ExpandedDiff imp={r.impact} />
                  {/* Inline Copilot explain — opt-in per row.
                      Hidden when the copilot isn't configured
                      so we don't render a button that just
                      503's. Stop click propagation so the
                      row's expand toggle doesn't also fire. */}
                  {copilotEnabled && (() => {
                    const key = `${r.deploy.version}-${r.deploy.timeUnixNs}`;
                    const st = explains[key] ?? { kind: 'idle' as const };
                    return (
                      <div style={{ marginTop: 10 }}
                        onClick={e => e.stopPropagation()}>
                        {st.kind === 'busy' && (
                          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                            ✨ Thinking…
                          </span>
                        )}
                        {(st.kind === 'idle' || st.kind === 'err') && (
                          <button className="sec"
                            onClick={() => askCopilot(r)}
                            style={{
                              fontSize: 11, padding: '4px 10px',
                              color: 'var(--accent2)',
                            }}
                            title="Ask Copilot whether this deploy looks clean, regressed, or rollback-worthy">
                            ✨ {st.kind === 'err' ? 'Re-ask' : 'Explain'} deploy
                          </button>
                        )}
                        {st.kind === 'err' && (
                          <span style={{
                            marginLeft: 8, fontSize: 11, color: 'var(--err)',
                          }}>{st.msg}</span>
                        )}
                        {st.kind === 'ok' && (
                          <div style={{
                            marginTop: 4, padding: 10,
                            background: 'var(--bg)',
                            border: '1px solid var(--border)',
                            borderRadius: 4, fontSize: 12,
                            lineHeight: 1.55,
                            whiteSpace: 'pre-wrap',
                          }}>
                            <div style={{
                              fontSize: 10, color: 'var(--accent2)',
                              textTransform: 'uppercase', letterSpacing: 0.4,
                              marginBottom: 6, fontWeight: 600,
                            }}>✨ Copilot</div>
                            {st.text}
                          </div>
                        )}
                      </div>
                    );
                  })()}
                </>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function DeltaChips({ imp }: { imp: DeployHistoryRow['impact'] }) {
  if (!imp) return null;
  // Pre-deploy with zero samples isn't a real "regression" —
  // it's just first traffic. Render a neutral chip rather than
  // a misleading 100% red flag.
  const p99Stable = imp.before.p99Ms === 0;
  const errStable = imp.before.errorRate === 0 && imp.after.errorRate === 0;
  return (
    <span style={{ display: 'inline-flex', gap: 6, fontSize: 11,
      fontFamily: 'ui-monospace, monospace' }}>
      <DeltaChip label="p99" pct={p99Stable ? null : imp.p99DeltaPct} suffix="%" />
      <DeltaChip label="err" pct={errStable ? null : imp.errorRateDeltaPct} suffix="pp" />
      <DeltaChip label="avg" pct={imp.before.avgMs === 0 ? null : imp.avgDeltaPct} suffix="%" />
    </span>
  );
}

function DeltaChip({ label, pct, suffix }: { label: string; pct: number | null; suffix: string }) {
  if (pct === null) {
    return (
      <span style={{
        padding: '2px 6px', borderRadius: 3,
        background: 'var(--bg3)', color: 'var(--text3)',
      }}>{label} —</span>
    );
  }
  // Colour bands. Latency / avg in percent; error rate in
  // percentage points (already pre-multiplied). > 5% worse =
  // red, > 1% worse = amber, < -5% better = green.
  const color = pct > 5 ? 'var(--err)'
    : pct > 1 ? 'var(--warn)'
    : pct < -5 ? 'var(--ok)'
    : 'var(--text3)';
  const sign = pct > 0 ? '+' : '';
  return (
    <span style={{
      padding: '2px 6px', borderRadius: 3,
      background: 'var(--bg3)', color,
    }}
      title={`${label} delta: ${sign}${pct.toFixed(2)}${suffix}`}>
      {label} {sign}{pct.toFixed(1)}{suffix}
    </span>
  );
}

function ExpandedDiff({ imp }: { imp: NonNullable<DeployHistoryRow['impact']> }) {
  return (
    <div style={{
      marginTop: 8, padding: 10, borderRadius: 4,
      background: 'var(--bg)', border: '1px solid var(--border)',
      fontSize: 11, color: 'var(--text2)',
    }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr 1fr', gap: '4px 14px', alignItems: 'center' }}>
        <span style={{ color: 'var(--text3)' }}></span>
        <span style={{ color: 'var(--text3)', textTransform: 'uppercase', fontSize: 10, letterSpacing: 0.4 }}>before</span>
        <span style={{ color: 'var(--text3)', textTransform: 'uppercase', fontSize: 10, letterSpacing: 0.4 }}>after</span>

        <span>Spans</span>
        <span className="mono">{fmtNum(imp.before.count)}</span>
        <span className="mono">{fmtNum(imp.after.count)}</span>

        <span>RPS</span>
        <span className="mono">{imp.before.rps.toFixed(2)}</span>
        <span className="mono">{imp.after.rps.toFixed(2)}</span>

        <span>Error rate</span>
        <span className="mono">{(imp.before.errorRate * 100).toFixed(2)}%</span>
        <span className="mono">{(imp.after.errorRate * 100).toFixed(2)}%</span>

        <span>p99 ms</span>
        <span className="mono">{imp.before.p99Ms.toFixed(0)}</span>
        <span className="mono">{imp.after.p99Ms.toFixed(0)}</span>

        <span>avg ms</span>
        <span className="mono">{imp.before.avgMs.toFixed(1)}</span>
        <span className="mono">{imp.after.avgMs.toFixed(1)}</span>
      </div>
    </div>
  );
}

function fmtAgo(unixNs: number): string {
  const secs = Math.max(1, Math.floor((Date.now() - unixNs / 1e6) / 1000));
  if (secs < 60)       return `${secs}s ago`;
  if (secs < 3600)     return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400)    return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}
