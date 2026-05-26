import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { LogPatternAnomaly } from '@/lib/types';

// LogPatternStrip — v0.5.239 in-context anomaly shelf on /logs.
// Fetches the same curated log-pattern detector that powers
// /anomalies + Inbox; surfaces the live "new / 2x+ over
// baseline" hits as a horizontal chip strip so the operator
// catches log anomalies WITHOUT switching pages.
//
// Click a chip:
//   • Pattern name becomes a body substring search ("OOMKilled"
//     → search="OOMKilled"). For most curated patterns the
//     human-readable name IS a literal token in the log line, so
//     this narrows the table to the matching events.
//   • If the pattern carries a service, the service picker also
//     flips so the operator lands directly on the firing
//     service's stream.
//
// Empty list = strip renders nothing (no visual weight when
// there's no signal).

export function LogPatternStrip({ onSelect }: {
  onSelect: (q: { search: string; service: string }) => void;
}) {
  const [hits, setHits] = useState<LogPatternAnomaly[] | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    const fetchOnce = () => {
      api.logPatternAnomalies()
        .then(d => { if (!cancelled) setHits(d ?? []); })
        .catch(() => { if (!cancelled) setHits(null); });
    };
    fetchOnce();
    // Same 30s cadence as the rest of the page's auto-refresh
    // hooks — at billion-log/day this poll is cheap (60s server
    // cache fronts the detector run). v0.5.248 — skip when the
    // tab is hidden so we don't burn mobile/laptop battery on
    // unfocused windows.
    const id = setInterval(() => { if (!document.hidden) fetchOnce(); }, 30_000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  if (!hits || hits.length === 0) return null;

  return (
    <div style={{
      display: 'flex', gap: 6, marginBottom: 10, flexWrap: 'wrap',
      alignItems: 'center', fontSize: 11,
    }}>
      <span style={{ color: 'var(--text3)', fontWeight: 600,
        textTransform: 'uppercase', letterSpacing: 0.3 }}>
        Log anomalies (5m)
      </span>
      {hits.slice(0, 12).map((a, i) => (
        <PatternChip key={a.pattern + ':' + a.service + ':' + i}
          anomaly={a}
          onClick={() => onSelect({
            // Use the pattern name as a search substring; for
            // curated patterns this matches the source string
            // (e.g. "panic:" / "OOMKilled" / "Deadlock").
            search: searchTermFor(a),
            service: a.service || '',
          })}
          onPickService={(svc) => onSelect({
            search: searchTermFor(a),
            service: svc,
          })} />
      ))}
      {hits.length > 12 && (
        <span style={{ color: 'var(--text3)' }}>
          + {hits.length - 12} more
        </span>
      )}
    </div>
  );
}

function PatternChip({ anomaly, onClick, onPickService }: {
  anomaly: LogPatternAnomaly;
  onClick: () => void;
  onPickService: (svc: string) => void;
}) {
  const isNew = anomaly.kind === 'new';
  const palette = isNew
    ? { bg: 'rgba(239,68,68,0.12)', border: 'rgba(239,68,68,0.40)', color: 'var(--err)' }
    : { bg: 'rgba(250,204,21,0.12)', border: 'rgba(250,204,21,0.40)', color: 'var(--warn, #facc15)' };
  const tag = isNew
    ? 'NEW'
    : `${anomaly.ratio.toFixed(1)}×`;
  // v0.5.287 — per-service rosette beneath the main chip when
  // the pattern fires on more than one service. Operator can
  // tap a specific service to narrow filter to (pattern + svc),
  // or tap the main chip for the global "fires on any of these"
  // view.
  const topSvcs = anomaly.topServices ?? [];
  const showRosette = topSvcs.length > 1;
  return (
    <span style={{
      display: 'inline-flex', flexDirection: 'column', gap: 3,
      alignItems: 'flex-start',
    }}>
      <button type="button" onClick={onClick}
        title={`${anomaly.pattern} — ${fmtNum(anomaly.currentCount)} hits in the last 5min` +
          (anomaly.baselineCount > 0 ? ` (baseline ${fmtNum(anomaly.baselineCount)})` : '') +
          (anomaly.service ? `\ntop service: ${anomaly.service}` : '') +
          (showRosette ? `\nfires on: ${topSvcs.map(s => `${s.service} (${s.count})`).join(', ')}` : '') +
          (anomaly.sample ? `\nsample: ${anomaly.sample.slice(0, 200)}` : '') +
          '\n\nClick to filter the log stream to this pattern.'}
        style={{
          all: 'unset', cursor: 'pointer',
          display: 'inline-flex', alignItems: 'center', gap: 6,
          padding: '3px 8px', borderRadius: 12,
          background: palette.bg, border: `1px solid ${palette.border}`,
          color: palette.color, fontSize: 11, whiteSpace: 'nowrap',
        }}>
        <span style={{
          fontSize: 9, fontWeight: 700,
          padding: '0 4px', borderRadius: 8,
          background: 'rgba(0,0,0,0.20)',
        }}>{tag}</span>
        <span style={{ fontWeight: 600 }}>{anomaly.pattern}</span>
        <span style={{ color: 'var(--text3)', fontFamily: 'ui-monospace, monospace' }}>
          {fmtNum(anomaly.currentCount)}
        </span>
        {!showRosette && anomaly.service && (
          <span style={{
            color: 'var(--text3)', fontSize: 10,
            padding: '0 4px', borderRadius: 6,
            background: 'var(--bg3)',
          }}>{anomaly.service}</span>
        )}
      </button>
      {showRosette && (
        <span style={{
          display: 'inline-flex', flexWrap: 'wrap', gap: 3,
          paddingLeft: 4, alignItems: 'center',
        }}>
          {topSvcs.slice(0, 4).map(s => (
            <button key={s.service} type="button"
              onClick={(e) => { e.stopPropagation(); onPickService(s.service); }}
              title={`Narrow filter to ${anomaly.pattern} on ${s.service} (${fmtNum(s.count)} hits)`}
              style={{
                all: 'unset', cursor: 'pointer',
                fontSize: 10, padding: '1px 6px', borderRadius: 8,
                background: 'var(--bg3)', color: 'var(--text2)',
                border: '1px solid var(--border)',
                fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                whiteSpace: 'nowrap',
              }}>
              {s.service}
              <span style={{ color: 'var(--text3)', marginLeft: 4 }}>
                {fmtNum(s.count)}
              </span>
            </button>
          ))}
          {topSvcs.length > 4 && (
            <span style={{ fontSize: 10, color: 'var(--text3)' }}>
              +{topSvcs.length - 4}
            </span>
          )}
        </span>
      )}
    </span>
  );
}

// searchTermFor — pattern name → /logs search box value.
// v0.5.477 — operator-reported (verbatim): clicking "JDBC pool
// exhausted" landed in the filter as just "JDBC". The previous
// first-word-only logic was meant to avoid the operator's typed
// AND being misparsed by query_string, but at the cost of
// losing the rest of the phrase that's often the most
// distinguishing piece ("pool exhausted" disambiguates from
// "JDBC connect", "JDBC closed", etc.).
//
// Multi-word patterns now emit as a double-quoted phrase so
// query_string treats them as a phrase match on the body
// field. CH's multiSearchAnyCaseInsensitive treats the quoted
// form as a single substring needle — same intent. Single-word
// patterns pass through with surrounding punctuation stripped
// (the v0.5.239 behaviour we want to keep for "panic:" /
// "OOMKilled" / "Deadlock").
function searchTermFor(a: LogPatternAnomaly): string {
  const name = a.pattern.trim();
  if (!name) return '';
  if (/\s/.test(name)) {
    return `"${name.replace(/"/g, '\\"')}"`;
  }
  return name.replace(/[():]/g, '').trim();
}
