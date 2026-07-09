import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { ESLogstoreInput, ESLogstoreSnapshot } from '@/lib/types';

// ElasticTab — UI-managed logs read backend (v0.8.232,
// operator-requested: configure ES from the UI and SEE the error when
// it's wrong). Follows the TempoTab template. Save is apply-first on
// the server: a config that can't connect comes back with the real ES
// error and nothing changes; Test pings a candidate without touching
// the live backend. UI-saved config overrides env/YAML (which stays
// the bootstrap default) and swaps the backend live — no restart.
export function ElasticTab() {
  const [loaded, setLoaded] = useState(false);
  const [snap, setSnap] = useState<ESLogstoreSnapshot | null>(null);

  const [backend, setBackend] = useState<'clickhouse' | 'elasticsearch'>('clickhouse');
  const [addresses, setAddresses] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [insecure, setInsecure] = useState(false);
  const [index, setIndex] = useState('');
  const [indexTemplate, setIndexTemplate] = useState('');
  const [fTimestamp, setFTimestamp] = useState('');
  const [fTraceId, setFTraceId] = useState('');
  const [fSpanId, setFSpanId] = useState('');
  const [fService, setFService] = useState('');
  const [fBody, setFBody] = useState('');
  const [fSevTx, setFSevTx] = useState('');
  const [fSevNo, setFSevNo] = useState('');
  // v0.8.400 — deployment-environment field for the ?env= filter;
  // empty = the backend self-discovers via field_caps.
  const [fEnv, setFEnv] = useState('');

  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getLogstoreSettings().then(s => {
      setSnap(s);
      setBackend(s.backend);
      setAddresses((s.addresses || []).join(', '));
      setUsername(s.username || '');
      setInsecure(!!s.insecureSkipVerify);
      setIndex(s.index || '');
      setIndexTemplate(s.indexTemplate || '');
      setFTimestamp(s.fields?.timestamp || '');
      setFTraceId(s.fields?.traceId || '');
      setFSpanId(s.fields?.spanId || '');
      setFService(s.fields?.service || '');
      setFBody(s.fields?.body || '');
      setFSevTx(s.fields?.severityTx || '');
      setFSevNo(s.fields?.severityNo || '');
      setFEnv(s.fields?.env || '');
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const buildInput = (): ESLogstoreInput => ({
    backend,
    addresses: addresses.split(',').map(a => a.trim()).filter(Boolean),
    username, password, apiKey,
    insecureSkipVerify: insecure,
    index, indexTemplate,
    fields: {
      timestamp: fTimestamp, traceId: fTraceId, spanId: fSpanId,
      service: fService, body: fBody, severityTx: fSevTx, severityNo: fSevNo,
      env: fEnv,
    },
  });

  const test = async () => {
    setBusy(true); setMsg(null);
    try {
      const r = await api.testLogstoreSettings(buildInput());
      setMsg(r.ok
        ? { kind: 'ok', text: backend === 'elasticsearch' ? 'Connected — cluster answered the ping.' : 'ClickHouse backend reachable.' }
        : { kind: 'err', text: r.error || 'Connection failed' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Test failed' });
    } finally {
      setBusy(false);
    }
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putLogstoreSettings(buildInput());
      setSnap(next);
      setPassword(''); setApiKey('');
      setMsg({ kind: 'ok',
        text: next.backend === 'elasticsearch'
          ? 'Saved — logs now read from Elasticsearch (applied live, all pods converge <30s).'
          : 'Saved — logs now read from ClickHouse.' });
    } catch (err) {
      // The server rejects an unconnectable config with the REAL ES
      // error (apply-first) — surface it verbatim.
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  const es = backend === 'elasticsearch';
  const fieldRow = (label: string, val: string, set: (v: string) => void, ph: string) => (
    <label style={{ display: 'block' }}>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      <input value={val} onChange={e => set(e.target.value)} placeholder={ph} style={{ width: '100%' }} />
    </label>
  );

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Logs read backend</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Ingest always writes to ClickHouse; this selects where /logs
        <em> reads</em> from. Point it at an external Elasticsearch cluster to
        query the indices your existing pipeline ships to. Saved config
        overrides <code>COREMETRY_ES_*</code> env (env stays the bootstrap
        default) and applies live — no restart. Failed queries surface on
        Admin → Elasticsearch → Recent query errors.
      </p>

      {snap && (
        <div className={`status-banner status-banner-${snap.backend === 'elasticsearch' ? 'operational' : 'degraded'}`}>
          <span className={`status-pill status-pill-${snap.backend === 'elasticsearch' ? 'operational' : 'degraded'}`}>
            {snap.backend.toUpperCase()}
          </span>
          <span style={{ fontWeight: 600, fontSize: 14 }}>
            {snap.backend === 'elasticsearch'
              ? `Reading ${snap.index || 'app-*'} on ${snap.addresses.join(', ') || '—'}`
              : 'Reading the built-in ClickHouse logs table.'}
          </span>
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
            source: {snap.source === 'ui' ? 'UI override' : 'env / YAML'}
          </span>
        </div>
      )}

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        display: 'grid', gap: 12,
      }}>
        <label style={{ display: 'block' }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Backend</div>
          <select value={backend}
            onChange={e => setBackend(e.target.value as 'clickhouse' | 'elasticsearch')}
            style={{ width: '100%' }}>
            <option value="clickhouse">ClickHouse (built-in logs table)</option>
            <option value="elasticsearch">Elasticsearch (external cluster, read-only)</option>
          </select>
        </label>

        {es && (
          <>
            <label style={{ display: 'block' }}>
              <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Addresses</div>
              <input value={addresses} onChange={e => setAddresses(e.target.value)}
                placeholder="https://es-0:9200, https://es-1:9200" style={{ width: '100%' }} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>Comma-separated.</div>
            </label>

            <label style={{ display: 'block' }}>
              <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                API key
                {snap?.hasApiKey && <span style={{ color: 'var(--ok)', marginLeft: 8 }}>· stored</span>}
              </div>
              <input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)}
                placeholder={snap?.hasApiKey ? '(leave empty to keep stored value)' : 'base64 id:api_key — takes precedence over basic auth'}
                style={{ width: '100%' }} />
            </label>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
              <label style={{ display: 'block' }}>
                <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Username (basic auth)</div>
                <input value={username} onChange={e => setUsername(e.target.value)}
                  placeholder="unused when an API key is set" style={{ width: '100%' }} />
              </label>
              <label style={{ display: 'block' }}>
                <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                  Password
                  {snap?.hasPassword && <span style={{ color: 'var(--ok)', marginLeft: 8 }}>· stored</span>}
                </div>
                <input type="password" value={password} onChange={e => setPassword(e.target.value)}
                  placeholder={snap?.hasPassword ? '(keep stored)' : ''} style={{ width: '100%' }} />
              </label>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
              <label style={{ display: 'block' }}>
                <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Index pattern</div>
                <input value={index} onChange={e => setIndex(e.target.value)}
                  placeholder="app-*" style={{ width: '100%' }} />
              </label>
              <label style={{ display: 'block' }}>
                <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Index template (per-service)</div>
                <input value={indexTemplate} onChange={e => setIndexTemplate(e.target.value)}
                  placeholder="app-{service}.{namespace}" style={{ width: '100%' }} />
                <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                  Service-filtered queries hit the resolved index instead of the
                  pattern. <code>{'{namespace}'}</code> comes from span resource
                  attributes; unresolved → <code>*</code>.
                </div>
              </label>
            </div>

            <details>
              <summary style={{ fontSize: 12, color: 'var(--text2)', cursor: 'pointer' }}>
                Document field map (leave empty for OTel/ECS defaults)
              </summary>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginTop: 10 }}>
                {fieldRow('Timestamp', fTimestamp, setFTimestamp, '@timestamp')}
                {fieldRow('Message body', fBody, setFBody, 'message')}
                {fieldRow('Trace ID', fTraceId, setFTraceId, 'trace.id')}
                {fieldRow('Span ID', fSpanId, setFSpanId, 'span.id')}
                {fieldRow('Service', fService, setFService, 'service.name')}
                {fieldRow('Severity (text)', fSevTx, setFSevTx, 'log.level')}
                {fieldRow('Severity (number)', fSevNo, setFSevNo, 'empty = skip')}
                {fieldRow('Environment', fEnv, setFEnv, 'empty = self-discover')}
              </div>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
                Environment backs the global env picker on /logs. Left empty,
                Coremetry discovers the field from the index mapping
                (deployment.environment[.name] and friends) and /logs shows an
                honest chip when none resolves.
              </div>
            </details>

            <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <input type="checkbox" checked={insecure} onChange={e => setInsecure(e.target.checked)} />
              <span style={{ fontSize: 13 }}>
                Skip TLS verification
                <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
                  (self-signed certs / POC only)
                </span>
              </span>
            </label>
          </>
        )}

        {msg && (
          <div style={{ fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-word',
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <Button type="button" variant="secondary" disabled={busy} onClick={test}>
            {busy ? 'Working…' : 'Test connection'}
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Working…' : 'Save & apply'}
          </Button>
        </div>
      </form>
    </div>
  );
}
