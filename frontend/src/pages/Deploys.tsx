import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';

// /deploys — v0.5.289. Cross-service deploy timeline. The
// What-changed banner only carries the 30-min slice; this page
// is the "what's been shipping in the last day / week / month"
// review. Uses the v0.5.283 effective-version chain (Helm
// labels, image tags, placeholders filtered) so installs without
// service.version still surface their releases.
//
// No time range picker — the page-local 24h/7d/30d preset is
// simpler than the Topbar range model since the window only
// affects this list. Server caps at 30 days hard.

type Deploy = {
  service: string;
  version: string;
  firstSeenNs: number;
  spanCount: number;
};

const PRESETS: { label: string; hours: number }[] = [
  { label: '24h', hours: 24 },
  { label: '7d',  hours: 24 * 7 },
  { label: '30d', hours: 24 * 30 },
];

export default function DeploysPage() {
  const [hours, setHours] = useState(24);
  const [rows, setRows] = useState<Deploy[] | null | undefined>(undefined);
  const [filter, setFilter] = useState('');

  useEffect(() => {
    setRows(undefined);
    api.allDeploys(hours, 1000)
      .then(d => setRows(d ?? []))
      .catch(() => setRows(null));
  }, [hours]);

  // Client-side substring filter on service / version so an
  // operator can narrow a 1000-row timeline without a refetch.
  const visible = useMemo(() => {
    if (!rows) return rows;
    const t = filter.trim().toLowerCase();
    if (!t) return rows;
    return rows.filter(r =>
      r.service.toLowerCase().includes(t)
      || r.version.toLowerCase().includes(t));
  }, [rows, filter]);

  // Group rows by service so the page reads as a stable per-
  // service timeline rather than a flat undifferentiated list.
  // Sort group order by most-recent deploy desc.
  const groups = useMemo(() => {
    if (!visible) return [] as { service: string; deploys: Deploy[] }[];
    const m = new Map<string, Deploy[]>();
    for (const r of visible) {
      const arr = m.get(r.service) ?? [];
      arr.push(r);
      m.set(r.service, arr);
    }
    const out: { service: string; deploys: Deploy[] }[] = [];
    for (const [service, deploys] of m) {
      deploys.sort((a, b) => b.firstSeenNs - a.firstSeenNs);
      out.push({ service, deploys });
    }
    out.sort((a, b) =>
      b.deploys[0].firstSeenNs - a.deploys[0].firstSeenNs);
    return out;
  }, [visible]);

  return (
    <>
      <Topbar title="Deploys" />
      <div id="content">
        <div className="controls" style={{ marginBottom: 12, gap: 12, flexWrap: 'wrap' }}>
          <label style={{ fontSize: 12, color: 'var(--text2)' }}>Window</label>
          {PRESETS.map(p => (
            <button key={p.label}
              className={hours === p.hours ? '' : 'sec'}
              onClick={() => setHours(p.hours)}
              style={{ fontSize: 12, padding: '3px 12px' }}>
              {p.label}
            </button>
          ))}
          <input type="search" placeholder="Filter service or version…"
            value={filter} onChange={e => setFilter(e.target.value)}
            style={{ fontSize: 12, padding: '3px 8px', width: 240, marginLeft: 8 }} />
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
            {rows && visible && (
              <>
                {fmtNum(visible.length)}
                {filter && rows.length !== visible.length && (
                  <> / {fmtNum(rows.length)}</>
                )} deploys · {fmtNum(groups.length)} services
              </>
            )}
          </span>
        </div>
        {rows === undefined && <Spinner />}
        {rows === null && <Empty icon="✗" title="Failed to load deploys" />}
        {rows && rows.length === 0 && (
          <Empty icon="◇" title="No deploys in this window">
            Widen the window, or check whether your services emit a
            service.version / container.image.tag / app.kubernetes.io/version
            resource attribute. The effective-version chain (v0.5.283)
            covers all three plus Helm chart version.
          </Empty>
        )}
        {visible && visible.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead><tr>
                <th>Time</th>
                <th>Service</th>
                <th>Version</th>
                <th style={{ width: 110 }} className="num">Span count</th>
                <th style={{ width: 90 }}>Drill</th>
              </tr></thead>
              <tbody>
                {groups.map(g => (
                  g.deploys.map((d, i) => {
                    const isFirst = i === 0;
                    return (
                      <tr key={`${g.service}|${d.version}|${d.firstSeenNs}`}
                        style={isFirst ? {
                          borderTop: '2px solid var(--border)',
                        } : undefined}>
                        <td className="mono" style={{ fontSize: 11, color: isFirst ? 'var(--text)' : 'var(--text3)' }}>
                          {tsLong(d.firstSeenNs)}
                          <span style={{ marginLeft: 6, color: 'var(--text3)' }}>
                            {ageLabel(d.firstSeenNs)}
                          </span>
                        </td>
                        <td style={{ fontFamily: 'monospace', fontSize: 12 }}>
                          {isFirst ? (
                            <Link to={`/service?name=${encodeURIComponent(g.service)}`}>
                              {g.service}
                            </Link>
                          ) : (
                            <span style={{ color: 'var(--text3)' }}>↳</span>
                          )}
                        </td>
                        <td className="mono" style={{ fontSize: 12, fontWeight: isFirst ? 700 : 400 }}>
                          {d.version}
                        </td>
                        <td className="num mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                          {fmtNum(d.spanCount)}
                        </td>
                        <td>
                          <Link to={`/service?name=${encodeURIComponent(g.service)}#deploys`}
                                style={{ fontSize: 11 }}>
                            history →
                          </Link>
                        </td>
                      </tr>
                    );
                  })
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

function ageLabel(ns: number): string {
  const ageSec = Math.max(1, Math.round((Date.now() - ns / 1e6) / 1000));
  if (ageSec < 60) return `${ageSec}s ago`;
  if (ageSec < 3600) return `${Math.round(ageSec / 60)}m ago`;
  if (ageSec < 86400) return `${Math.round(ageSec / 3600)}h ago`;
  return `${Math.round(ageSec / 86400)}d ago`;
}
