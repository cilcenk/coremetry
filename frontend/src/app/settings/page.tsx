'use client';
import { useEffect, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import type { SMTPSettings, NotificationChannel, ChannelType } from '@/lib/types';

type Tab = 'smtp' | 'channels' | 'ai' | 'retention';

export default function SettingsPage() {
  const { user } = useAuth();
  const [tab, setTab] = useState<Tab>('smtp');

  if (user && user.role !== 'admin') {
    return (
      <>
        <Topbar title="Settings" />
        <div id="content">
          <Empty icon="🔒" title="Admin access required">
            System settings are only available to administrators.
          </Empty>
        </div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Settings" />
      <div id="content">
        <div style={{ display: 'flex', gap: 4, borderBottom: '1px solid var(--border)', marginBottom: 16 }}>
          <TabBtn active={tab === 'smtp'} onClick={() => setTab('smtp')}>📨 SMTP</TabBtn>
          <TabBtn active={tab === 'channels'} onClick={() => setTab('channels')}>🔔 Notification channels</TabBtn>
          <TabBtn active={tab === 'ai'} onClick={() => setTab('ai')}>🤖 AI Copilot</TabBtn>
          <TabBtn active={tab === 'retention'} onClick={() => setTab('retention')}>🗑 Data retention</TabBtn>
        </div>
        {tab === 'smtp' && <SMTPTab />}
        {tab === 'channels' && <ChannelsTab />}
        {tab === 'ai' && <AITab />}
        {tab === 'retention' && <RetentionTab />}
      </div>
    </>
  );
}

function TabBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} style={{
      padding: '8px 14px', background: 'transparent',
      border: 'none', borderBottom: active ? '2px solid var(--accent)' : '2px solid transparent',
      color: active ? 'var(--text)' : 'var(--text2)',
      fontSize: 13, fontWeight: active ? 600 : 500, cursor: 'pointer',
    }}>{children}</button>
  );
}

// ── SMTP tab ────────────────────────────────────────────────────────────────

function SMTPTab() {
  const [s, setS] = useState<SMTPSettings | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [testTo, setTestTo] = useState('');

  const load = () => {
    setS(undefined);
    api.getSMTP().then(setS).catch(() => setS(null));
  };
  useEffect(load, []);

  if (s === undefined) return <Spinner />;
  if (s === null) return <Empty icon="⚠" title="Failed to load SMTP settings" />;

  const update = <K extends keyof SMTPSettings>(k: K, v: SMTPSettings[K]) => setS({ ...s, [k]: v });

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      // If the password field is still the masked sentinel, send empty
      // string — the backend treats "empty / ********" as "keep current".
      const payload = { ...s, password: s.password === '********' ? '' : s.password };
      const next = await api.putSMTP(payload);
      setS(next);
      setMsg({ kind: 'ok', text: 'Saved.' });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const sendTest = async () => {
    if (!testTo) { setMsg({ kind: 'err', text: 'Enter a recipient first' }); return; }
    setBusy(true); setMsg(null);
    try {
      await api.testSMTP(testTo);
      setMsg({ kind: 'ok', text: `Test email sent to ${testTo}.` });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={save} style={{ maxWidth: 640 }}>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Outbound mail settings used by every email notification channel.
        Changes take effect immediately — no restart needed.
      </p>

      <Row>
        <Field label="SMTP host" flex={2}>
          <input required value={s.host} placeholder="smtp.example.com"
            onChange={e => update('host', e.target.value)} />
        </Field>
        <Field label="Port" flex={1}>
          <input required type="number" value={s.port || ''} placeholder="587"
            onChange={e => update('port', parseInt(e.target.value || '0'))} />
        </Field>
      </Row>
      <Row>
        <Field label="Username" flex={1}>
          <input value={s.username} onChange={e => update('username', e.target.value)} />
        </Field>
        <Field label="Password" flex={1}>
          <input type="password" value={s.password}
            placeholder={s.configured && !s.password ? '(unchanged)' : ''}
            onChange={e => update('password', e.target.value)} />
        </Field>
      </Row>
      <Row>
        <Field label="From address" flex={2}>
          <input required type="email" value={s.from} placeholder="coremetry@yourcorp.com"
            onChange={e => update('from', e.target.value)} />
        </Field>
        <Field label="From name (optional)" flex={1}>
          <input value={s.fromName} placeholder="Coremetry Alerts"
            onChange={e => update('fromName', e.target.value)} />
        </Field>
      </Row>
      <Row>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--text2)', fontSize: 12 }}>
          <input type="checkbox" checked={s.startTLS}
            onChange={e => update('startTLS', e.target.checked)} />
          Use STARTTLS (recommended for ports 587/25)
        </label>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--text2)', fontSize: 12 }}>
          <input type="checkbox" checked={s.skipVerify}
            onChange={e => update('skipVerify', e.target.checked)} />
          Skip TLS verification (self-signed only)
        </label>
      </Row>

      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}

      <div style={{ display: 'flex', gap: 8, marginTop: 18, alignItems: 'center' }}>
        <button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save settings'}</button>
        <div style={{ flex: 1 }} />
        <input type="email" value={testTo} placeholder="recipient@example.com"
          onChange={e => setTestTo(e.target.value)} style={{ width: 240 }} />
        <button type="button" className="sec" onClick={sendTest} disabled={busy || !s.configured}>
          Send test email
        </button>
      </div>
      {!s.configured && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
          Save valid SMTP settings before testing.
        </div>
      )}
    </form>
  );
}

// ── Channels tab ────────────────────────────────────────────────────────────

function ChannelsTab() {
  const [items, setItems] = useState<NotificationChannel[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<NotificationChannel | 'new' | null>(null);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const refresh = () => {
    setItems(undefined);
    api.listChannels().then(d => setItems(d ?? [])).catch(() => setItems(null));
  };
  useEffect(refresh, []);

  const onDelete = async (c: NotificationChannel) => {
    if (!confirm(`Delete channel "${c.name}"?`)) return;
    try { await api.deleteChannel(c.id); refresh(); }
    catch (err) { setMsg({ kind: 'err', text: humanize(err) }); }
  };
  const onTest = async (c: NotificationChannel) => {
    setMsg(null);
    try {
      await api.testChannel(c.id);
      setMsg({ kind: 'ok', text: `Test sent through "${c.name}".` });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    }
  };

  return (
    <div>
      <div className="controls" style={{ marginBottom: 12 }}>
        <p style={{ color: 'var(--text2)', fontSize: 13, margin: 0 }}>
          Channels receive Problem alerts whenever the evaluator or anomaly detector opens a new incident.
        </p>
        <button onClick={() => setEditing('new')} style={{ marginLeft: 'auto' }}>+ New channel</button>
      </div>

      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}

      {items === undefined && <Spinner />}
      {items !== undefined && (!items || items.length === 0) && (
        <Empty icon="🔔" title="No channels yet">
          Create one to start receiving alert notifications.
        </Empty>
      )}
      {items && items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Type</th>
                <th>Recipients / target</th>
                <th>Min severity</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {items.map(c => (
                <tr key={c.id}>
                  <td><b>{c.name}</b></td>
                  <td className="mono">{c.type}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{summarizeChannel(c)}</td>
                  <td><SeverityBadge s={c.minSeverity} /></td>
                  <td>{c.enabled
                    ? <span className="badge b-ok">ON</span>
                    : <span className="badge b-gray">OFF</span>}
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <button className="sec" onClick={() => onTest(c)} style={{ marginRight: 6 }}>Test</button>
                    <button className="sec" onClick={() => setEditing(c)} style={{ marginRight: 6 }}>Edit</button>
                    <button className="sec" onClick={() => onDelete(c)}
                      style={{ color: 'var(--err)' }}>Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing && (
        <ChannelModal
          initial={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); refresh(); }}
        />
      )}
    </div>
  );
}

function summarizeChannel(c: NotificationChannel): string {
  if (c.type === 'email') return (c.config.recipients ?? []).join(', ') || '(none)';
  if (c.type === 'slack' || c.type === 'mattermost') return c.config.webhookUrl ?? '(no webhook)';
  if (c.type === 'webhook') return c.config.url ?? '(no url)';
  if (c.type === 'whatsapp') return (c.config.to ?? []).join(', ') || '(no recipients)';
  return '';
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}

function ChannelModal({ initial, onClose, onSaved }: {
  initial: NotificationChannel | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [type, setType] = useState<ChannelType>(initial?.type ?? 'email');
  const [recipients, setRecipients] = useState((initial?.config.recipients ?? []).join(', '));
  const [webhookUrl, setWebhookUrl] = useState(initial?.config.webhookUrl ?? '');
  const [url, setUrl] = useState(initial?.config.url ?? '');
  // WhatsApp / Twilio fields
  const [twilioSid, setTwilioSid] = useState(initial?.config.accountSid ?? '');
  const [twilioToken, setTwilioToken] = useState(initial?.config.authToken ?? '');
  const [waFrom, setWaFrom] = useState(initial?.config.from ?? '');
  const [waTo, setWaTo] = useState((initial?.config.to ?? []).join(', '));
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [minSeverity, setMinSeverity] = useState<'info' | 'warning' | 'critical'>(initial?.minSeverity ?? 'warning');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const config: NotificationChannel['config'] = {};
      if (type === 'email') {
        config.recipients = recipients.split(/[,;\s]+/).map(s => s.trim()).filter(Boolean);
        if (config.recipients.length === 0) throw new Error('At least one recipient is required');
      } else if (type === 'slack' || type === 'mattermost') {
        if (!webhookUrl) throw new Error(`${type === 'slack' ? 'Slack' : 'Mattermost'} webhook URL is required`);
        config.webhookUrl = webhookUrl;
      } else if (type === 'webhook') {
        if (!url) throw new Error('Webhook URL is required');
        config.url = url;
      } else if (type === 'whatsapp') {
        if (!twilioSid || !twilioToken) throw new Error('Twilio Account SID and Auth Token are required');
        if (!waFrom) throw new Error('Sender number (whatsapp:+E164) is required');
        const tos = waTo.split(/[,;\s]+/).map(s => s.trim()).filter(Boolean);
        if (tos.length === 0) throw new Error('At least one WhatsApp recipient is required');
        config.accountSid = twilioSid.trim();
        config.authToken = twilioToken.trim();
        config.from = waFrom.trim();
        config.to = tos;
      }
      const payload = { name, type, config, enabled, minSeverity };
      if (initial) await api.updateChannel(initial.id, payload);
      else        await api.createChannel(payload);
      onSaved();
    } catch (err) {
      setError(humanize(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 460, padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>
          {initial ? `Edit channel — ${initial.name}` : 'New channel'}
        </div>
        <form onSubmit={submit}>
          <Field label="Name">
            <input required autoFocus value={name}
              onChange={e => setName(e.target.value)} style={{ width: '100%' }} />
          </Field>
          <Row>
            <Field label="Type" flex={1}>
              <select value={type} onChange={e => setType(e.target.value as ChannelType)}>
                <option value="email">Email</option>
                <option value="slack">Slack</option>
                <option value="mattermost">Mattermost</option>
                <option value="webhook">Webhook (generic JSON POST)</option>
                <option value="whatsapp">WhatsApp (via Twilio)</option>
              </select>
            </Field>
            <Field label="Min severity" flex={1}>
              <select value={minSeverity}
                onChange={e => setMinSeverity(e.target.value as 'info' | 'warning' | 'critical')}>
                <option value="info">Info (every problem)</option>
                <option value="warning">Warning</option>
                <option value="critical">Critical only</option>
              </select>
            </Field>
          </Row>

          {type === 'email' && (
            <Field label="Recipients (comma-separated)">
              <input required value={recipients} placeholder="oncall@acme.com, sre@acme.com"
                onChange={e => setRecipients(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'slack' && (
            <Field label="Slack incoming webhook URL">
              <input required value={webhookUrl} placeholder="https://hooks.slack.com/services/T.../B.../..."
                onChange={e => setWebhookUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'mattermost' && (
            <Field label="Mattermost incoming webhook URL">
              <input required value={webhookUrl} placeholder="https://your-mattermost.example.com/hooks/..."
                onChange={e => setWebhookUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'webhook' && (
            <Field label="Webhook URL (raw Problem JSON is POSTed here)">
              <input required value={url} placeholder="https://your-receiver.example.com/incidents"
                onChange={e => setUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'whatsapp' && (
            <>
              <Row>
                <Field label="Twilio Account SID" flex={1}>
                  <input required value={twilioSid} placeholder="ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
                    onChange={e => setTwilioSid(e.target.value)} style={{ width: '100%' }} />
                </Field>
                <Field label="Auth Token" flex={1}>
                  <input required type="password" value={twilioToken} placeholder="32-char Auth Token"
                    onChange={e => setTwilioToken(e.target.value)} style={{ width: '100%' }} />
                </Field>
              </Row>
              <Field label="Sender number (with whatsapp: prefix)">
                <input required value={waFrom} placeholder="whatsapp:+14155238886 (Twilio sandbox) or your approved number"
                  onChange={e => setWaFrom(e.target.value)} style={{ width: '100%' }} />
              </Field>
              <Field label="Recipient numbers (comma-separated, E.164)">
                <input required value={waTo} placeholder="+905XXXXXXXXX, +1XXXXXXXXXX"
                  onChange={e => setWaTo(e.target.value)} style={{ width: '100%' }} />
              </Field>
              <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: -4 }}>
                Twilio is the de-facto WhatsApp Business API broker. The sandbox lets you test for free
                (recipients must opt in by texting the join code). Production usage requires a Twilio-approved sender.
              </p>
            </>
          )}

          <label style={{ display: 'flex', gap: 6, alignItems: 'center',
                          color: 'var(--text2)', fontSize: 12, marginTop: 6 }}>
            <input type="checkbox" checked={enabled}
              onChange={e => setEnabled(e.target.checked)} />
            Enabled
          </label>

          {error && <FlashBox kind="err">{error}</FlashBox>}
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 18 }}>
            <button type="button" className="sec" onClick={onClose}>Cancel</button>
            <button type="submit" disabled={busy}>
              {busy ? 'Saving…' : initial ? 'Update' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ── Tiny shared primitives ──────────────────────────────────────────────────

function Field({ label, children, flex }: { label: string; children: React.ReactNode; flex?: number }) {
  return (
    <label style={{ display: 'block', marginBottom: 12, flex }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

function Row({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
      {children}
    </div>
  );
}

function FlashBox({ kind, children }: { kind: 'ok' | 'err'; children: React.ReactNode }) {
  const colors = kind === 'ok'
    ? { fg: 'var(--ok)',  bg: 'rgba(63,185,80,0.08)',  bd: 'rgba(63,185,80,0.3)' }
    : { fg: 'var(--err)', bg: 'rgba(220,38,38,0.08)',  bd: 'rgba(220,38,38,0.3)' };
  return (
    <div style={{
      color: colors.fg, fontSize: 12, marginTop: 12,
      padding: '6px 10px', background: colors.bg,
      border: `1px solid ${colors.bd}`, borderRadius: 4,
    }}>{children}</div>
  );
}

// AITab — read-only status of the AI Copilot integration. Configuration
// is server-side via env vars (COREMETRY_AI_API_KEY) so the operator
// who edits compose / Helm controls credentials, not anyone with admin
// access in the UI. This tab just shows whether the wiring's working.
function AITab() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);

  if (enabled === null) return <Spinner />;

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>AI Copilot</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Inline natural-language explanations for traces and Problems via Anthropic's
        Messages API. The button shows up automatically on the trace detail page and
        in the Problems table once configured.
      </p>

      <div className={`status-banner status-banner-${enabled ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${enabled ? 'operational' : 'degraded'}`}>
          {enabled ? 'CONFIGURED' : 'NOT CONFIGURED'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {enabled
            ? 'AI Copilot is ready — buttons are visible on trace and problem pages.'
            : 'AI Copilot is dormant — set COREMETRY_AI_API_KEY to enable.'}
        </span>
      </div>

      {!enabled && (
        <div style={{ marginTop: 18, padding: 16, borderRadius: 8,
          background: 'var(--bg2)', border: '1px solid var(--border)' }}>
          <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 8 }}>Enable in 3 steps</h3>
          <ol style={{ fontSize: 13, lineHeight: 1.7, color: 'var(--text)', paddingLeft: 18 }}>
            <li>
              Get an API key from{' '}
              <a href="https://console.anthropic.com/settings/keys" target="_blank" rel="noopener"
                 style={{ color: 'var(--accent2)' }}>console.anthropic.com</a>.
            </li>
            <li>
              Set the env var on the coremetry container:{' '}
              <code style={{ background: 'var(--bg0)', padding: '1px 6px', borderRadius: 3 }}>
                COREMETRY_AI_API_KEY=sk-ant-…
              </code>
              {' '}(via <code>.env</code> for compose, or a Secret on Helm).
            </li>
            <li>Restart the coremetry container — this banner flips to <b style={{ color: 'var(--ok)' }}>CONFIGURED</b>.</li>
          </ol>
          <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: 10 }}>
            Default model: <code>claude-sonnet-4-6</code>.{' '}
            Override with <code>COREMETRY_AI_MODEL</code>.
          </p>
        </div>
      )}

      {enabled && (
        <div style={{ marginTop: 18, padding: 16, borderRadius: 8,
          background: 'var(--bg2)', border: '1px solid var(--border)' }}>
          <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 8 }}>What it does</h3>
          <ul style={{ fontSize: 13, lineHeight: 1.7, color: 'var(--text)', paddingLeft: 18 }}>
            <li>
              <b>🤖 Explain this trace</b> — on any trace detail page. Pulls the spans server-side,
              builds a compact summary, asks for slowest span / error concentration / root-cause hint.
            </li>
            <li>
              <b>🤖</b> column on the <a href="/problems" style={{ color: 'var(--accent2)' }}>Problems</a> page.
              Click the icon to get plain-language meaning + ranked likely causes + first three things to check.
            </li>
          </ul>
          <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: 10 }}>
            Cost: ~1¢-3¢ per call against Sonnet at typical trace size.
          </p>
        </div>
      )}
    </div>
  );
}

// RetentionTab — per-signal TTL controls. Each signal (spans / logs /
// metrics / profiles) takes a number + unit (hours / days). Save calls
// PUT /api/settings/retention which runs ALTER TABLE ... MODIFY TTL on
// the underlying ClickHouse tables. Effect is online — ClickHouse
// re-evaluates TTL on next merge so deletions catch up within ~10 min.
function RetentionTab() {
  type Unit = 'h' | 'd';
  type Row = { value: string; unit: Unit };
  const empty: Row = { value: '', unit: 'd' };
  const [spans,    setSpans]    = useState<Row>(empty);
  const [logs,     setLogs]     = useState<Row>(empty);
  const [metrics,  setMetrics]  = useState<Row>(empty);
  const [profiles, setProfiles] = useState<Row>(empty);
  const [busy, setBusy] = useState(false);
  const [msg,  setMsg]  = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const decode = (s?: string): Row => {
    const m = s?.match(/^(\d+)([hd])$/);
    return m ? { value: m[1], unit: m[2] as Unit } : empty;
  };
  const encode = (r: Row): string => r.value ? `${r.value}${r.unit}` : '';

  useEffect(() => {
    api.getRetention().then(sp => {
      setSpans(decode(sp.spans));
      setLogs(decode(sp.logs));
      setMetrics(decode(sp.metrics));
      setProfiles(decode(sp.profiles));
    }).catch(() => {});
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      await api.putRetention({
        spans:    encode(spans),
        logs:     encode(logs),
        metrics:  encode(metrics),
        profiles: encode(profiles),
      });
      setMsg({ kind: 'ok', text: 'Applied — ClickHouse will re-evaluate TTL on next merge (~10 min).' });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} style={{ maxWidth: 560 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Data retention</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Per-signal TTL on the underlying ClickHouse tables. Older data is dropped
        on the next merge cycle. Leave a field blank to keep the current value
        (initial defaults come from the config file: spans 30d, logs 30d,
        metrics 7d, profiles 7d).
      </p>

      <RetentionRow label="Spans"    row={spans}    setRow={setSpans} />
      <RetentionRow label="Logs"     row={logs}     setRow={setLogs} />
      <RetentionRow label="Metrics"  row={metrics}  setRow={setMetrics} />
      <RetentionRow label="Profiles" row={profiles} setRow={setProfiles} />

      <div style={{ marginTop: 14, display: 'flex', gap: 8, alignItems: 'center' }}>
        <button type="submit" disabled={busy}>{busy ? 'Applying…' : 'Apply'}</button>
        {msg && (
          <span style={{ fontSize: 12, color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </span>
        )}
      </div>

      <p style={{ marginTop: 18, fontSize: 11, color: 'var(--text3)' }}>
        Hour-precision TTL is supported (e.g. <code>36h</code>) but ClickHouse
        partitions data per day, so very short retention windows still
        process at day-boundary granularity. Examples: <code>48h</code> = last 2 days,
        <code> 2d</code> = same thing, <code>30d</code> = last 30 days.
      </p>
    </form>
  );
}

function RetentionRow({ label, row, setRow }: {
  label: string;
  row: { value: string; unit: 'h' | 'd' };
  setRow: (r: { value: string; unit: 'h' | 'd' }) => void;
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 10 }}>
      <span style={{ width: 90, fontSize: 13 }}>{label}</span>
      <input type="number" min={1} value={row.value}
        onChange={e => setRow({ ...row, value: e.target.value })}
        placeholder="(unchanged)"
        style={{ width: 100 }} />
      <select value={row.unit}
        onChange={e => setRow({ ...row, unit: e.target.value as 'h' | 'd' })}>
        <option value="h">hours</option>
        <option value="d">days</option>
      </select>
    </div>
  );
}

function humanize(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  const body = msg.replace(/^HTTP \d+:\s*/, '');
  try {
    const j = JSON.parse(body);
    if (j && typeof j.error === 'string') return j.error;
  } catch {}
  return body || msg;
}
