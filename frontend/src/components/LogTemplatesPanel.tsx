import { useEffect, useMemo, useState } from 'react';
import { api } from '@/lib/api';
import { Spinner } from './Spinner';
import { fmtNum, tsLong, tsRel } from '@/lib/utils';
import type { LogTemplate } from '@/lib/types';

// LogTemplatesPanel — v0.5.244 Drain-extracted template ledger
// rendered as a collapsible section on /logs. Default sort:
// first_seen DESC ("what shapes JUST started appearing?").
//
// Each row shows the canonical template with <*> in variable
// positions plus its running count + services + "new since"
// badge. Click → use a few representative tokens as a body
// substring search so the table narrows to lines matching
// that shape. (Can't filter exactly by template_id without
// re-indexing the logs table; substring search is the
// pragmatic match.)

type Sort = 'first_seen' | 'last_seen' | 'count';

export function LogTemplatesPanel({
  onSelectTemplate,
}: {
  onSelectTemplate: (substring: string) => void;
}) {
  const [data, setData] = useState<LogTemplate[] | null | undefined>(undefined);
  const [sort, setSort] = useState<Sort>('first_seen');
  // v0.5.474 — operator-reported: this panel was open by default,
  // pushing the log rows below the fold on first paint. Default
  // closed; operator clicks the header to expand. Polling is
  // also gated on !collapsed so the 30s tick doesn't burn API
  // calls while the panel is hidden.
  const [collapsed, setCollapsed] = useState(true);

  useEffect(() => {
    if (collapsed) return;
    let cancelled = false;
    const fetchOnce = () => {
      api.logsTemplates({ sort, since: '24h', limit: 50 })
        .then(d => { if (!cancelled) setData(d ?? []); })
        .catch(() => { if (!cancelled) setData(null); });
    };
    fetchOnce();
    // 30s refresh — server caches the read for 30s so the
    // poll is cheap on the API side. v0.5.248 — pause when
    // the tab is hidden (mobile battery + idle API traffic).
    const id = setInterval(() => { if (!document.hidden) fetchOnce(); }, 30_000);
    return () => { cancelled = true; clearInterval(id); };
  }, [sort, collapsed]);

  const newCount = useMemo(() => {
    if (!data) return 0;
    const hour = Date.now() * 1_000_000 - 60 * 60 * 1_000_000_000;
    return data.filter(t => t.firstSeen > hour).length;
  }, [data]);

  // v0.5.474 — render the header even when data hasn't loaded
  // (collapsed by default → polling gated on expand → data
  // would never load before first expand otherwise). Rows only
  // render when expanded AND data resolves to a non-empty list.
  const hasData = data && data.length > 0;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 10, marginBottom: 10, fontSize: 12,
    }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: collapsed ? 0 : 8,
      }}>
        <button type="button" onClick={() => setCollapsed(c => !c)}
          style={{
            all: 'unset', cursor: 'pointer',
            display: 'inline-flex', alignItems: 'baseline', gap: 8, flex: 1,
          }}>
          <span style={{ fontSize: 11, color: 'var(--text3)',
            fontFamily: 'ui-monospace, monospace' }}>{collapsed ? '▶' : '▼'}</span>
          <span style={{ fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.4 }}>
            Templates
          </span>
          <span style={{ color: 'var(--text3)', fontSize: 11 }}>
            {hasData
              ? <>Drain-extracted shapes · {data!.length} active</>
              : <>Drain-extracted shapes · click to load</>}
            {hasData && newCount > 0 && (
              <span style={{ color: 'var(--err)', marginLeft: 8, fontWeight: 600 }}>
                · {newCount} new in last hour
              </span>
            )}
          </span>
        </button>
        {!collapsed && (
          <select value={sort} onChange={e => setSort(e.target.value as Sort)}
            style={{ fontSize: 11, padding: '2px 6px' }}
            title="Sort templates by recency or volume">
            <option value="first_seen">First seen (newest)</option>
            <option value="last_seen">Last seen</option>
            <option value="count">Total count</option>
          </select>
        )}
      </div>
      {!collapsed && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          {data === undefined && (
            <Spinner label="Loading log templates…" hint="Drain-extracted shapes; ~200ms at scale." />
          )}
          {hasData && data!.slice(0, 30).map(t => (
            <TemplateRow key={t.id} t={t} onClick={() => onSelectTemplate(distinctiveTokens(t.template))} />
          ))}
          {data !== undefined && !hasData && (
            <div style={{ color: 'var(--text3)', fontSize: 11, padding: '4px 0' }}>
              No templates extracted in the last 24h.
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function TemplateRow({ t, onClick }: { t: LogTemplate; onClick: () => void }) {
  const hour = Date.now() * 1_000_000 - 60 * 60 * 1_000_000_000;
  const isNew = t.firstSeen > hour;
  return (
    <button type="button" onClick={onClick}
      title={`First seen: ${tsLong(t.firstSeen)}\nLast seen: ${tsLong(t.lastSeen)}\n${fmtNum(t.totalCount)} occurrences\nServices: ${t.services.join(', ') || '(none)'}\n\nSample: ${t.sample.slice(0, 200)}`}
      style={{
        all: 'unset', cursor: 'pointer',
        display: 'grid',
        gridTemplateColumns: '60px 1fr 80px 100px',
        gap: 8, alignItems: 'center',
        padding: '4px 8px', borderRadius: 4,
        background: isNew ? 'rgba(239,68,68,0.06)' : 'transparent',
        borderLeft: isNew ? '3px solid var(--err)' : '3px solid transparent',
        fontSize: 11,
      }}
      className="log-template-row">
      <span style={{
        fontSize: 10, fontWeight: 700,
        padding: '1px 4px', borderRadius: 8,
        background: isNew ? 'rgba(239,68,68,0.18)' : 'rgba(148,163,184,0.10)',
        color: isNew ? 'var(--err)' : 'var(--text3)',
        textAlign: 'center',
      }}>
        {isNew ? 'NEW' : tsRel(t.firstSeen)}
      </span>
      <span style={{
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
        color: 'var(--text)',
      }}>
        {t.template}
      </span>
      <span style={{ color: 'var(--text3)', fontFamily: 'ui-monospace, monospace', textAlign: 'right' }}>
        {fmtNum(t.totalCount)}
      </span>
      <span style={{
        fontSize: 10, color: 'var(--text3)',
        whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
      }}>
        {t.services.length > 0 ? t.services.slice(0, 2).join(', ') : '—'}
        {t.services.length > 2 && ` +${t.services.length - 2}`}
      </span>
    </button>
  );
}

// distinctiveTokens picks the two most informative non-"<*>"
// tokens from a template — used as the body substring search
// when the operator clicks a template row. Picking single
// distinctive tokens beats sending the full template (which
// contains generic words like "to" / "in" / "the" that match
// everything).
function distinctiveTokens(template: string): string {
  const tokens = template.split(/\s+/).filter(t => t !== '<*>');
  // Score by length + capitalised words (Java class names rank
  // high here) + presence of dots (logger paths). Pick top 2.
  const scored = tokens.map(t => ({
    tok: t,
    score: t.length + (/^[A-Z]/.test(t) ? 4 : 0) + (t.includes('.') ? 3 : 0),
  })).sort((a, b) => b.score - a.score);
  const top = scored.slice(0, 2).map(s => s.tok);
  // Quote each so Lucene treats dashes / dots / colons as
  // literal — matches the rest of the page's filter quoting
  // discipline (v0.5.230).
  return top.map(t => `"${t}"`).join(' ');
}
