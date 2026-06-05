import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { TempoAuthType } from '@/lib/types';

// TempoTab — external Grafana Tempo backend (v0.5.208). When
// configured, /api/traces/{id} falls back to Tempo on a CH miss,
// so operators running Coremetry at low sampling + Tempo at 100%
// retention can still resolve long-tail trace IDs in the same
// /trace URL the rest of the UI links to. Admin-only — the saved
// token reads every trace in the operator's Tempo cluster.
export function TempoTab() {
  const [loaded, setLoaded] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [baseUrl, setBaseUrl] = useState('');
  const [authType, setAuthType] = useState<TempoAuthType>('none');
  const [username, setUsername] = useState('');
  const [orgId, setOrgId] = useState('');
  const [token, setToken] = useState('');
  const [hasToken, setHasToken] = useState(false);
  const [insecureSkipVerify, setInsecureSkipVerify] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getTempoSettings().then(s => {
      setEnabled(s.enabled);
      setBaseUrl(s.baseUrl || '');
      setAuthType((s.authType || 'none') as TempoAuthType);
      setUsername(s.username || '');
      setOrgId(s.orgId || '');
      setHasToken(s.hasToken);
      setInsecureSkipVerify(!!s.insecureSkipVerify);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putTempoSettings({
        enabled, baseUrl, authType,
        token, // empty preserved on the server side
        username, orgId, insecureSkipVerify,
      });
      setHasToken(next.hasToken);
      setToken('');
      setMsg({ kind: 'ok',
        text: next.enabled
          ? 'Saved — Tempo fallback live for trace-by-id lookups.'
          : 'Saved — Tempo disabled.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const clearToken = async () => {
    if (!confirm('Remove the saved Tempo token? Lookups will fail with 401 until a new one is set.')) return;
    setBusy(true); setMsg(null);
    try {
      // Server contract: empty token = preserve. To explicitly
      // CLEAR we send a sentinel and the server compares cur vs
      // payload. Without a sentinel, the simplest path is to
      // flip authType to "none" — drops the Authorization
      // header even if the token is still stored. That's enough
      // for "stop using my creds".
      const next = await api.putTempoSettings({
        enabled, baseUrl, authType: 'none',
        username, orgId, insecureSkipVerify,
      });
      setAuthType('none');
      setHasToken(next.hasToken);
      setMsg({ kind: 'ok', text: 'Auth disabled — lookups now go anonymously.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Clear failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  const ready = enabled && baseUrl.trim().length > 0;

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>External Tempo backend</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Use case: Coremetry at low sampling (e.g. 5%) for fast hot-path
        observability + an external Grafana Tempo cluster at 100% retention
        for forensics. When a trace ID isn't in Coremetry's store,
        <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3, margin: '0 4px' }}>/trace?id=…</code>
        silently falls back to Tempo. The trace renders in the same
        waterfall with a small banner so it's clear where the data came from.
        Trace-by-id only — search / aggregations / topology still hit Coremetry.
      </p>

      <div className={`status-banner status-banner-${ready ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${ready ? 'operational' : 'degraded'}`}>
          {ready ? 'ENABLED' : 'NOT CONFIGURED'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {ready
            ? `Pointing at ${baseUrl}${orgId ? ` (orgId=${orgId})` : ''}.`
            : 'Disabled — CH misses return empty without trying Tempo.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={enabled}
            onChange={e => setEnabled(e.target.checked)} />
          <span style={{ fontSize: 13 }}>Enable Tempo fallback</span>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Base URL</div>
          <input value={baseUrl}
            onChange={e => setBaseUrl(e.target.value)}
            placeholder="https://tempo.example.com  ·  Grafana Cloud: https://tempo-prod-XX.grafana.net/tempo"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Trailing slash optional. We call <code>{`{baseUrl}/api/traces/{id}`}</code> with
            <code style={{ marginLeft: 4 }}>Accept: application/json</code>.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Auth</div>
          <select value={authType}
            onChange={e => setAuthType(e.target.value as TempoAuthType)}
            style={{ width: '100%' }}>
            <option value="none">None (open Tempo behind VPN / mTLS)</option>
            <option value="bearer">Bearer token (Grafana Cloud API key)</option>
            <option value="basic">Basic auth (self-hosted + nginx htpasswd)</option>
          </select>
        </label>

        {authType === 'basic' && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Username</div>
            <input value={username}
              onChange={e => setUsername(e.target.value)}
              style={{ width: '100%' }} />
          </label>
        )}

        {(authType === 'bearer' || authType === 'basic') && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              {authType === 'bearer' ? 'Bearer token' : 'Password'}
              {hasToken && <span style={{ color: 'var(--ok)', marginLeft: 8 }}>· stored</span>}
            </div>
            <input type="password" value={token}
              onChange={e => setToken(e.target.value)}
              placeholder={hasToken ? '(leave empty to keep stored value)' : 'paste token…'}
              style={{ width: '100%' }} />
          </label>
        )}

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            X-Scope-OrgID (multi-tenant Tempo / Grafana Cloud)
          </div>
          <input value={orgId}
            onChange={e => setOrgId(e.target.value)}
            placeholder="leave empty for single-tenant"
            style={{ width: '100%' }} />
        </label>

        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={insecureSkipVerify}
            onChange={e => setInsecureSkipVerify(e.target.checked)} />
          <span style={{ fontSize: 13 }}>
            Skip TLS verification
            <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
              (self-signed certs / POC only)
            </span>
          </span>
        </label>

        {msg && (
          <div style={{ marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
          {hasToken && authType !== 'none' && (
            <Button type="button" variant="secondary" disabled={busy} onClick={clearToken}>
              Disable auth
            </Button>
          )}
        </div>
      </form>
    </div>
  );
}
