import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Empty, Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, fmtBytes } from '@/lib/utils';

// AdminElastic (v0.5.466) — operator-facing inventory of the
// logs backend's indices: name, doc count, size, health, ILM
// lifecycle phase + policy. One screen, one table.
//
// Renders the "not Elasticsearch" empty state when backend
// reports CH or another non-ES log store — instead of pretending
// there's nothing to show. The state confirms what's actually
// wired so the operator doesn't think the page is broken.

interface Row {
  name: string;
  docCount: number;
  sizeBytes: number;
  health: string;
  ilmPolicy: string;
  ilmPhase: string;
}

interface Payload {
  backend: string;
  indices: Row[];
}

const PHASE_COLOUR: Record<string, string> = {
  hot:    'rgba(220,38,38,0.18)',
  warm:   'rgba(234,179,8,0.18)',
  cold:   'rgba(56,189,248,0.18)',
  frozen: 'rgba(165,180,252,0.22)',
  delete: 'rgba(120,113,108,0.22)',
};

const HEALTH_COLOUR: Record<string, string> = {
  green:  'rgba(46,160,67,0.20)',
  yellow: 'rgba(234,179,8,0.18)',
  red:    'rgba(220,38,38,0.22)',
};

export default function AdminElasticPage() {
  const [data, setData] = useState<Payload | undefined>(undefined);
  const [err, setErr] = useState<string | null>(null);
  const [sort, setSort] = useState<keyof Row>('sizeBytes');
  const [dir, setDir] = useState<'asc' | 'desc'>('desc');

  useEffect(() => {
    let cancelled = false;
    setErr(null);
    api.adminElasticIndices()
      .then(d => { if (!cancelled) setData(d ?? { backend: '', indices: [] }); })
      .catch(e => { if (!cancelled) setErr(e instanceof Error ? e.message : String(e)); });
    return () => { cancelled = true; };
  }, []);

  const rows = (data?.indices ?? []).slice().sort((a, b) => {
    const va = a[sort];
    const vb = b[sort];
    const cmp = typeof va === 'number' && typeof vb === 'number'
      ? va - vb
      : String(va).localeCompare(String(vb));
    return dir === 'asc' ? cmp : -cmp;
  });

  const toggleSort = (col: keyof Row) => {
    if (sort === col) setDir(d => d === 'asc' ? 'desc' : 'asc');
    else { setSort(col); setDir(col === 'name' ? 'asc' : 'desc'); }
  };
  const Th = ({ col, label, align }: { col: keyof Row; label: string; align?: 'left' | 'right' }) => (
    <th onClick={() => toggleSort(col)}
      className={`sortable${sort === col ? ' sorted' : ''}`}
      style={{ textAlign: align ?? 'left', cursor: 'pointer' }}>
      {label}{sort === col && <span className="sort-arrow">{dir === 'asc' ? '↑' : '↓'}</span>}
    </th>
  );

  return (
    <>
      <Topbar title="Admin · Elasticsearch indices" />
      <div id="content">
        {err && (
          <div className="empty" style={{ padding: 24, color: 'var(--err)' }}>
            <div style={{ marginBottom: 6, fontWeight: 600 }}>Failed to fetch index inventory</div>
            <div style={{ fontSize: 12 }}>{err}</div>
          </div>
        )}

        {!err && data === undefined && <Spinner />}

        {!err && data && data.backend !== 'elasticsearch' && (
          <Empty icon="≡" title={`Logs backend is "${data.backend || 'unknown'}", not Elasticsearch`}>
            <div style={{ marginTop: 8, color: 'var(--text2)' }}>
              This page shows ES index inventory + ILM lifecycle.
              Switch the logs backend to Elasticsearch
              (<code>COREMETRY_LOGS_BACKEND=elasticsearch</code>) to populate.
            </div>
          </Empty>
        )}

        {!err && data && data.backend === 'elasticsearch' && rows.length === 0 && (
          <Empty icon="≡" title="No indices match the configured pattern" />
        )}

        {!err && data && data.backend === 'elasticsearch' && rows.length > 0 && (
          <>
            <div style={{ marginBottom: 8, fontSize: 12, color: 'var(--text2)' }}>
              {rows.length} {rows.length === 1 ? 'index' : 'indices'} ·{' '}
              {fmtNum(rows.reduce((s, r) => s + r.docCount, 0))} docs ·{' '}
              {fmtBytes(rows.reduce((s, r) => s + r.sizeBytes, 0))}
            </div>
            <table>
              <thead>
                <tr>
                  <Th col="name" label="Index" />
                  <Th col="health" label="Health" />
                  <Th col="docCount" label="Docs" align="right" />
                  <Th col="sizeBytes" label="Size" align="right" />
                  <Th col="ilmPhase" label="ILM phase" />
                  <Th col="ilmPolicy" label="Policy" />
                </tr>
              </thead>
              <tbody>
                {rows.map(r => (
                  <tr key={r.name}>
                    <td className="mono">{r.name}</td>
                    <td>
                      <span className="badge" style={{
                        background: HEALTH_COLOUR[r.health] ?? 'var(--bg3)',
                        textTransform: 'lowercase',
                      }}>{r.health || '—'}</span>
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(r.docCount)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtBytes(r.sizeBytes)}</td>
                    <td>
                      {r.ilmPhase ? (
                        <span className="badge" style={{
                          background: PHASE_COLOUR[r.ilmPhase] ?? 'var(--bg3)',
                          textTransform: 'lowercase',
                        }}>{r.ilmPhase}</span>
                      ) : (
                        <span style={{ color: 'var(--text3)' }}>—</span>
                      )}
                    </td>
                    <td style={{ color: 'var(--text2)', fontSize: 12 }}>{r.ilmPolicy || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </div>
    </>
  );
}
