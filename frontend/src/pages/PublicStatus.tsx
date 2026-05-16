import { useEffect, useState, FormEvent } from 'react';
import { TelescopeIcon } from '@/components/TelescopeIcon';
import { ThemeToggle } from '@/components/ThemeToggle';

// /public-status — customer-facing status page. Standalone layout
// (no sidebar, no auth, no Coremetry chrome). Polls /api/public-status
// every 30s. Subscribers post their email to get notified when
// incidents are published.

interface ComponentRow {
  id: string;
  name: string;
  description?: string;
  status: 'operational' | 'degraded' | 'outage' | 'unknown';
  message?: string;
  uptimeDays?: number[];
}

interface IncidentRow {
  id: string;
  title: string;
  body?: string;
  status: string;
  severity: string;
  startedAt: number;
  resolvedAt?: number;
}

interface StatusResp {
  title: string;
  description?: string;
  supportUrl?: string;
  status: 'operational' | 'degraded' | 'outage';
  checkedAt: string;
  components: ComponentRow[];
  incidents: IncidentRow[];
}

export default function PublicStatusPage() {
  const [data, setData] = useState<StatusResp | null | undefined>(undefined);

  useEffect(() => {
    const load = () => fetch('/api/public-status', { credentials: 'omit' })
      .then(r => r.ok ? r.json() : Promise.reject())
      .then(setData)
      .catch(() => setData(null));
    load();
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  }, []);

  if (data === undefined) return <div style={{ padding: 60, textAlign: 'center', color: 'var(--text3)' }}>Loading…</div>;
  if (data === null)      return <div style={{ padding: 60, textAlign: 'center', color: 'var(--err)' }}>Could not load status</div>;

  return (
    <div style={{
      minHeight: '100vh', background: 'var(--bg)',
      display: 'flex', flexDirection: 'column',
    }}>
      {/* Top bar — branded header (no sidebar) */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 12,
        padding: '14px 28px', borderBottom: '1px solid var(--border)',
        background: 'var(--bg1)',
      }}>
        <TelescopeIcon size={28} />
        {/* Title only renders when the operator set a non-default
            value. The seeded "Service Status" placeholder + the
            "Acme Status" sample literal stay invisible — the OTel
            mark + URL context already identify the page. */}
        {data.title && data.title !== 'Service Status' && data.title !== 'Acme Status' && (
          <div style={{ fontSize: 16, fontWeight: 700, color: 'var(--text)' }}>{data.title}</div>
        )}
        <span style={{ marginLeft: 'auto' }} />
        <ThemeToggle />
      </div>

      <div style={{ maxWidth: 880, margin: '0 auto', padding: '32px 24px', width: '100%', flex: 1 }}>
        {/* Banner */}
        <div className={`status-banner status-banner-${data.status}`}>
          <span className={`status-pill status-pill-${data.status}`}>{labelOf(data.status)}</span>
          <span style={{ fontWeight: 700, fontSize: 18 }}>{headlineOf(data.status)}</span>
        </div>
        {data.description && (
          <p style={{ color: 'var(--text2)', fontSize: 13, marginTop: 12 }}>{data.description}</p>
        )}
        <p style={{ color: 'var(--text3)', fontSize: 11, marginTop: 8 }}>
          Last updated {new Date(data.checkedAt).toLocaleString()} · refreshes every 30s
        </p>

        {/* Components — name + 90-day uptime bars */}
        {data.components.length > 0 && (
          <div style={{ marginTop: 28 }}>
            <div style={{ fontWeight: 700, fontSize: 13, color: 'var(--text)', marginBottom: 12 }}>Components</div>
            <div className="status-grid">
              {data.components.map(c => <ComponentCard key={c.id} c={c} />)}
            </div>
          </div>
        )}

        {/* Active + recent incidents */}
        {data.incidents.length > 0 && (
          <div style={{ marginTop: 32 }}>
            <div style={{ fontWeight: 700, fontSize: 13, color: 'var(--text)', marginBottom: 12 }}>Recent incidents</div>
            {data.incidents.map(i => <IncidentCard key={i.id} i={i} />)}
          </div>
        )}

        {/* Subscribe form */}
        <SubscribeForm />

        <div style={{ marginTop: 40, color: 'var(--text3)', fontSize: 11, textAlign: 'center' }}>
          Powered by Coremetry
        </div>
      </div>
    </div>
  );
}

function labelOf(s: 'operational' | 'degraded' | 'outage'): string {
  return s === 'operational' ? 'OPERATIONAL' : s === 'degraded' ? 'DEGRADED' : 'OUTAGE';
}
function headlineOf(s: 'operational' | 'degraded' | 'outage'): string {
  return s === 'operational' ? 'All systems operational'
       : s === 'degraded'    ? 'Some systems are experiencing issues'
       :                       'Major outage in progress';
}

function ComponentCard({ c }: { c: ComponentRow }) {
  const cls = c.status === 'operational' ? 'operational'
            : c.status === 'degraded'    ? 'degraded'
            : c.status === 'outage'      ? 'outage'
            :                              'degraded';
  return (
    <div className={`status-row status-row-${cls}`}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span className={`status-dot status-dot-${cls}`} />
          <span style={{ fontWeight: 600 }}>{c.name}</span>
          {c.message && (
            <span style={{ color: 'var(--text3)', fontSize: 12 }}>· {c.message}</span>
          )}
        </div>
        {c.description && (
          <div style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 18 }}>{c.description}</div>
        )}
        {c.uptimeDays && c.uptimeDays.length > 0 && (
          <div style={{ marginLeft: 18 }}>
            <UptimeBar days={c.uptimeDays} />
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 10, color: 'var(--text3)', marginTop: 4 }}>
              <span>90 days ago</span>
              <span>{uptimePct(c.uptimeDays)}% uptime</span>
              <span>today</span>
            </div>
          </div>
        )}
      </div>
      <span className={`status-pill status-pill-${cls}`}>{c.status === 'unknown' ? 'PENDING' : c.status.toUpperCase()}</span>
    </div>
  );
}

function UptimeBar({ days }: { days: number[] }) {
  return (
    <div style={{ display: 'flex', gap: 1, alignItems: 'center', maxWidth: '100%' }}>
      {days.map((r, i) => {
        let bg = 'var(--ok)';
        let title = 'Operational';
        if (r < 0) {
          bg = 'var(--bg3)';
          title = 'No data';
        } else if (r < 0.95) {
          bg = 'var(--err)';
          title = `${(r * 100).toFixed(1)}% — major issues`;
        } else if (r < 0.99) {
          bg = 'var(--warn)';
          title = `${(r * 100).toFixed(1)}% — minor issues`;
        } else {
          title = `${(r * 100).toFixed(1)}% uptime`;
        }
        return <span key={i} title={title}
          style={{ flex: 1, height: 24, background: bg, borderRadius: 1, opacity: 0.85 }} />;
      })}
    </div>
  );
}

function uptimePct(days: number[]): string {
  const valid = days.filter(d => d >= 0);
  if (valid.length === 0) return '—';
  const avg = valid.reduce((a, b) => a + b, 0) / valid.length;
  return (avg * 100).toFixed(2);
}

function IncidentCard({ i }: { i: IncidentRow }) {
  const sevCls = i.severity === 'critical' ? 'b-err' : i.severity === 'warning' ? 'b-warn' : 'b-info';
  return (
    <div style={{
      padding: 14, borderRadius: 6, marginBottom: 10,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
        <span className={`badge ${sevCls}`}>{i.severity.toUpperCase()}</span>
        <span style={{ fontWeight: 600 }}>{i.title}</span>
        <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
          {i.status === 'resolved' ? 'Resolved' : i.status === 'acknowledged' ? 'Investigating' : 'Open'}
        </span>
      </div>
      {i.body && (
        <div style={{ color: 'var(--text2)', fontSize: 13, lineHeight: 1.5, whiteSpace: 'pre-wrap' }}>{i.body}</div>
      )}
      <div style={{ color: 'var(--text3)', fontSize: 11, marginTop: 6 }}>
        Started {new Date(i.startedAt / 1e6).toLocaleString()}
        {i.resolvedAt && ` · Resolved ${new Date(i.resolvedAt / 1e6).toLocaleString()}`}
      </div>
    </div>
  );
}

function SubscribeForm() {
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const r = await fetch('/api/public-status/subscribe', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email }),
      });
      if (!r.ok) throw new Error(await r.text());
      // v0.5.158 — server returns {status, message} with the
      // double-opt-in copy. Fall back to a generic message if a
      // pre-v0.5.158 deploy is still answering.
      const body = await r.json().catch(() => ({} as { message?: string }));
      setMsg({
        kind: 'ok',
        text: body.message
          || 'Check your inbox — the subscription is inactive until you confirm.',
      });
      setEmail('');
    } catch (err: unknown) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Subscribe failed' });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div style={{
      marginTop: 32, padding: 16, borderRadius: 8,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontWeight: 600, marginBottom: 8 }}>Get notified</div>
      <p style={{ color: 'var(--text3)', fontSize: 12, marginBottom: 12 }}>
        Subscribe to receive an email whenever an incident is opened or resolved.
      </p>
      <form onSubmit={submit} style={{ display: 'flex', gap: 8 }}>
        <input required type="email" value={email} onChange={e => setEmail(e.target.value)}
          placeholder="you@example.com" style={{ flex: 1 }} />
        <button type="submit" disabled={busy}>{busy ? 'Subscribing…' : 'Subscribe'}</button>
      </form>
      {msg && (
        <div style={{
          marginTop: 8, fontSize: 12,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
        }}>{msg.text}</div>
      )}
    </div>
  );
}
