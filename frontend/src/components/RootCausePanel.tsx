import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Spinner, Empty } from './Spinner';
import { IconFlame } from './icons';
import { api } from '@/lib/api';
import { fmtFixed, fmtDurShort } from '@/lib/utils';
import type { RootCause, BubbleUpValue } from '@/lib/types';

// RootCausePanel — the single "what changed / likely cause" surface for a
// Problem (v0.7.52, backend bundle shipped v0.7.51). Fetches
// /api/problems/{id}/rootcause ONCE on open (no polling — the data is anchored
// to a fixed window and reads expensively), then renders whichever signals are
// present, in triage priority order:
//   1. Likely-cause headline (synthesized from the strongest signal)
//   2. Smoking-gun dimension (bubble-up — error problems only)
//   3. Blast radius (who's downstream / cascading)
//   4. What changed (correlated services — the former standalone
//      CorrelationsPanel, folded in so it's one round-trip not two)
//   5. Exemplar trace (one representative bad trace)
// Every sub-signal is best-effort server-side, so a partial bundle still
// renders — the panel only shows the Empty state when nothing correlated.
export function RootCausePanel({ problemId, service }: { problemId: string; service: string }) {
  const [rc, setRc] = useState<RootCause | null | undefined>(undefined);
  useEffect(() => {
    setRc(undefined);
    api.problemRootCause(problemId)
      .then(r => setRc(r ?? null))
      .catch(() => setRc(null));
  }, [problemId]);

  if (rc === undefined) return <Spinner />;
  if (rc === null) {
    return <div style={{ fontSize: 12, color: 'var(--err)' }}>
      Root-cause analysis failed to load. Check the server log.
    </div>;
  }

  const bubble = topBubble(rc);
  const blast = rc.blastRadius && rc.blastRadius.totalCallers > 0 ? rc.blastRadius : null;
  const corr = rc.correlations.filter(c => c.service !== service);
  const headline = likelyCause(rc, service, bubble);
  const nothing = !rc.recentDeploy && !bubble && !blast && corr.length === 0 && !rc.exemplar;

  if (nothing) {
    return (
      <Empty icon={<IconFlame size={28} />} title="No correlating signals">
        The fire looks localized to <b>{service}</b> — no recent deploy,
        downstream cascade, or co-moving service in the analysis window.
      </Empty>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      {/* 1. Likely-cause headline — the one-line synthesis. */}
      <div style={{
        padding: '10px 12px', borderRadius: 6,
        background: headline.bg, border: `1px solid ${headline.border}`,
        fontSize: 12.5, color: 'var(--text)', lineHeight: 1.5,
      }}>
        <div style={{
          fontSize: 10.5, fontWeight: 700, letterSpacing: 0.4,
          textTransform: 'uppercase', color: headline.accent, marginBottom: 3,
        }}>Likely cause</div>
        {headline.text}
      </div>

      {/* 2. Smoking-gun dimension (bubble-up). */}
      {bubble && (
        <Section title="Where errors concentrate"
                 subtitle={`top attribute over the error spans (${rootWindow(rc)})`}>
          <div style={{ fontSize: 12, marginBottom: 6 }}>
            <code>{bubble.key}</code>
          </div>
          {bubble.values.slice(0, 5).map((v, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
              <span style={{ flex: '0 0 38%', fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                    title={v.value}>{v.value || '(empty)'}</span>
              <div style={{ flex: 1, height: 14, background: 'var(--bg3)', borderRadius: 3, position: 'relative' }}>
                <div style={{
                  position: 'absolute', inset: 0, width: `${Math.max(2, v.selectionPct * 100)}%`,
                  background: scoreColor(v.score), borderRadius: 3,
                }} />
              </div>
              <span className="mono" style={{ flex: '0 0 96px', textAlign: 'right', fontSize: 11, color: 'var(--text2)' }}>
                {pct(v.selectionPct)} <span style={{ color: 'var(--text3)' }}>/ {pct(v.baselinePct)}</span>
              </span>
            </div>
          ))}
          <div style={{ fontSize: 10.5, color: 'var(--text3)', marginTop: 4 }}>
            bar = % of error spans with this value · right = error% / baseline%
          </div>
        </Section>
      )}

      {/* 3. Blast radius. */}
      {blast && (
        <Section title="Blast radius"
                 subtitle={`${blast.totalCallers} caller${blast.totalCallers === 1 ? '' : 's'}, ${blast.cascadingCallers} already cascading`}>
          {blast.callers.length === 0 ? (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>
              No inbound callers in the window — <b>{service}</b> is an entry point.
            </div>
          ) : (
            <div className="table-wrap">
              <table>
                <thead><tr>
                  <th>Caller</th>
                  <th className="num" style={{ width: 70 }}>RPS</th>
                  <th className="num" style={{ width: 80 }}>Errors</th>
                  <th style={{ width: 90 }}>State</th>
                </tr></thead>
                <tbody>
                  {[...blast.callers]
                    .sort((a, b) => b.errorRate - a.errorRate || b.rps - a.rps)
                    .slice(0, 6)
                    .map(c => (
                      <tr key={c.service}>
                        <td>
                          <Link to={`/service?name=${encodeURIComponent(c.service)}`} style={{ fontWeight: 600 }}>
                            {c.service}
                          </Link>
                        </td>
                        <td className="num mono">{c.rps.toFixed(1)}</td>
                        <td className="num mono" style={{ color: c.errorRate > 0 ? 'var(--err)' : 'var(--text2)' }}>
                          {/* v0.8.317 — BlastRadiusCaller.errorRate is a PERCENT
                              (chstore/blast_radius.go: errors*100/calls); pct()
                              multiplies a 0..1 fraction by 100, so a 3%-error
                              caller read "300%" on the triage drawer. */}
                          {fmtFixed(c.errorRate, 1)}%
                        </td>
                        <td>
                          {c.hasOpenProblem
                            ? <span className="badge b-err">OPEN</span>
                            : <span style={{ fontSize: 11, color: 'var(--text3)' }}>—</span>}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          )}
        </Section>
      )}

      {/* 4. What changed — correlated services (folded-in CorrelationsPanel). */}
      {corr.length > 0 && (
        <Section title="What else changed"
                 subtitle="services that moved around the fire (current vs prior window, by composite score)">
          <div className="table-wrap">
            <table>
              <thead><tr>
                <th style={{ width: 36 }}>#</th>
                <th>Service</th>
                <th>What changed</th>
                <th className="num" style={{ width: 64 }}>Score</th>
              </tr></thead>
              <tbody>
                {corr.slice(0, 8).map((c, i) => (
                  <tr key={c.service}>
                    <td className="mono" style={{ color: 'var(--text3)' }}>{i + 1}</td>
                    <td>
                      <Link to={`/service?name=${encodeURIComponent(c.service)}`} style={{ fontWeight: 600 }}>
                        {c.service}
                      </Link>
                    </td>
                    <td style={{ fontSize: 12, lineHeight: 1.5 }}>
                      {c.reasons.map((r, k) => <div key={k}>{r}</div>)}
                    </td>
                    <td className="num mono" style={{
                      fontWeight: 600,
                      color: c.score > 50 ? 'var(--err)' : c.score > 20 ? 'var(--warn)' : 'var(--text2)',
                    }}>{c.score.toFixed(0)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Section>
      )}

      {/* 5. Exemplar trace. */}
      {rc.exemplar && (
        <Section title="Exemplar trace" subtitle="one representative bad trace from the window">
          <Link to={`/trace?id=${encodeURIComponent(rc.exemplar.traceId)}`}
                style={{
                  display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap',
                  padding: '8px 12px', borderRadius: 6, textDecoration: 'none',
                  background: 'var(--bg2)', border: '1px solid var(--border)',
                }}>
            {rc.exemplar.statusCode === 'error'
              ? <span className="badge b-err">ERROR</span>
              : <span className="badge b-ok">OK</span>}
            <span style={{ fontWeight: 600, color: 'var(--text)' }}>{rc.exemplar.name}</span>
            <span className="mono" style={{ fontSize: 12, color: 'var(--text2)' }}>
              {(rc.exemplar.durationNs / 1e6).toFixed(1)} ms
            </span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--text3)', marginLeft: 'auto' }}>
              {rc.exemplar.traceId.slice(0, 16)}… ↗
            </span>
          </Link>
        </Section>
      )}
    </div>
  );
}

// Section — uniform sub-header + body wrapper for each root-cause signal.
function Section({ title, subtitle, children }: { title: string; subtitle?: string; children: ReactNode }) {
  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6 }}>
        <span style={{ fontSize: 11, fontWeight: 700, letterSpacing: 0.4, textTransform: 'uppercase', color: 'var(--text2)' }}>
          {title}
        </span>
        {subtitle && <span style={{ fontSize: 10.5, color: 'var(--text3)' }}>{subtitle}</span>}
      </div>
      {children}
    </div>
  );
}

// likelyCause synthesizes the single strongest signal into a one-liner +
// tone. Priority: deploy regression > concentrated error dimension >
// co-moving service > localized.
function likelyCause(
  rc: RootCause, service: string,
  bubble: { key: string; values: BubbleUpValue[] } | null,
): { text: ReactNode; accent: string; bg: string; border: string } {
  const deploy = { accent: 'var(--warn)', bg: 'rgba(250,204,21,0.10)', border: 'rgba(250,204,21,0.40)' };
  const dim = { accent: 'var(--err)', bg: 'rgba(220,38,38,0.08)', border: 'rgba(220,38,38,0.35)' };
  const corr = { accent: 'var(--accent2)', bg: 'rgba(56,139,253,0.08)', border: 'rgba(56,139,253,0.35)' };
  const local = { accent: 'var(--text3)', bg: 'var(--bg2)', border: 'var(--border)' };

  if (rc.recentDeploy) {
    return {
      ...deploy,
      text: <>Coincides with a <b>deploy</b> — <code>service.version={rc.recentDeploy.version}</code> first
        seen <b>{fmtDurShort(rc.recentDeploy.ageSeconds)}</b> before this problem opened.</>,
    };
  }
  const top = bubble?.values[0];
  if (bubble && top && top.score >= 0.2) {
    return {
      ...dim,
      text: <>Errors concentrate in <code>{bubble.key}={top.value || '(empty)'}</code> — <b>{pct(top.selectionPct)}</b> of
        error spans vs {pct(top.baselinePct)} of all spans.</>,
    };
  }
  const co = rc.correlations.find(c => c.service !== service && c.score >= 20);
  if (co) {
    return {
      ...corr,
      text: <>Co-moving with <b>{co.service}</b> in the same window (score {co.score.toFixed(0)}) — possible
        upstream / downstream propagation.</>,
    };
  }
  return {
    ...local,
    text: <>No strong external signal — the fire appears <b>localized to {service}</b>.</>,
  };
}

// topBubble flattens the bubble-up result to the single most over-represented
// attribute (the one whose top value has the highest score), returning its
// values sorted desc for the bar list. null when bubble-up is absent or flat.
function topBubble(rc: RootCause): { key: string; values: BubbleUpValue[] } | null {
  if (!rc.bubbleUp || rc.bubbleUp.attributes.length === 0) return null;
  let best: { key: string; values: BubbleUpValue[] } | null = null;
  for (const attr of rc.bubbleUp.attributes) {
    const values = [...attr.values].sort((a, b) => b.score - a.score);
    const topScore = values[0]?.score ?? 0;
    if (topScore > 0 && (!best || topScore > (best.values[0]?.score ?? 0))) {
      best = { key: attr.key, values };
    }
  }
  return best;
}

// rootWindow — human window length for sub-headers ("over 18m").
function rootWindow(rc: RootCause): string {
  return `over ${fmtDurShort((rc.toNs - rc.fromNs) / 1e9)}`;
}

// scoreColor — over-representation heat: deep red for a strong smoking gun,
// fading to muted as the score approaches the baseline.
function scoreColor(score: number): string {
  if (score >= 0.4) return 'var(--err)';
  if (score >= 0.15) return 'var(--warn)';
  return 'var(--accent2)';
}

// fmtAgo — compact "6m" / "2h" / "3d" age. Local copy (the AnomaliesPage one
// isn't exported); trivial enough not to warrant a shared-util churn.

// pct — 0..1 fraction → whole-number percent string.
function pct(f: number): string {
  return `${Math.round(f * 100)}%`;
}
