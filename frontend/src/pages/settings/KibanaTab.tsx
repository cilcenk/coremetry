import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { KibanaSettings } from '@/lib/types';
import { SettingRow } from './shared';

// KibanaTab — external Kibana deep-link config (v0.5.236).
// Operator pastes the base URL of their Kibana install; the
// Logs page then renders an "Open in Kibana Discover" link
// per row + a global one in the topbar. Empty / disabled =
// no link rendered.
//
// OpenShift's "Discover in Kibana" pattern: pass a KQL clause
// + time bounds via the _g / _a state params so the Kibana
// landing surface matches the row's context.
export function KibanaTab() {
  const [loaded, setLoaded] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [baseUrl, setBaseUrl] = useState('');
  const [dataView, setDataView] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getKibanaSettings().then(s => {
      setEnabled(!!s.enabled);
      setBaseUrl(s.baseUrl || '');
      setDataView(s.dataView || '');
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next: KibanaSettings = { enabled, baseUrl, dataView: dataView || undefined };
      const r = await api.putKibanaSettings(next);
      setMsg({ kind: 'ok', text: r.enabled
        ? 'Saved — Kibana link is live on /logs.'
        : 'Saved — Kibana link disabled.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  const ready = enabled && baseUrl.trim() !== '';

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Kibana deep-link</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Operators who use Kibana alongside Coremetry can jump out
        to Kibana Discover with the current Logs filter pre-applied
        — same pattern as OpenShift's "Discover in Kibana" affordance.
        Coremetry never proxies Kibana; only mints the deep-link.
      </p>

      <div className={`status-banner status-banner-${ready ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${ready ? 'operational' : 'degraded'}`}>
          {ready ? 'ENABLED' : 'NOT CONFIGURED'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {ready
            ? `Logs page will render a Kibana link pointing at ${baseUrl}.`
            : 'Disabled — no Kibana link rendered on the Logs page.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={enabled}
            onChange={e => setEnabled(e.target.checked)} />
          <span style={{ fontSize: 13 }}>Show "Open in Kibana" link on Logs</span>
        </label>

        <SettingRow
          label="Kibana base URL"
          hint={<>
            Just the host (or host + path prefix if Kibana lives under one,
            e.g. <code>https://openshift-console.example.com/monitoring/kibana</code>).
            Coremetry appends <code>/app/discover#/?…</code>.
          </>}>
          <input value={baseUrl}
            onChange={e => setBaseUrl(e.target.value)}
            placeholder="https://kibana.example.com  (no trailing /app/...)"
            style={{ width: '100%' }} />
        </SettingRow>

        <SettingRow
          label={<>Data view id <span style={{ color: 'var(--text3)' }}>(optional)</span></>}
          hint={<>
            Pins the Discover panel to a specific index pattern. Empty =
            Kibana picks the default, fine for most single-pattern installs.
          </>}>
          <input value={dataView}
            onChange={e => setDataView(e.target.value)}
            placeholder="e.g. logs-*  or  the data-view UUID"
            style={{ width: '100%' }} />
        </SettingRow>

        {msg && (
          <div style={{ marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </form>
    </div>
  );
}
