import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange } from '@/lib/urlState';
import type { EndpointRow, TimeRange } from '@/lib/types';

// /endpoints — operator-asked v0.5.365. Cross-service inbound
// RED rollup keyed on http.route (templated) with url.path /
// http.target fallbacks. Backend resolves the priority chain
// per row so this page only deals with the resolved string.
// Mirrors the /services list ergonomics: search + service
// filter + sortable columns + drill-throughs into /traces and
// /service detail.

type SortKey = 'service' | 'path' | 'calls' | 'errors' | 'errorRate' | 'avgMs' | 'p99Ms';

export default function EndpointsPage() {
  const [params, setParams] = useSearchParams();
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [rows, setRows] = useState<EndpointRow[] | null | undefined>(undefined);
  const [search, setSearch] = useState(() => params.get('search') ?? '');
  const service = params.get('service') ?? '';
  const setService = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('service', v); else next.delete('service');
    return next;
  }, { replace: true });
  const setSearchParam = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('search', v); else next.delete('search');
    return next;
  }, { replace: true });

  const [sortKey, setSortKey] = useState<SortKey>('calls');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    setRows(undefined);
    api.endpoints({
      from, to,
      service: service || undefined,
      search: search.trim() || undefined,
      limit: 1000,
    })
      .then(r => setRows(r ?? []))
      .catch(() => setRows(null));
  }, [from, to, service, search]);

  const sorted = useMemo(() => {
    const list = rows ?? [];
    const arr = [...list].sort((a, b) => {
      const dir = sortDir === 'asc' ? 1 : -1;
      switch (sortKey) {
        case 'service':   return dir * a.service.localeCompare(b.service);
        case 'path':      return dir * a.path.localeCompare(b.path);
        case 'calls':     return dir * (a.calls - b.calls);
        case 'errors':    return dir * (a.errors - b.errors);
        case 'errorRate': return dir * (a.errorRate - b.errorRate);
        case 'avgMs':     return dir * (a.avgMs - b.avgMs);
        case 'p99Ms':     return dir * (a.p99Ms - b.p99Ms);
      }
    });
    return arr;
  }, [rows, sortKey, sortDir]);

  const setSort = (k: SortKey) => {
    if (k === sortKey) setSortDir(d => d === 'asc' ? 'desc' : 'asc');
    else { setSortKey(k); setSortDir(k === 'service' || k === 'path' ? 'asc' : 'desc'); }
  };

  const totalCalls = (rows ?? []).reduce((s, r) => s + r.calls, 0);
  const totalErrors = (rows ?? []).reduce((s, r) => s + r.errors, 0);
  const totalErrorRate = totalCalls > 0 ? (totalErrors / totalCalls) * 100 : 0;

  return (
    <>
      <Topbar title="Endpoints" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ flexWrap: 'wrap', marginBottom: 12 }}>
          <ServicePicker value={service} onChange={setService}
            placeholder="All services…" width={200} />
          <input value={search}
            onChange={e => { setSearch(e.target.value); setSearchParam(e.target.value); }}
            placeholder="Filter by path (substring)…"
            style={{ width: 280, padding: '5px 10px', fontSize: 12,
                     background: 'var(--bg)', color: 'var(--text)',
                     border: '1px solid var(--border)', borderRadius: 4 }} />
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {rows ? `${rows.length} endpoint${rows.length === 1 ? '' : 's'}` : ''}
          </span>
        </div>

        {rows === undefined && <Spinner />}
        {rows === null && (
          <Empty icon="⚠" title="Failed to load endpoints">
            The backend /api/endpoints request errored.
          </Empty>
        )}
        {rows && rows.length === 0 && (
          <Empty icon="∅" title="No endpoints in window">
            <div style={{ fontSize: 12, color: 'var(--text2)', maxWidth: 520, marginTop: 8, lineHeight: 1.5 }}>
              No spans with <code>http.route</code> / <code>url.path</code> / <code>http.target</code> attrs
              landed in this window. Try widening the time range, or check
              that your services emit one of those attributes on server-kind spans.
            </div>
          </Empty>
        )}
        {rows && rows.length > 0 && (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
              gap: 12, marginBottom: 14,
            }}>
              <KPI label="Endpoints" value={fmtNum(rows.length)} />
              <KPI label="Total calls" value={fmtNum(totalCalls)} />
              <KPI label="Errors" value={fmtNum(totalErrors)}
                   sub={`${totalErrorRate.toFixed(2)}%`}
                   cls={totalErrorRate >= 5 ? 'err' : totalErrorRate >= 1 ? 'warn' : ''} />
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <SortHeader k="service"   label="Service"    cur={sortKey} dir={sortDir} onSort={setSort} />
                    <SortHeader k="path"      label="Path"       cur={sortKey} dir={sortDir} onSort={setSort} />
                    <th>Method</th>
                    <SortHeader k="calls"     label="Calls"      cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="errors"    label="Errors"     cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="errorRate" label="Error rate" cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="avgMs"     label="Avg"        cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="p99Ms"     label="P99"        cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <th>Traces</th>
                  </tr>
                </thead>
                <tbody>
                  {sorted.map((r, i) => {
                    const errCls = r.errorRate >= 5 ? 'b-err' : r.errorRate >= 1 ? 'b-warn' : 'b-ok';
                    return (
                      <tr key={`${r.service}|${r.path}|${i}`}
                          style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                        <td>
                          <Link to={`/services?name=${encodeURIComponent(r.service)}`}
                                style={{ fontFamily: 'monospace', fontSize: 12 }}>
                            {r.service}
                          </Link>
                        </td>
                        <td className="mono" style={{ fontSize: 12, wordBreak: 'break-all' }} title={r.path}>
                          {r.path}
                        </td>
                        <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                          {r.method || '—'}
                        </td>
                        <td className="num mono">{fmtNum(r.calls)}</td>
                        <td className="num mono">{fmtNum(r.errors)}</td>
                        <td className="num mono">
                          <span className={`badge ${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                        </td>
                        <td className="num mono">{r.avgMs.toFixed(1)} ms</td>
                        <td className="num mono">{r.p99Ms.toFixed(0)} ms</td>
                        <td>
                          {/* /traces filter on (service, search=path).
                              The search field matches span.name OR
                              attrs; combined with rootOnly=false and
                              the service filter, this returns every
                              trace that includes a call on this
                              endpoint. */}
                          <Link to={tracesLink(r, range)}
                                style={{ fontSize: 11, color: 'var(--accent2)' }}>
                            view →
                          </Link>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
              Path source priority: <code>http.route</code> (templated) →
              {' '}<code>url.path</code> → <code>http.target</code>.
              Server / consumer spans only — outbound client spans count under
              the callee's row.
            </div>
          </>
        )}
      </div>
    </>
  );
}

function tracesLink(r: EndpointRow, range: TimeRange): string {
  return `/traces?service=${encodeURIComponent(r.service)}` +
    `&search=${encodeURIComponent(r.path)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    `&view=list&rootOnly=false`;
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: '8px 14px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 2 }}>{label}</div>
      <div style={{
        fontSize: 22, fontWeight: 600,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

function SortHeader({ k, label, cur, dir, onSort, num }: {
  k: SortKey; label: string; cur: SortKey; dir: 'asc' | 'desc';
  onSort: (k: SortKey) => void; num?: boolean;
}) {
  const active = cur === k;
  return (
    <th onClick={() => onSort(k)} className={num ? 'num' : ''}
        style={{ cursor: 'pointer', userSelect: 'none' }}>
      <span style={{ color: active ? 'var(--text)' : 'var(--text2)' }}>
        {label}{active ? (dir === 'asc' ? ' ▲' : ' ▼') : ''}
      </span>
    </th>
  );
}
