import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Spinner, Empty } from './Spinner';
import { IconFlame } from './icons';
import { api } from '@/lib/api';
import type { RootCauseSummary, RootCause, AnomalyRootCause, ScoredCause } from '@/lib/types';
import { fmtDurShort } from '@/lib/utils';

// RootCauseRibbon (rc #3) — the in-page "Root cause: <suspect> (NN%) ▸" chip on
// each /anomalies + /problems row. The COLLAPSED chip renders straight from the
// row's persisted summary (AnomalyEvent.rootCause / Problem.rootCause) — NO
// fetch on mount, that's the whole point of the worker-persisted join. Clicking
// ▸ expands and ONLY THEN fetches the full /rootcause fan-out (the ranked
// candidates + deploy + exemplar). Honest about evidence: no summary OR ~zero
// confidence → a muted "no clear cause yet" / "computing…" state, never a fake
// suspect. Read-only — viewers see it unchanged (it's derived data, no gating).
//
// anchor selects which fan-out the expand reads: a problem id → problemRootCause,
// an anomaly id → anomalyRootCause. Both return the shared RootCause shape (the
// anomaly variant just adds anchor fields), so the expanded body renders one way.
export function RootCauseRibbon({
  anchor, id, summary,
}: {
  anchor: 'problem' | 'anomaly';
  id: string;
  summary?: RootCauseSummary | null;
}) {
  const [open, setOpen] = useState(false);
  // undefined = not fetched yet, null = fetch failed/empty, object = loaded.
  const [rc, setRc] = useState<RootCause | AnomalyRootCause | null | undefined>(undefined);

  const onToggle = () => {
    const next = !open;
    setOpen(next);
    // Lazy first fetch — only when expanding, and only once.
    if (next && rc === undefined) {
      const p = anchor === 'anomaly' ? api.anomalyRootCause(id) : api.problemRootCause(id);
      p.then(r => setRc(r ?? null)).catch(() => setRc(null));
    }
  };

  const conf = summary?.confidence ?? 0;
  const hasCause = !!summary && summary.topSuspect !== '' && conf > 0.05;

  return (
    <div style={{ marginTop: 4 }}>
      {/* Collapsed chip — pure render from the list summary. */}
      <button
        type="button"
        onClick={(e) => { e.stopPropagation(); onToggle(); }}
        title={hasCause
          ? `Likely cause: ${summary!.topSuspect} · confidence ${pct(conf)} — click to expand the ranked candidates`
          : 'No clear cause synthesized yet — click for the full root-cause analysis'}
        style={{
          all: 'unset', cursor: 'pointer',
          display: 'inline-flex', alignItems: 'center', gap: 6,
          fontSize: 11, lineHeight: 1.3,
          padding: '2px 8px', borderRadius: 10,
          border: `1px solid ${hasCause ? 'color-mix(in srgb, var(--accent) 40%, transparent)' : 'var(--border)'}`,
          background: hasCause ? 'var(--accent-soft)' : 'var(--bg2)',
          color: 'var(--text2)', maxWidth: '100%',
        }}>
        <span style={{
          fontWeight: 700, textTransform: 'uppercase', letterSpacing: '.04em',
          fontSize: 9.5, color: hasCause ? 'var(--accent)' : 'var(--text3)',
        }}>
          Root cause
        </span>
        {hasCause ? (
          <>
            <span className="mono" style={{
              fontWeight: 600, color: 'var(--text)',
              overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              maxWidth: 200,
            }}>
              {summary!.topSuspect}
            </span>
            <ConfidencePct conf={conf} />
          </>
        ) : (
          <span style={{ color: 'var(--text3)' }}>
            no clear cause yet
          </span>
        )}
        <span style={{ color: 'var(--text3)', transition: 'transform .12s', transform: open ? 'rotate(90deg)' : 'none' }}>▸</span>
      </button>

      {/* Expanded panel — the full fan-out, fetched on first open. */}
      {open && (
        <div
          onClick={(e) => e.stopPropagation()}
          style={{
            marginTop: 8, padding: '10px 12px',
            border: '1px solid var(--border)', borderRadius: 6,
            background: 'var(--bg1)',
          }}>
          {rc === undefined && <Spinner label="Assembling root-cause evidence…" />}
          {rc === null && (
            <div style={{ fontSize: 12, color: 'var(--err)' }}>
              Root-cause analysis failed to load. Check the server log.
            </div>
          )}
          {rc && <ExpandedBody rc={rc} />}
          {/* Optional Copilot prose narration on top of the deterministic
              ranking (rc #4). Lazy — only fetched when the operator clicks
              ✨ Explain (Copilot calls cost), never on mount/expand. */}
          {rc && <ExplainBlock anchor={anchor} id={id} />}
        </div>
      )}
    </div>
  );
}

// ConfidencePct — the NN% pill. High confidence reads in accent; low confidence
// dims to muted so the operator never over-trusts a thin hypothesis. No new
// palette — reuses --accent / --text3 (CLAUDE.md: confidence high→accent,
// low→muted).
function ConfidencePct({ conf }: { conf: number }) {
  const high = conf >= 0.5;
  return (
    <span className="mono" style={{
      fontWeight: 700, fontSize: 10.5,
      color: high ? 'var(--accent)' : 'var(--text3)',
    }}>
      {pct(conf)}
    </span>
  );
}

// ExpandedBody renders the ranked candidates + recent deploy + exemplar from the
// fetched fan-out. Shows an honest Empty when nothing correlated — a partial
// bundle (any of candidates / deploy / exemplar) still renders.
function ExpandedBody({ rc }: { rc: RootCause | AnomalyRootCause }) {
  // The persisted ranking isn't on the live fan-out; the candidates the operator
  // cares about here are the correlated services (the same signal the worker
  // ranks). Map them to the ScoredCause-shaped reason lines the ribbon shows.
  const candidates = candidatesFromBundle(rc);
  const nothing = candidates.length === 0 && !rc.recentDeploy && !rc.exemplar;

  if (nothing) {
    return (
      <Empty icon={<IconFlame size={24} />} title="No correlating signals">
        No recent deploy, ranked downstream suspect, or exemplar trace in the
        analysis window — the signal looks localized to <b>{rc.service}</b>.
      </Empty>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {/* Recent deploy — the strongest "what changed" signal when present. */}
      {rc.recentDeploy && (
        <div style={{ fontSize: 12, lineHeight: 1.5 }}>
          <Label>Recent deploy</Label>
          <div>
            <code>service.version={rc.recentDeploy.version}</code> first seen{' '}
            <b>{fmtDurShort(rc.recentDeploy.ageSeconds)}</b> before onset on{' '}
            <Link to={`/service?name=${encodeURIComponent(rc.service)}#deploys`} style={{ fontWeight: 600 }}>
              {rc.service}
            </Link>.
          </div>
        </div>
      )}

      {/* Ranked candidate causes. */}
      {candidates.length > 0 && (
        <div>
          <Label>Ranked candidates</Label>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {candidates.slice(0, 5).map((c, i) => (
              <div key={c.service} style={{ display: 'flex', alignItems: 'baseline', gap: 8, fontSize: 12 }}>
                <span className="mono" style={{ color: 'var(--text3)', flex: '0 0 16px' }}>{i + 1}</span>
                <Link to={`/service?name=${encodeURIComponent(c.service)}`} style={{ fontWeight: 600, flex: '0 0 auto' }}>
                  {c.service}
                </Link>
                <span className="badge b-info" style={{ fontSize: 10 }}>{Math.round(c.score)}</span>
                {c.hops > 0 && (
                  <span className="badge b-gray" style={{ fontSize: 10 }}>
                    {c.hops} hop{c.hops === 1 ? '' : 's'}
                  </span>
                )}
                {c.reason && (
                  <span style={{ color: 'var(--text2)', flex: 1, minWidth: 0,
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                    title={c.reason}>
                    {c.reason}
                  </span>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Exemplar trace — one representative bad trace, click-to-trace (reuses
          the /trace deep-link affordance from RootCausePanel). */}
      {rc.exemplar && (
        <div>
          <Label>Exemplar trace</Label>
          <Link to={`/trace?id=${encodeURIComponent(rc.exemplar.traceId)}`}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 10, flexWrap: 'wrap',
              padding: '6px 10px', borderRadius: 6, textDecoration: 'none',
              background: 'var(--bg2)', border: '1px solid var(--border)', fontSize: 12,
            }}>
            <span aria-hidden="true" style={{ color: 'var(--accent)' }}>◆</span>
            {rc.exemplar.statusCode === 'error'
              ? <span className="badge b-err">ERROR</span>
              : <span className="badge b-ok">OK</span>}
            <span style={{ fontWeight: 600, color: 'var(--text)' }}>{rc.exemplar.name}</span>
            <span className="mono" style={{ color: 'var(--text2)' }}>
              {(rc.exemplar.durationNs / 1e6).toFixed(1)} ms
            </span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
              {rc.exemplar.traceId.slice(0, 16)}… ↗
            </span>
          </Link>
        </div>
      )}
    </div>
  );
}

// ExplainBlock — the opt-in ✨ Explain affordance (rc #4). Turns the persisted
// deterministic ranking into an operator-readable paragraph via Copilot. LAZY:
// nothing is fetched until the operator clicks the button — a Copilot call is
// expensive, so it never auto-fires on mount/expand. Honest about degradation:
// a 404 (no hypothesis synthesized yet) or a 503 (Copilot not configured) or
// any other error collapses to a muted inline message, never a crash and never
// fabricated prose. The backend caches the prose keyed on the hypothesis
// version, so a re-click after a re-synthesis gets fresh narration.
function ExplainBlock({ anchor, id }: { anchor: 'problem' | 'anomaly'; id: string }) {
  const [loading, setLoading] = useState(false);
  // undefined = not asked yet, null = failed/empty (honest degraded), string = prose.
  const [prose, setProse] = useState<string | null | undefined>(undefined);

  const onExplain = () => {
    if (loading) return;
    setLoading(true);
    const p = anchor === 'anomaly' ? api.rootCauseExplain(id) : api.problemRootCauseExplain(id);
    p.then(r => {
      const text = r?.prose?.trim();
      setProse(text ? text : null);
    }).catch(() => setProse(null)).finally(() => setLoading(false));
  };

  return (
    <div style={{ marginTop: 4, paddingTop: 10, borderTop: '1px solid var(--border)' }}>
      {prose === undefined && !loading && (
        <Button variant="secondary" size="sm" onClick={onExplain}>
          ✨ Explain
        </Button>
      )}
      {loading && <Spinner label="Narrating the ranked evidence…" />}
      {prose === null && (
        <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic' }}>
          No narration available — the hypothesis isn't synthesized yet, or the
          AI copilot isn't configured.
        </div>
      )}
      {prose && (
        <div
          style={{
            fontSize: 12.5, lineHeight: 1.55, color: 'var(--text2)',
            padding: '10px 12px', borderRadius: 6,
            background: 'var(--accent-soft)',
            border: '1px solid color-mix(in srgb, var(--accent) 30%, transparent)',
          }}>
          <span aria-hidden="true" style={{ marginRight: 6 }}>✨</span>
          {prose}
        </div>
      )}
    </div>
  );
}

function Label({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      fontSize: 10, fontWeight: 700, letterSpacing: '.06em', textTransform: 'uppercase',
      color: 'var(--text3)', marginBottom: 5,
    }}>
      {children}
    </div>
  );
}

// candidatesFromBundle derives the ranked candidate list the expand shows from
// the live fan-out's correlated services — the same downstream-suspect signal
// the worker's persisted hypothesis ranks, surfaced here from the on-demand
// bundle so the expand always reflects the current window. Self is filtered out;
// reasons come from the correlation's own change descriptions.
function candidatesFromBundle(rc: RootCause | AnomalyRootCause): ScoredCause[] {
  return rc.correlations
    .filter(c => c.service && c.service !== rc.service)
    .map(c => ({
      service: c.service,
      score: c.score,
      hops: 0,
      reason: c.reasons?.[0],
    }))
    .sort((a, b) => b.score - a.score);
}

// pct — 0..1 fraction → whole-number percent string.
function pct(f: number): string {
  return `${Math.round(f * 100)}%`;
}

