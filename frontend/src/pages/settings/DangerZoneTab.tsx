import { useState } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { humanize, FlashBox } from './shared';
import type { PurgeResult } from '@/lib/types';

// ── Danger zone — Purge telemetry data ──────────────────────────────────────
//
// A "factory reset" of observability DATA: every span / log / metric and the
// aggregation, topology, trace and derived-analysis tables are TRUNCATEd from
// ClickHouse, while ALL configuration is preserved (LDAP/system_settings,
// alert rules, saved views, users, dashboards, monitors, status page,
// notification channels — and the audit_log, which records the purge itself).
// Admin-only (the whole Settings area is admin-gated); the backend re-checks
// the role on the route. Two-step type-to-confirm so it can't fire by accident.
const CONFIRM = 'purge all telemetry';

const DELETED = [
  'Spans, logs, metrics, profiles (raw signals)',
  'RED / topology / trace rollup materialized views',
  'Auto-generated analysis (anomalies, root-cause, AI usage, monitor results)',
];
const KEPT = [
  'LDAP / SSO / every system setting',
  'Alert rules, saved views, dashboards',
  'Users, custom roles, notification channels',
  'Monitors, runbooks, SLOs, status page, maintenance windows',
  'Your problems, incidents, exceptions, events & runbook history',
  'The audit log — it records this purge',
];

export function DangerZoneTab() {
  const [arming, setArming] = useState(false);
  const [phrase, setPhrase] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const onPurge = async () => {
    setBusy(true); setMsg(null);
    try {
      const res: PurgeResult = await api.purgeTelemetry();
      const parts = [`Purged ${res.tablesPurged.length} table(s)`];
      if (res.skipped?.length) parts.push(`${res.skipped.length} skipped (absent on this install)`);
      if (res.errors?.length) parts.push(`${res.errors.length} error(s): ${res.errors.join('; ')}`);
      setMsg({ kind: res.errors?.length ? 'err' : 'ok', text: parts.join(' · ') });
      setArming(false); setPhrase('');
    } catch (e) {
      setMsg({ kind: 'err', text: humanize(e) });
    } finally { setBusy(false); }
  };

  return (
    <div className="card" style={{ padding: 16, maxWidth: 760 }}>
      <h3 style={{ margin: '0 0 12px' }}>⚠ Danger zone — Purge telemetry data</h3>
      <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 16, lineHeight: 1.5 }}>
        A factory reset of observability data: every span, log and metric, plus
        the aggregation / topology / trace views, are emptied from ClickHouse.
        <strong> All configuration is preserved.</strong> This cannot be undone.
      </div>

      <div style={{ display: 'flex', gap: 24, marginBottom: 18, flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: 240 }}>
          <div style={{ fontWeight: 600, marginBottom: 6 }}>Deleted</div>
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12, color: 'var(--text2)', lineHeight: 1.6 }}>
            {DELETED.map(d => <li key={d}>{d}</li>)}
          </ul>
        </div>
        <div style={{ flex: 1, minWidth: 240 }}>
          <div style={{ fontWeight: 600, marginBottom: 6 }}>Kept</div>
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12, color: 'var(--text2)', lineHeight: 1.6 }}>
            {KEPT.map(k => <li key={k}>{k}</li>)}
          </ul>
        </div>
      </div>

      {!arming ? (
        <Button variant="danger" onClick={() => { setArming(true); setMsg(null); }}>
          Purge telemetry data…
        </Button>
      ) : (
        <div style={{ borderTop: '1px solid var(--border)', paddingTop: 14 }}>
          <div style={{ fontSize: 12, marginBottom: 8 }}>
            Type <code>{CONFIRM}</code> to confirm:
          </div>
          <input
            className="field"
            value={phrase}
            onChange={e => setPhrase(e.target.value)}
            placeholder={CONFIRM}
            autoFocus
            style={{ maxWidth: 280, marginBottom: 12, display: 'block' }}
          />
          <div style={{ display: 'flex', gap: 8 }}>
            <Button variant="danger" disabled={phrase.trim() !== CONFIRM || busy} onClick={onPurge}>
              {busy ? 'Purging…' : 'Confirm — delete all telemetry'}
            </Button>
            <Button variant="ghost" disabled={busy} onClick={() => { setArming(false); setPhrase(''); }}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      {msg && <div style={{ marginTop: 14 }}><FlashBox kind={msg.kind}>{msg.text}</FlashBox></div>}
    </div>
  );
}
