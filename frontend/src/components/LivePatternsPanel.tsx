import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';

// LivePatternsPanel — v0.5.243 unsupervised log-anomaly panel
// on /logs. Calls the ES `significant_text` aggregation through
// /api/logs/patterns and renders the top-N rare-in-cur-vs-base
// tokens as click-to-filter chips. CH backend returns empty;
// panel hides itself silently in that case.
//
// Difference vs LogPatternStrip (v0.5.239): that's CURATED
// regex patterns (OOMKilled / panic / NPE / etc.). This panel
// is UNSUPERVISED — finds tokens that just got over-represented
// vs the trailing baseline. Java apps emit class names like
// "NullPointerException", "ClassCastException", logger paths
// like "com.example.OrderService" — these score high on
// significant_text because the standard analyzer tokenises
// them as single units.

type Pattern = { token: string; docCount: number; bgCount: number; score: number };

export function LivePatternsPanel({
  onSelect, onTracePeek,
}: {
  onSelect: (token: string) => void;
  // v0.5.401 — sample-trace preview. When a pattern chip's "👁"
  // button is clicked, the panel fetches 3 sample traces
  // containing the token (via /api/logs/similar more_like_this)
  // and renders a strip below the chips. Clicking a trace ID
  // opens the parent's TracePeekDrawer for inline summary.
  // Optional — when omitted the preview just shows trace IDs
  // as plain text without the drill-in affordance.
  onTracePeek?: (traceId: string) => void;
}) {
  const [data, setData] = useState<{
    backend: string;
    window: string;
    baseline: string;
    patterns: Pattern[];
    timedOut?: boolean;
  } | null | undefined>(undefined);
  const [collapsed, setCollapsed] = useState(false);
  // Pattern token whose sample-trace preview is currently open.
  // null = no preview. Single preview at a time keeps the UI
  // tight; switching tokens swaps the contents.
  const [previewToken, setPreviewToken] = useState<string | null>(null);
  const [previewTraces, setPreviewTraces] = useState<Array<{ traceId: string; count: number }> | null | undefined>(undefined);

  useEffect(() => {
    if (!previewToken) {
      setPreviewTraces(undefined);
      return;
    }
    let cancelled = false;
    setPreviewTraces(undefined);
    api.logsSimilarTraces(previewToken, 3)
      .then(r => { if (!cancelled) setPreviewTraces(r?.traces ?? []); })
      .catch(() => { if (!cancelled) setPreviewTraces(null); });
    return () => { cancelled = true; };
  }, [previewToken]);

  useEffect(() => {
    let cancelled = false;
    const fetchOnce = () => {
      api.logsSignificantPatterns({ window: '15m', baseline: '24h', topN: 25 })
        .then(d => { if (!cancelled) setData(d ?? null); })
        .catch(() => { if (!cancelled) setData(null); });
    };
    fetchOnce();
    // 30s poll; the server caches the agg for 60s so half of
    // these are no-ops at the ES layer. v0.5.248 — pause when
    // the tab is hidden so we don't run significant_text on a
    // backgrounded operator session.
    const id = setInterval(() => { if (!document.hidden) fetchOnce(); }, 30_000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  if (!data) return null;
  // v0.5.390 — render a "still computing" hint when the backend
  // bailed on the 15s deadline; polling will re-fetch in 30s
  // and ES's request_cache + sampler.shard_size=20k usually
  // settles it on the next attempt. Don't hide the panel
  // outright (the operator would think it was disabled).
  if (data.patterns.length === 0) {
    if (data.timedOut) {
      return (
        <div style={{
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 10, marginBottom: 10,
          fontSize: 11, color: 'var(--text3)', lineHeight: 1.5,
        }}>
          <strong style={{ color: 'var(--text2)' }}>Live patterns</strong>
          {' '}— ES sampling is still computing this window; the
          next 30s poll will retry. (significant_text timed out
          server-side; partial results were empty.)
        </div>
      );
    }
    return null;
  }
  // v0.5.298 — CH backend now ships its own rare-token scorer
  // (sample-based foreground vs background frequency ratio,
  // not as statistically tight as ES significant_text but the
  // same operator-facing "what's rare-but-rising right now"
  // signal). Render the panel for both backends when data is
  // non-empty.

  const topScore = data.patterns[0]?.score ?? 1;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 10, marginBottom: 10, fontSize: 12,
    }}>
      <button type="button" onClick={() => setCollapsed(c => !c)}
        style={{
          all: 'unset', cursor: 'pointer',
          display: 'flex', alignItems: 'baseline', gap: 8, width: '100%',
          marginBottom: collapsed ? 0 : 8,
        }}>
        <span style={{ fontSize: 11, color: 'var(--text3)',
          fontFamily: 'ui-monospace, monospace' }}>{collapsed ? '▶' : '▼'}</span>
        <span style={{ fontWeight: 700, color: 'var(--text2)',
          textTransform: 'uppercase', letterSpacing: 0.4 }}>
          Live patterns
        </span>
        <span style={{ color: 'var(--text3)', fontSize: 11 }}>
          tokens rare in last {data.window} vs {data.baseline} baseline
          {' · '}{data.patterns.length} signal{data.patterns.length === 1 ? '' : 's'}
        </span>
      </button>
      {!collapsed && (
        <>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {data.patterns.slice(0, 20).map(p => (
              <PatternChip key={p.token} pat={p} topScore={topScore}
                onClick={() => onSelect(p.token)}
                onPreview={() => setPreviewToken(p.token === previewToken ? null : p.token)}
                previewing={p.token === previewToken} />
            ))}
          </div>
          {previewToken && (
            <div style={{
              marginTop: 8, padding: '8px 10px',
              background: 'var(--bg2)', borderRadius: 4,
              border: '1px dashed var(--border)',
              fontSize: 11,
            }}>
              <div style={{
                display: 'flex', justifyContent: 'space-between',
                alignItems: 'baseline', marginBottom: 6,
              }}>
                <span style={{ color: 'var(--text2)' }}>
                  Sample traces containing
                  {' '}<code className="mono" style={{ color: 'var(--text)' }}>{previewToken}</code>
                </span>
                <button type="button"
                  onClick={() => setPreviewToken(null)}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    fontSize: 11, color: 'var(--text3)', padding: '0 4px',
                  }}>×</button>
              </div>
              {previewTraces === undefined && (
                <span style={{ color: 'var(--text3)' }}>Loading…</span>
              )}
              {previewTraces === null && (
                <span style={{ color: 'var(--err)' }}>
                  Failed to fetch sample traces. (more_like_this ES-only;
                  CH-backed installs don't surface this affordance.)
                </span>
              )}
              {previewTraces && previewTraces.length === 0 && (
                <span style={{ color: 'var(--text3)' }}>
                  No matching trace_ids surfaced — the token may live in
                  logs without trace correlation.
                </span>
              )}
              {previewTraces && previewTraces.length > 0 && (
                <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                  {previewTraces.map(t => (
                    <button key={t.traceId}
                      type="button"
                      onClick={() => onTracePeek?.(t.traceId)}
                      title={t.count > 1
                        ? `${t.count} log lines in this trace match the pattern`
                        : 'Open trace summary inline'}
                      style={{
                        all: 'unset', cursor: 'pointer',
                        display: 'inline-flex', alignItems: 'center', gap: 6,
                        padding: '3px 8px', borderRadius: 12,
                        background: 'rgba(56,139,253,0.10)',
                        border: '1px solid rgba(56,139,253,0.35)',
                        color: 'var(--accent2)',
                        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                        fontSize: 10,
                      }}>
                      👁 {t.traceId.slice(0, 12)}…
                      {t.count > 1 && (
                        <span style={{ color: 'var(--text3)' }}>
                          ×{t.count}
                        </span>
                      )}
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function PatternChip({ pat, topScore, onClick, onPreview, previewing }: {
  pat: Pattern;
  topScore: number;
  onClick: () => void;
  onPreview: () => void;
  previewing: boolean;
}) {
  // Score-based heat. Top scorer ≥ 0.5 = red (clear anomaly),
  // mid range = amber, low = grey. ES scores aren't normalised
  // so we pick palette relative to the top score in this slice.
  const heat = topScore > 0 ? pat.score / topScore : 0;
  const palette = heat >= 0.7
    ? { bg: 'rgba(239,68,68,0.12)', border: 'rgba(239,68,68,0.40)', color: 'var(--err)' }
    : heat >= 0.35
    ? { bg: 'rgba(250,204,21,0.10)', border: 'rgba(250,204,21,0.35)', color: 'var(--warn, #facc15)' }
    : { bg: 'rgba(148,163,184,0.08)', border: 'rgba(148,163,184,0.25)', color: 'var(--text2)' };

  const delta = pat.bgCount > 0
    ? (pat.docCount / pat.bgCount).toFixed(1) + '×'
    : 'NEW';

  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 0,
      borderRadius: 12,
      background: palette.bg, border: `1px solid ${palette.border}`,
      whiteSpace: 'nowrap',
      outline: previewing ? '1px solid var(--accent2)' : 'none',
      outlineOffset: previewing ? 1 : 0,
    }}>
      <button type="button" onClick={onClick}
        title={`Click: filter logs to this token.\n\nToken: ${pat.token}\n${fmtNum(pat.docCount)} hits in current window\n${fmtNum(pat.bgCount)} hits in baseline\nscore: ${pat.score.toFixed(3)}`}
        style={{
          all: 'unset', cursor: 'pointer',
          display: 'inline-flex', alignItems: 'center', gap: 6,
          padding: '3px 4px 3px 8px',
          color: palette.color, fontSize: 11,
        }}>
        <span style={{
          fontSize: 9, fontWeight: 700,
          padding: '0 4px', borderRadius: 8,
          background: 'rgba(0,0,0,0.20)',
        }}>{delta}</span>
        <span style={{
          fontWeight: 600,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>
          {pat.token}
        </span>
        <span style={{ color: 'var(--text3)', fontSize: 10,
          fontFamily: 'ui-monospace, monospace' }}>
          {fmtNum(pat.docCount)}
        </span>
      </button>
      {/* v0.5.401 — eye button opens the sample-trace preview
          below the chip strip. Separate target keeps the primary
          "filter logs" click discoverable + non-destructive. */}
      <button type="button" onClick={onPreview}
        title={previewing
          ? 'Close sample-trace preview'
          : 'Preview 3 sample traces containing this token'}
        style={{
          all: 'unset', cursor: 'pointer',
          padding: '3px 7px 3px 4px',
          color: previewing ? 'var(--accent2)' : 'var(--text3)',
          fontSize: 11,
        }}>👁</button>
    </span>
  );
}
