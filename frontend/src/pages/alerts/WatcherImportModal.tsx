import { useState } from 'react';
import { api } from '@/lib/api';
import { Modal, Field, TextareaField, Button } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import type { WatcherImportReport, WatcherSupport } from '@/lib/types';

// WatcherImportModal — ES Watcher Faz-1 import flow. The operator
// pastes the exact PUT _watcher/watch body, Validate runs the
// server-side dry-run (Parse → Validate → ToRule, nothing persists)
// and renders the field-by-field mapping report + rule projection
// preview; Import persists the rule (Metric='watcher', raw JSON
// stored verbatim) and KEEPS the modal open showing the import
// report — a directly-imported watch may land DISABLED with a reason
// the operator must see (review F6). Name collisions answer 409 —
// Faz-1 has no overwrite, the operator renames.
//
// The textarea content is sent as the LITERAL string (watchText,
// review F5): JSON.parse here is only a validity gate — a
// parse→stringify round-trip would silently round >2^53 integers and
// reformat the operator's definition before it ever reached the
// server's verbatim storage.
//
// Modal state is deliberately ephemeral (no URL params): this is a
// transient paste-and-go flow, not a shareable drawer view.

const badgeClass: Record<WatcherSupport, string> = {
  supported:   'b-ok',
  partial:     'b-warn',
  unsupported: 'b-err',
};

// Server errors arrive as `HTTP 409: {"error":"…"}` — surface just
// the message (same unwrap as ChangePasswordModal).
function errMessage(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  const body = msg.replace(/^HTTP \d+:\s*/, '');
  try {
    const j = JSON.parse(body) as { error?: string };
    return j?.error ?? body;
  } catch { return body; }
}

// tripleQuotesToJson mirrors the server's watcher.NormalizeDevTools:
// Kibana DevTools """triple-quoted""" strings (not valid JSON) become
// escaped JSON strings so the client-side validity gate tolerates a
// DevTools paste. Validation-only — the RAW text is what gets sent.
function tripleQuotesToJson(s: string): string {
  if (!s.includes('"""')) return s;
  let out = '';
  let inStr = false;
  let i = 0;
  while (i < s.length) {
    const c = s[i];
    if (inStr) {
      out += c;
      if (c === '\\' && i + 1 < s.length) { out += s[i + 1]; i += 2; continue; }
      if (c === '"') inStr = false;
      i++;
      continue;
    }
    if (c === '"' && s.startsWith('"""', i)) {
      const end = s.indexOf('"""', i + 3);
      if (end < 0) { out += s.slice(i); return out; }
      out += JSON.stringify(s.slice(i + 3, end));
      i = end + 3;
      continue;
    }
    if (c === '"') inStr = true;
    out += c;
    i++;
  }
  return out;
}

export function WatcherImportModal({ onClose, onImported }: {
  onClose: () => void;
  onImported: () => void;
}) {
  const [name, setName] = useState('');
  const [raw, setRaw] = useState('');
  const [report, setReport] = useState<WatcherImportReport | null>(null);
  const [imported, setImported] = useState(false);
  const [busy, setBusy] = useState<'validate' | 'import' | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Client-side JSON gate so a stray comma fails instantly with a
  // pointer instead of a server round-trip. Tolerates DevTools
  // triple-quoted strings (server normalizes them the same way); the
  // raw text is transmitted either way.
  const watchIsValid = (): boolean => {
    try { JSON.parse(raw); return true; }
    catch (e) {
      try { JSON.parse(tripleQuotesToJson(raw)); return true; }
      catch {
        setError('Watch is not valid JSON: ' + (e instanceof Error ? e.message : String(e)));
        return false;
      }
    }
  };

  const run = async (dryRun: boolean) => {
    if (!watchIsValid()) return;
    setBusy(dryRun ? 'validate' : 'import');
    setError(null);
    try {
      const res = await api.importWatcher({ name: name.trim(), watchText: raw, dryRun });
      setReport(res.report);
      if (!dryRun) {
        // Keep the modal open: the report (esp. a disabled-with-reason
        // verdict) must survive the import click (review F6).
        setImported(true);
        onImported();
      }
    } catch (e) {
      setError(errMessage(e));
    } finally {
      setBusy(null);
    }
  };

  const empty = raw.trim() === '';

  return (
    <Modal open onClose={onClose} title="Import ES watcher" size="lg"
      initialFocus="input"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{imported ? 'Close' : 'Cancel'}</Button>
          <Button variant="secondary" onClick={() => run(true)}
            loading={busy === 'validate'} disabled={empty || busy !== null || imported}
            title="Dry-run: parse the watch and show the field-by-field mapping report without saving anything">
            Validate
          </Button>
          <Button variant="primary" onClick={() => run(false)}
            loading={busy === 'import'} disabled={empty || busy !== null || imported}
            title="Create the alert rule from this watch (the raw definition is stored verbatim)">
            Import
          </Button>
        </>
      }>
      <Field label="Rule name" value={name} disabled={imported}
        onChange={e => { setName(e.target.value); setReport(null); }}
        placeholder="e.g. prod error spike"
        hint="Leave empty to use the watch's metadata.name. An existing rule with the same name blocks the import." />
      <div style={{ marginTop: 10 }}>
        <TextareaField label="Watch JSON (PUT _watcher/watch body)" rows={12}
          value={raw} disabled={imported}
          onChange={e => { setRaw(e.target.value); setReport(null); }}
          spellCheck={false}
          style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, width: '100%' }}
          placeholder={'{\n  "trigger": { "schedule": { "interval": "10m" } },\n  "input": { "search": { "request": { "indices": ["app-*"], "body": { … } } } },\n  "condition": { "compare": { "ctx.payload.hits.total": { "gte": 100 } } }\n}'} />
      </div>

      {busy === 'validate' && !report && <Spinner />}
      {error && (
        <div style={{ marginTop: 10, color: 'var(--err)', fontSize: 12 }}>
          {error}
        </div>
      )}

      {report && (
        <div style={{ marginTop: 12 }}>
          {imported && (
            <div style={{
              padding: '8px 10px', marginBottom: 10, borderRadius: 6,
              border: '1px solid var(--ok)', background: 'var(--bg2)',
              fontSize: 12,
            }}>
              <b>Imported ✓</b> — alert rule created{report.enabled
                ? '; it evaluates on the next tick.'
                : ' (disabled — see below).'}
            </div>
          )}
          {!report.enabled && (
            <div style={{
              padding: '8px 10px', marginBottom: 10, borderRadius: 6,
              border: '1px solid var(--err)', background: 'var(--bg2)',
              fontSize: 12,
            }}>
              <b>{imported ? 'Rule imported DISABLED' : 'Rule will import DISABLED'}</b>
              {report.disabledReason && <> — {report.disabledReason}</>}.
              The definition is kept; nothing fires until you fix and re-enable it.
            </div>
          )}
          <div style={{
            fontSize: 12, marginBottom: 10, padding: '8px 10px',
            borderRadius: 6, background: 'var(--bg2)',
            border: '1px solid var(--border)',
          }}>
            Rule preview: <b>{report.rule.name}</b>
            <span className="mono" style={{ marginLeft: 8 }}>
              hits.total {report.rule.comparator} {report.rule.threshold}
            </span>
            <span style={{ color: 'var(--text2)', marginLeft: 8 }}>
              window {Math.round(report.rule.windowSec / 60)}m
              {report.rule.cooldownSec > 0 && <> · cooldown {report.rule.cooldownSec}s</>}
            </span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {report.findings.map((f, i) => (
              <div key={`${f.field}-${i}`}
                style={{ display: 'flex', alignItems: 'baseline', gap: 8, fontSize: 12 }}>
                <span className={`badge ${badgeClass[f.status]}`}
                  style={{ flexShrink: 0 }}>
                  {f.status.toUpperCase()}
                </span>
                <code style={{ flexShrink: 0 }}>{f.field}</code>
                <span style={{ color: 'var(--text2)' }}>{f.reason}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </Modal>
  );
}
