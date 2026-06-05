import { useState } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { humanize } from './shared';

// ── Backup / Restore tab ────────────────────────────────────────────────────
//
// Operator-set state lives in a small set of ReplacingMergeTree
// tables (system_settings, notification_channels, alert_rules,
// dashboards, saved_views, slos, maintenance_windows, monitors,
// service_contracts, status_page_*). The backend dumps the whole
// catalogue to JSON via /api/admin/config/export and replays via
// /api/admin/config/import.
//
// Two use cases drive the UI shape here:
//   1. **Clean install** — operator runs COREMETRY_CH_RESET_SCHEMA=1,
//      then comes here and uploads the JSON from before the reset.
//   2. **Promotion between environments** — export dev/staging
//      config, import to prod (or vice-versa) to keep alert rules
//      and dashboards in lock-step.
//
// The import has two modes:
//   - **merge** (default) — insert rows verbatim with their stored
//     version columns; ReplacingMergeTree picks the newest
//     (per ORDER BY key). Local edits made AFTER the export win.
//   - **replace** — bump every row's version to now() so imported
//     state always wins. Opt-in, since it can shadow local edits.
type DiffResult = {
  tables: Record<string, {
    willAdd: string[]; willOverwrite: string[];
    unchanged: number; onlyInDB: number;
  }>;
  exportedAt: string;
  coremetryVersion?: string;
};

export function BackupTab() {
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [mode, setMode] = useState<'merge' | 'replace'>('merge');
  const [file, setFile] = useState<File | null>(null);
  // v0.5.398 — diff preview before import. Operator picks a file,
  // clicks "Preview diff" first, sees per-table {willAdd /
  // willOverwrite / unchanged / onlyInDB} counts so they can
  // confirm the import scope is what they expected. Clears on
  // file change so a stale preview never lingers next to a new
  // file selection.
  const [diff, setDiff] = useState<DiffResult | null>(null);

  const onExport = async () => {
    setMsg(null); setBusy(true);
    try {
      await api.exportConfig();
      setMsg({ kind: 'ok', text: 'Export downloaded.' });
    } catch (e) {
      setMsg({ kind: 'err', text: humanize(e) });
    } finally { setBusy(false); }
  };

  const onImport = async () => {
    if (!file) return;
    setMsg(null); setBusy(true);
    try {
      const r = await api.importConfig(file, mode);
      const tables = Object.entries(r.tables).map(([t, n]) => `${t}=${n}`).join(', ');
      const skipped = r.skippedUnknown && r.skippedUnknown.length
        ? ` (skipped unknown: ${r.skippedUnknown.join(', ')})`
        : '';
      setMsg({
        kind: 'ok',
        text: `Imported ${r.rows} rows · mode=${r.mode} · ${tables}${skipped}`,
      });
      setFile(null);
      setDiff(null);
    } catch (e) {
      setMsg({ kind: 'err', text: humanize(e) });
    } finally { setBusy(false); }
  };

  const onPreviewDiff = async () => {
    if (!file) return;
    setMsg(null); setBusy(true);
    try {
      const r = await api.diffConfig(file);
      setDiff(r as DiffResult);
    } catch (e) {
      setMsg({ kind: 'err', text: humanize(e) });
    } finally { setBusy(false); }
  };

  // Reset preview when the operator picks a different file so
  // the displayed diff never lies about the file in scope.
  const onFileChange = (f: File | null) => {
    setFile(f);
    setDiff(null);
    setMsg(null);
  };

  return (
    <div className="card" style={{ padding: 16, maxWidth: 760 }}>
      <h3 style={{ margin: '0 0 12px' }}>Backup / Restore</h3>
      <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 16, lineHeight: 1.5 }}>
        Export every operator-set table (settings, channels, alert
        rules, dashboards, saved views, SLOs, maintenance windows,
        monitors, status page) to a JSON file. Replay later to
        recover from a schema reset, promote config between
        environments, or share dashboards with another install.
        Excludes runtime data (spans, problems, incidents, audit
        log) — those are rebuilt from ingest.
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 16, marginBottom: 20 }}>
        <div style={{ fontWeight: 600, marginBottom: 8 }}>Export</div>
        <Button variant="primary" disabled={busy} onClick={onExport}>
          {busy ? 'Working…' : 'Download config (JSON)'}
        </Button>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
          File contains every row from system_settings, alert_rules,
          dashboards, saved_views, notification_channels, slos,
          maintenance_windows, anomaly_silences, monitors,
          service_metadata, service_contracts, status_page_*.
          Includes secrets in their stored form (AI keys, SMTP
          passwords, LDAP bind passwords) — treat the file like a
          secret.
        </div>
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 16 }}>
        <div style={{ fontWeight: 600, marginBottom: 8 }}>Restore</div>
        <input
          type="file"
          accept="application/json,.json"
          onChange={e => onFileChange(e.target.files?.[0] ?? null)}
          disabled={busy}
          style={{ marginBottom: 12, display: 'block' }}
        />
        <label style={{ display: 'block', marginBottom: 8, fontSize: 12 }}>
          <input
            type="radio"
            name="iox-mode"
            checked={mode === 'merge'}
            onChange={() => setMode('merge')}
            disabled={busy}
            style={{ marginRight: 6 }}
          />
          <strong>Merge</strong> — preserve local edits made after the export.
          {' '}Imported rows only win where no newer local edit exists.
        </label>
        <label style={{ display: 'block', marginBottom: 16, fontSize: 12 }}>
          <input
            type="radio"
            name="iox-mode"
            checked={mode === 'replace'}
            onChange={() => setMode('replace')}
            disabled={busy}
            style={{ marginRight: 6 }}
          />
          <strong>Replace</strong> — imported rows always win, even
          over newer local edits. Use this for clean-install
          restore.
        </label>
        <div style={{ display: 'flex', gap: 8 }}>
          <Button
            disabled={busy || !file}
            onClick={onPreviewDiff}
            title="Dry-run: shows what would be added / overwritten / left alone, no writes.">
            {busy ? 'Working…' : 'Preview diff'}
          </Button>
          <Button
            variant="primary"
            disabled={busy || !file}
            onClick={onImport}
          >
            {busy ? 'Working…' : 'Upload + apply'}
          </Button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
          Live settings hot-reload after the import — no restart
          needed. Unknown tables in the file are skipped (forward-
          compat). Truncate is never performed; import always
          inserts on top of what's there.
        </div>

        {/* v0.5.416 — dry-run diff result panel. Per-table:
            +willAdd  · ~willOverwrite · =unchanged · DB-only.
            Operator confirms scope before triggering import. */}
        {diff && (
          <div style={{
            marginTop: 14, padding: '10px 12px',
            border: '1px solid var(--border)', borderRadius: 6,
            background: 'var(--bg1)',
            fontSize: 12,
          }}>
            <div style={{
              fontSize: 10, fontWeight: 700,
              textTransform: 'uppercase', letterSpacing: 0.4,
              color: 'var(--text2)', marginBottom: 8,
              display: 'flex', justifyContent: 'space-between', alignItems: 'center',
            }}>
              <span>Diff preview</span>
              <span style={{ color: 'var(--text3)', textTransform: 'none', letterSpacing: 0, fontWeight: 400 }}>
                from {diff.exportedAt || '?'}
                {diff.coremetryVersion && ` · ${diff.coremetryVersion}`}
              </span>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {Object.entries(diff.tables).length === 0 && (
                <span style={{ color: 'var(--text3)' }}>No tables in this file matched the catalogue.</span>
              )}
              {Object.entries(diff.tables).map(([t, d]) => (
                <div key={t} style={{
                  display: 'grid',
                  gridTemplateColumns: '180px repeat(4, 1fr)',
                  gap: 8, padding: '3px 0',
                  fontFamily: 'ui-monospace, monospace', fontSize: 11,
                  borderBottom: '1px solid var(--bg2)',
                }}>
                  <span style={{ color: 'var(--text)', fontWeight: 600 }}>{t}</span>
                  <span style={{ color: d.willAdd.length > 0 ? 'var(--ok)' : 'var(--text3)' }}
                    title={d.willAdd.length > 0 ? d.willAdd.slice(0, 20).join('\n') : 'no new rows'}>
                    +{d.willAdd.length} new
                  </span>
                  <span style={{ color: d.willOverwrite.length > 0 ? 'var(--warn)' : 'var(--text3)' }}
                    title={d.willOverwrite.length > 0 ? d.willOverwrite.slice(0, 20).join('\n') : 'no overwrites'}>
                    ~{d.willOverwrite.length} overwrite
                  </span>
                  <span style={{ color: 'var(--text3)' }}>
                    ={d.unchanged} same
                  </span>
                  <span style={{ color: 'var(--text3)' }}
                    title="Rows present in DB but not in the file. Import never deletes — these stay.">
                    {d.onlyInDB} DB-only
                  </span>
                </div>
              ))}
            </div>
            <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 8, lineHeight: 1.4 }}>
              <strong>+new</strong>: rows in file that don't exist locally · <strong>~overwrite</strong>: rows whose version differs (merge mode keeps newer; replace mode forces file) · <strong>=same</strong>: identical · <strong>DB-only</strong>: kept as-is, import never deletes.
            </div>
          </div>
        )}
      </div>

      {msg && (
        <div style={{
          marginTop: 16, fontSize: 12,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
        }}>
          {msg.text}
        </div>
      )}
    </div>
  );
}
