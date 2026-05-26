import { useEffect, useRef, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Empty, Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { toast } from '@/lib/toast';
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

  const fileRef = useRef<HTMLInputElement>(null);
  const onImport = async (file: File) => {
    try {
      const text = await file.text();
      const res = await api.kibanaImportPost(text);
      const summary = `imported ${res.imported}, skipped ${res.skipped}`;
      if (res.errors && res.errors.length > 0) {
        toast.info(`Kibana sync: ${summary} (${res.errors.length} warning${res.errors.length === 1 ? '' : 's'})`);
      } else {
        toast.success(`Kibana sync: ${summary}`);
      }
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      toast.error('Kibana import failed: ' + m);
    }
  };

  return (
    <>
      <Topbar title="Admin · Elasticsearch indices" />
      <div id="content">
        {/* Kibana saved-search interop (v0.5.467) — operator can
            push Coremetry /logs saved_views into Kibana Discover
            as native saved searches, or pull Kibana saved
            searches back as Coremetry views. Mapping is
            faithful but lossy (columns / sort drop; title +
            KQL query round-trip). */}
        <div style={{
          display: 'flex', gap: 10, alignItems: 'center',
          marginBottom: 16, padding: '8px 12px',
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 4, fontSize: 12,
        }}>
          <span style={{ color: 'var(--text2)', marginRight: 4 }}>Kibana saved-search sync:</span>
          <a className="sec"
            href={api.kibanaExportURL()}
            download
            style={{ padding: '4px 12px', textDecoration: 'none', fontSize: 12 }}
            title="Download your /logs saved views as Kibana .ndjson — import in Kibana → Saved Objects.">
            ↓ Export to .ndjson
          </a>
          <button type="button" className="sec"
            onClick={() => fileRef.current?.click()}
            style={{ padding: '4px 12px', fontSize: 12 }}
            title="Upload a Kibana saved-search .ndjson — each `type:search` doc becomes a Coremetry /logs saved view.">
            ↑ Import from .ndjson
          </button>
          <input ref={fileRef} type="file" accept=".ndjson,application/x-ndjson,.json,application/json"
            style={{ display: 'none' }}
            onChange={e => {
              const f = e.target.files?.[0];
              if (f) void onImport(f);
              e.target.value = '';
            }} />
          <span style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--text3)' }}>
            Lossy round-trip: title + KQL query only.
          </span>
        </div>

        {err && (
          <div className="empty" style={{ padding: 24, color: 'var(--err)' }}>
            <div style={{ marginBottom: 6, fontWeight: 600 }}>Failed to fetch index inventory</div>
            <div style={{ fontSize: 12 }}>{err}</div>
          </div>
        )}

        {!err && data === undefined && (
          <Spinner label="Fetching index inventory + ILM lifecycle…" hint="_cat/indices + _ilm/explain, ~1-3s depending on cluster size." />
        )}

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
