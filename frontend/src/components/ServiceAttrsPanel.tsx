import { useEffect, useMemo, useState } from 'react';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import type { ServiceAttrRow, TimeRange } from '@/lib/types';

// ServiceAttrsPanel — v0.5.381. Surfaces "what attrs is my
// SDK actually emitting" for this service so the operator
// doesn't have to open a single trace and squint at the
// attribute table. Lives on the Service detail Details tab
// next to the other infra/breakdown panels.
//
// Rows are grouped by scope (span vs resource) because the
// operator-facing meaning is different: resource attrs are
// process-stable (k8s.pod.name, service.namespace,
// service.instance.id) and useful as filter dimensions;
// span attrs are per-request (http.route, db.statement,
// rpc.method) and useful as group-by dimensions.

export function ServiceAttrsPanel({ service, range }: {
  service: string;
  range: TimeRange;
}) {
  const [rows, setRows] = useState<ServiceAttrRow[] | null | undefined>(undefined);
  const [filter, setFilter] = useState('');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    if (!service) return;
    setRows(undefined);
    api.serviceAttrs(service, from, to, { top: 80, samples: 5 })
      .then(r => setRows(r?.attrs ?? []))
      .catch(() => setRows(null));
  }, [service, from, to]);

  if (rows === undefined) {
    return (
      <div style={{
        marginTop: 14, padding: 12, borderRadius: 8,
        background: 'var(--bg1)', border: '1px solid var(--border)',
        fontSize: 12, color: 'var(--text3)',
      }}>
        Loading attributes…
      </div>
    );
  }
  if (rows === null || rows.length === 0) {
    // Self-hide when nothing — empty panel adds visual noise
    // to services that haven't pushed enough spans yet.
    return null;
  }

  const filtered = filter.trim()
    ? rows.filter(r =>
        r.key.toLowerCase().includes(filter.trim().toLowerCase()) ||
        r.sampleValues.some(v => v.toLowerCase().includes(filter.trim().toLowerCase())))
    : rows;
  const spanAttrs = filtered.filter(r => r.scope === 'span');
  const resAttrs  = filtered.filter(r => r.scope === 'resource');

  return (
    <div style={{
      marginTop: 14, padding: 12, borderRadius: 8,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 10, flexWrap: 'wrap' }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>⌥ Attributes emitted</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {rows.length} key{rows.length === 1 ? '' : 's'} sampled
          {filter.trim() ? ` · ${filtered.length} matching` : ''}
        </span>
        <input value={filter} onChange={e => setFilter(e.target.value)}
          placeholder="Filter by key or value…"
          style={{ marginLeft: 'auto', width: 220, padding: '4px 10px', fontSize: 12,
                   background: 'var(--bg)', color: 'var(--text)',
                   border: '1px solid var(--border)', borderRadius: 4 }} />
      </div>
      <AttrSection title="Resource attrs (stable per-process)" rows={resAttrs} />
      <AttrSection title="Span attrs (per-request)" rows={spanAttrs} />
      <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
        Sampled across up to 5k recent spans. Sample values shown
        per key (max 5). Resource attrs make stable filter keys;
        span attrs work better as group-by dimensions.
      </div>
    </div>
  );
}

function AttrSection({ title, rows }: { title: string; rows: ServiceAttrRow[] }) {
  if (rows.length === 0) return null;
  return (
    <div style={{ marginTop: 6 }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 600,
                    textTransform: 'uppercase', letterSpacing: 0.4, marginBottom: 4 }}>
        {title}
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Key</th>
              <th className="num" style={{ width: 100 }}>Occurrences</th>
              <th>Sample values</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(r => (
              <tr key={`${r.scope}:${r.key}`}
                  style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 28px' }}>
                <td className="mono" style={{ fontSize: 12, wordBreak: 'break-all' }}>{r.key}</td>
                <td className="num mono" style={{ color: 'var(--text2)' }}>{fmtNum(r.occurrences)}</td>
                <td style={{ fontSize: 11, color: 'var(--text2)' }}>
                  {r.sampleValues.length === 0 ? '—' : (
                    <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                      {r.sampleValues.map((v, i) => (
                        <code key={i} style={{
                          background: 'var(--bg2)', padding: '1px 6px',
                          borderRadius: 3, fontSize: 11,
                          maxWidth: 280, overflow: 'hidden',
                          textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                          display: 'inline-block',
                        }} title={v}>{v}</code>
                      ))}
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
