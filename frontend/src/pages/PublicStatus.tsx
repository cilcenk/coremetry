import { useEffect, useState, FormEvent } from 'react';
import { TelescopeIcon } from '@/components/TelescopeIcon';
import { ThemeToggle } from '@/components/ThemeToggle';
import { Spinner, Empty } from '@/components/Spinner';

// /public-status — customer-facing status page. Standalone
// layout (no sidebar, no auth, no Coremetry chrome). Polls
// /api/public-status every 30s. Subscribers post their email
// to get notified when incidents are published.
//
// v0.5.179 — visual overhaul modelled on status.claude.com:
// big hero headline + status pill, narrower content column,
// minimal component rows (no card chrome), incidents grouped
// by recency bucket. Same data contract; only the layout
// changed.

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
    // Skip the poll when the tab is hidden — public status is a
    // background-pinned page for many viewers; without this guard
    // a dozen idle tabs each fire one round-trip every 30s,
    // multiplied by every operator who pinned it.
    const tick = () => { if (!document.hidden) load(); };
    const t = setInterval(tick, 30_000);
    // Refresh immediately on tab focus so a returning operator
    // sees current state, not whatever staleness was paused on.
    const onVis = () => { if (!document.hidden) load(); };
    document.addEventListener('visibilitychange', onVis);
    return () => {
      clearInterval(t);
      document.removeEventListener('visibilitychange', onVis);
    };
  }, []);

  if (data === undefined) {
    return (
      <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', background: 'var(--bg)' }}>
        <Spinner label="Loading status…" />
      </div>
    );
  }
  if (data === null) {
    return (
      <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', background: 'var(--bg)' }}>
        <Empty icon="⚠" title="Could not load status">
          The status feed didn't respond. It refreshes automatically — this page will recover on its own once the service is reachable.
        </Empty>
      </div>
    );
  }

  return (
    <div style={{
      minHeight: '100vh', background: 'var(--bg)',
      display: 'flex', flexDirection: 'column',
    }}>
      {/* Slim top bar — wordmark on the left, theme toggle on
          the right. No chrome, no buttons, no navigation. The
          page IS the status. */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 10,
        padding: '18px 32px',
        borderBottom: '1px solid var(--border)',
      }}>
        <TelescopeIcon size={24} />
        <div style={{ fontSize: 14, fontWeight: 700, color: 'var(--text)' }}>
          {data.title && data.title !== 'Service Status' && data.title !== 'Acme Status'
            ? data.title
            : 'Status'}
        </div>
        <span style={{ marginLeft: 'auto' }} />
        <ThemeToggle />
      </div>

      <div style={{
        maxWidth: 720, margin: '0 auto',
        padding: '56px 24px 64px',
        width: '100%', flex: 1,
      }}>
        {/* Hero — large headline + small pill underneath.
            status.claude.com style is "answer the question on
            arrival" — the visitor's first read should be the
            three-word verdict. */}
        <Hero status={data.status} description={data.description} checkedAt={data.checkedAt} />

        {/* Components — flat list, no card chrome. Status
            pill on the right, optional 90-day uptime bar
            underneath. */}
        {data.components.length > 0 && (
          <section style={{ marginTop: 56 }}>
            <SectionHeading>Components</SectionHeading>
            <div style={{
              border: '1px solid var(--border)', borderRadius: 8,
              overflow: 'hidden',
              background: 'var(--bg1)',
            }}>
              {data.components.map((c, i) => (
                <ComponentLine key={c.id} c={c} first={i === 0} />
              ))}
            </div>
          </section>
        )}

        {/* Past incidents — grouped by recency bucket so the
            "what happened today" question lands first. */}
        {data.incidents.length > 0 ? (
          <section style={{ marginTop: 56 }}>
            <SectionHeading>Past incidents</SectionHeading>
            <IncidentList incidents={data.incidents} />
          </section>
        ) : (
          <section style={{ marginTop: 56 }}>
            <SectionHeading>Past incidents</SectionHeading>
            <div style={{
              padding: 24, textAlign: 'center',
              color: 'var(--text3)', fontSize: 13,
              border: '1px solid var(--border)', borderRadius: 8,
              background: 'var(--bg1)',
            }}>
              No incidents reported.
            </div>
          </section>
        )}

        {/* Subscribe — boxed at the bottom, intentionally
            small so it doesn't dominate above-the-fold. */}
        <section style={{ marginTop: 56 }}>
          <SubscribeForm />
        </section>

        <div style={{
          marginTop: 64,
          paddingTop: 24,
          borderTop: '1px solid var(--border)',
          color: 'var(--text3)', fontSize: 11,
          display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6,
        }}>
          <TelescopeIcon size={12} /> <span>Powered by Coremetry</span>
        </div>
      </div>
    </div>
  );
}

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      fontSize: 11, fontWeight: 600,
      color: 'var(--text3)',
      letterSpacing: 1.2,
      textTransform: 'uppercase',
      marginBottom: 14,
    }}>
      {children}
    </div>
  );
}

function Hero({ status, description, checkedAt }: {
  status: 'operational' | 'degraded' | 'outage';
  description?: string;
  checkedAt: string;
}) {
  const headline = status === 'operational' ? 'All systems operational'
    : status === 'degraded'    ? 'Some systems are experiencing issues'
    :                            'Major outage in progress';
  const accent = status === 'operational' ? 'var(--ok)'
    : status === 'degraded'    ? 'var(--warn)'
    :                            'var(--err)';
  return (
    <div style={{
      padding: '32px 24px',
      borderRadius: 12,
      background: status === 'operational'
        ? 'rgba(63,185,80,0.08)'
        : status === 'degraded'
          ? 'rgba(245,159,0,0.08)'
          : 'rgba(255,82,82,0.08)',
      border: `1px solid ${accent}`,
    }}>
      <div style={{
        display: 'inline-flex', alignItems: 'center', gap: 8,
        marginBottom: 14,
      }}>
        <span style={{
          width: 10, height: 10, borderRadius: '50%',
          background: accent,
          boxShadow: `0 0 8px ${accent}`,
        }} />
        <span style={{
          fontSize: 11, fontWeight: 700,
          letterSpacing: 1.2, textTransform: 'uppercase',
          color: accent,
        }}>{statusLabel(status)}</span>
      </div>
      <div style={{
        fontSize: 28, fontWeight: 700,
        color: 'var(--text)', lineHeight: 1.25,
        marginBottom: description ? 14 : 10,
      }}>
        {headline}
      </div>
      {description && (
        <div style={{
          fontSize: 14, color: 'var(--text2)', lineHeight: 1.55,
          marginBottom: 10,
        }}>
          {description}
        </div>
      )}
      <div style={{
        fontSize: 11, color: 'var(--text3)',
      }}>
        Last updated {new Date(checkedAt).toLocaleString()} · refreshes every 30s
      </div>
    </div>
  );
}

function statusLabel(s: 'operational' | 'degraded' | 'outage'): string {
  return s === 'operational' ? 'Operational' : s === 'degraded' ? 'Degraded' : 'Outage';
}

function ComponentLine({ c, first }: { c: ComponentRow; first: boolean }) {
  const cls = c.status === 'operational' ? 'operational'
    : c.status === 'degraded'    ? 'degraded'
    : c.status === 'outage'      ? 'outage'
    :                              'unknown';
  const color = cls === 'operational' ? 'var(--ok)'
    : cls === 'degraded' ? 'var(--warn)'
    : cls === 'outage'   ? 'var(--err)'
    : 'var(--text3)';
  const pillLabel = c.status === 'unknown' ? 'No data'
    : c.status === 'operational' ? 'Operational'
    : c.status === 'degraded' ? 'Degraded'
    : 'Outage';
  return (
    <div style={{
      padding: '14px 18px',
      borderTop: first ? 'none' : '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <span style={{
          width: 8, height: 8, borderRadius: '50%',
          background: color,
          boxShadow: cls !== 'unknown' ? `0 0 4px ${color}` : 'none',
          flexShrink: 0,
        }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 14, fontWeight: 500, color: 'var(--text)' }}>
            {c.name}
          </div>
          {c.description && (
            <div style={{ fontSize: 12, color: 'var(--text3)', marginTop: 2 }}>
              {c.description}
            </div>
          )}
          {c.message && (
            <div style={{ fontSize: 12, color, marginTop: 2 }}>
              {c.message}
            </div>
          )}
        </div>
        <div style={{
          fontSize: 11, color, fontWeight: 600,
          letterSpacing: 0.4, textTransform: 'uppercase',
          flexShrink: 0,
        }}>
          {pillLabel}
        </div>
      </div>
      {c.uptimeDays && c.uptimeDays.length > 0 && (
        <div style={{ marginTop: 14, marginLeft: 20 }}>
          <UptimeBar days={c.uptimeDays} />
          <div style={{
            display: 'flex', justifyContent: 'space-between',
            fontSize: 10, color: 'var(--text3)', marginTop: 6,
          }}>
            <span>{c.uptimeDays.length} days ago</span>
            <span>{uptimePct(c.uptimeDays)}% uptime</span>
            <span>Today</span>
          </div>
        </div>
      )}
    </div>
  );
}

function UptimeBar({ days }: { days: number[] }) {
  return (
    <div style={{ display: 'flex', gap: 2, alignItems: 'center', maxWidth: '100%' }}>
      {days.map((r, i) => {
        let bg = 'var(--ok)';
        let title = 'Operational';
        if (r < 0)         { bg = 'var(--bg3)'; title = 'No data'; }
        else if (r < 0.95) { bg = 'var(--err)'; title = `${(r * 100).toFixed(1)}% — major issues`; }
        else if (r < 0.99) { bg = 'var(--warn)'; title = `${(r * 100).toFixed(1)}% — minor issues`; }
        else               { title = `${(r * 100).toFixed(1)}% uptime`; }
        return <span key={i} title={title} style={{
          flex: 1, height: 28, background: bg,
          borderRadius: 2, opacity: 0.9,
        }} />;
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

// IncidentList — groups by recency bucket so the visitor sees
// "what's happening today" first without scanning the full
// history. Matches status.claude.com's "Today / Yesterday /
// older" layout.
function IncidentList({ incidents }: { incidents: IncidentRow[] }) {
  const now = Date.now();
  const dayMs = 86400_000;
  const startOfDay = (ts: number) => {
    const d = new Date(ts / 1e6);
    d.setHours(0, 0, 0, 0);
    return d.getTime();
  };
  const today = new Date(); today.setHours(0, 0, 0, 0);
  const buckets: Record<string, IncidentRow[]> = {};
  const labelFor = (ts: number): string => {
    const day = startOfDay(ts);
    const today0 = today.getTime();
    if (day === today0) return 'Today';
    if (day === today0 - dayMs) return 'Yesterday';
    if (now - day < 7 * dayMs) return 'This week';
    if (now - day < 30 * dayMs) return 'This month';
    return 'Earlier';
  };
  for (const i of incidents) {
    const k = labelFor(i.startedAt);
    (buckets[k] ??= []).push(i);
  }
  const order = ['Today', 'Yesterday', 'This week', 'This month', 'Earlier'];
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
      {order.flatMap(k => buckets[k] ? [(
        <div key={k}>
          <div style={{
            fontSize: 12, fontWeight: 600,
            color: 'var(--text2)', marginBottom: 10,
          }}>{k}</div>
          <div style={{
            border: '1px solid var(--border)', borderRadius: 8,
            background: 'var(--bg1)', overflow: 'hidden',
          }}>
            {buckets[k].map((i, idx) => (
              <IncidentItem key={i.id} i={i} first={idx === 0} />
            ))}
          </div>
        </div>
      )] : [])}
    </div>
  );
}

function IncidentItem({ i, first }: { i: IncidentRow; first: boolean }) {
  const sevColor = i.severity === 'critical' ? 'var(--err)'
    : i.severity === 'warning' ? 'var(--warn)' : 'var(--text3)';
  const statusLine = i.status === 'resolved' ? 'Resolved'
    : i.status === 'acknowledged' ? 'Investigating'
    : 'Ongoing';
  const started = new Date(i.startedAt / 1e6);
  return (
    <div style={{
      padding: '14px 18px',
      borderTop: first ? 'none' : '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10 }}>
        <span style={{
          fontSize: 10, fontWeight: 600,
          color: sevColor,
          textTransform: 'uppercase', letterSpacing: 0.6,
        }}>
          {i.severity}
        </span>
        <span style={{ fontSize: 14, fontWeight: 600, color: 'var(--text)' }}>
          {i.title}
        </span>
        <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
          {statusLine}
        </span>
      </div>
      {i.body && (
        <div style={{
          color: 'var(--text2)', fontSize: 13, lineHeight: 1.55,
          whiteSpace: 'pre-wrap', marginTop: 8,
        }}>{i.body}</div>
      )}
      <div style={{
        color: 'var(--text3)', fontSize: 11, marginTop: 8,
        fontFamily: 'ui-monospace, monospace',
      }}>
        {started.toLocaleString()}
        {i.resolvedAt && ` → ${new Date(i.resolvedAt / 1e6).toLocaleString()}`}
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
      padding: 24,
      borderRadius: 8,
      background: 'var(--bg1)',
      border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text)' }}>
        Subscribe to updates
      </div>
      <p style={{ color: 'var(--text3)', fontSize: 12, margin: '6px 0 14px', lineHeight: 1.55 }}>
        Get an email whenever an incident opens or resolves. You'll receive a
        confirmation link first.
      </p>
      <form onSubmit={submit} style={{ display: 'flex', gap: 8 }}>
        <input required type="email" value={email}
          onChange={e => setEmail(e.target.value)}
          placeholder="you@example.com"
          aria-label="Email address to subscribe"
          style={{ flex: 1 }} />
        <button type="submit" disabled={busy}>
          {busy ? 'Subscribing…' : 'Subscribe'}
        </button>
      </form>
      {msg && (
        <div style={{
          marginTop: 10, fontSize: 12,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
        }}>{msg.text}</div>
      )}
    </div>
  );
}
