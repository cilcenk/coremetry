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
  onSelect,
}: {
  onSelect: (token: string) => void;
}) {
  const [data, setData] = useState<{
    backend: string;
    window: string;
    baseline: string;
    patterns: Pattern[];
  } | null | undefined>(undefined);
  const [collapsed, setCollapsed] = useState(false);

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

  if (!data || data.patterns.length === 0) return null;
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
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {data.patterns.slice(0, 20).map(p => (
            <PatternChip key={p.token} pat={p} topScore={topScore}
              onClick={() => onSelect(p.token)} />
          ))}
        </div>
      )}
    </div>
  );
}

function PatternChip({ pat, topScore, onClick }: {
  pat: Pattern;
  topScore: number;
  onClick: () => void;
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
    <button type="button" onClick={onClick}
      title={`Token: ${pat.token}\n${fmtNum(pat.docCount)} hits in current window\n${fmtNum(pat.bgCount)} hits in baseline\nscore: ${pat.score.toFixed(3)}\n\nClick to filter the log stream to this token.`}
      style={{
        all: 'unset', cursor: 'pointer',
        display: 'inline-flex', alignItems: 'center', gap: 6,
        padding: '3px 8px', borderRadius: 12,
        background: palette.bg, border: `1px solid ${palette.border}`,
        color: palette.color, whiteSpace: 'nowrap', fontSize: 11,
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
  );
}
