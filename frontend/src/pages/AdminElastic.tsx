import { Fragment, useRef, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Empty, Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { toast } from '@/lib/toast';
import { fmtNum, fmtBytes } from '@/lib/utils';
import { useElasticIndices, useElasticErrors } from '@/lib/queries';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

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

// Columns for the shared sortable + resizable DataTable (v0.7.54
// adoption). Preserves the prior default sort (Size desc) and the
// prior natural directions: text cols asc, numeric cols desc.
// Body-cell order below MUST match this order.
const ELASTIC_COLS: DataTableColumn<Row>[] = [
  { id: 'name',      label: 'Index',     sortValue: r => r.name,      naturalDir: 'asc',  width: 320 },
  { id: 'health',    label: 'Health',    sortValue: r => r.health,    naturalDir: 'asc',  width: 100 },
  { id: 'docCount',  label: 'Docs',      sortValue: r => r.docCount,  naturalDir: 'desc', numeric: true, width: 120 },
  { id: 'sizeBytes', label: 'Size',      sortValue: r => r.sizeBytes, naturalDir: 'desc', numeric: true, width: 120 },
  { id: 'ilmPhase',  label: 'ILM phase', sortValue: r => r.ilmPhase,  naturalDir: 'asc',  width: 120 },
  { id: 'ilmPolicy', label: 'Policy',    sortValue: r => r.ilmPolicy, naturalDir: 'asc',  width: 200 },
];

// Recent failed ES queries (v0.8.230, operator-requested). Polls every
// 30s (pauses on document.hidden — React Query's default of not
// refetching in background tabs) so an error the operator just
// triggered on /logs shows up here without a manual refresh.
// Expandable rows reveal the exact query body for curl replay.
// Fetch errors are swallowed: the panel is best-effort; the indices
// error banner covers hard failures.
function QueryErrorsPanel() {
  const diag = useElasticErrors().data;
  const [open, setOpen] = useState<number | null>(null);

  if (!diag) return null;
  const errs = diag.recentErrors ?? [];
  return (
    <div style={{ marginTop: 24 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8 }}>
        <h3 style={{ margin: 0, fontSize: 14 }}>Recent query errors</h3>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {fmtNum(diag.queryErrors)} since boot · last {errs.length} shown · also in pod log as “[logstore-es] query FAILED”
        </span>
      </div>
      {errs.length === 0 ? (
        <Empty icon="✓" title="No failed queries since boot" />
      ) : (
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <colgroup>
            <col style={{ width: 150 }} /><col style={{ width: 170 }} />
            <col style={{ width: 70 }} /><col style={{ width: 260 }} /><col />
          </colgroup>
          <thead>
            <tr><th>Time</th><th>Op</th><th className="num">Status</th><th>Index</th><th>Error</th></tr>
          </thead>
          <tbody>
            {errs.map((e, i) => (
              <Fragment key={i}>
                <tr onClick={() => setOpen(open === i ? null : i)}
                  style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}
                  title="Click to show the exact query body sent">
                  <td className="mono" style={{ fontSize: 11 }}>{new Date(e.at).toLocaleTimeString()}</td>
                  <td>{e.op}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <span className="badge" style={{ background: 'rgba(220,38,38,0.22)' }}>
                      {e.status || 'net'}
                    </span>
                  </td>
                  <td className="mono" style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.index}>{e.index}</td>
                  <td style={{ fontSize: 12, color: 'var(--err)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.error}>{e.error}</td>
                </tr>
                {open === i && (
                  <tr>
                    <td colSpan={5}>
                      <pre style={{
                        margin: '4px 0 8px', padding: 8, fontSize: 11, maxHeight: 240,
                        overflow: 'auto', background: 'var(--bg2)',
                        border: '1px solid var(--border)', borderRadius: 4,
                        whiteSpace: 'pre-wrap', wordBreak: 'break-all',
                      }}>{e.query}</pre>
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

export default function AdminElasticPage() {
  const indicesQ = useElasticIndices();
  const err = indicesQ.error
    ? (indicesQ.error instanceof Error ? indicesQ.error.message : String(indicesQ.error))
    : null;
  const data: Payload | undefined =
    indicesQ.data === undefined ? undefined : indicesQ.data ?? { backend: '', indices: [] };

  const rows = data?.indices ?? [];
  const dt = useDataTable<Row>({
    storageKey: 'admin-elastic',
    columns: ELASTIC_COLS,
    rows,
    initialSort: { id: 'sizeBytes', dir: 'desc' },
  });

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
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(r => (
                  <tr key={r.name} style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
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

        {/* v0.8.230 — failed-query diagnostics. Rendered whenever the
            backend is ES OR the inventory itself errored (the exact
            situation the panel exists for). */}
        {(err || (data && data.backend === 'elasticsearch')) && <QueryErrorsPanel />}
      </div>
    </>
  );
}
