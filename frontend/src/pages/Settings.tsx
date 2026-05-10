import { useEffect, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import type {
  SMTPSettings, NotificationChannel, ChannelType, AIProvider,
  LDAPConfig, LDAPGroupRoleMapping, LDAPDirectoryUser, Role,
} from '@/lib/types';
import {
  IconMail, IconBell, IconSparkles, IconLock, IconTrash,
} from '@/components/icons';

type Tab = 'smtp' | 'channels' | 'ai' | 'ldap' | 'retention' | 'sampling';

export default function SettingsPage() {
  const { user } = useAuth();
  const [tab, setTab] = useState<Tab>('smtp');

  if (user && user.role !== 'admin') {
    return (
      <>
        <Topbar title="Settings" />
        <div id="content">
          <Empty icon={<IconLock size={28} />} title="Admin access required">
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
        <div className="tab-strip" style={{ marginBottom: 16 }}>
          <TabBtn active={tab === 'smtp'} onClick={() => setTab('smtp')}>
            <IconMail /> <span style={{ marginLeft: 6 }}>SMTP</span>
          </TabBtn>
          <TabBtn active={tab === 'channels'} onClick={() => setTab('channels')}>
            <IconBell /> <span style={{ marginLeft: 6 }}>Notification channels</span>
          </TabBtn>
          <TabBtn active={tab === 'ai'} onClick={() => setTab('ai')}>
            <IconSparkles /> <span style={{ marginLeft: 6 }}>AI Copilot</span>
          </TabBtn>
          <TabBtn active={tab === 'ldap'} onClick={() => setTab('ldap')}>
            <IconLock /> <span style={{ marginLeft: 6 }}>LDAP / AD</span>
          </TabBtn>
          <TabBtn active={tab === 'retention'} onClick={() => setTab('retention')}>
            <IconTrash /> <span style={{ marginLeft: 6 }}>Data retention</span>
          </TabBtn>
          <TabBtn active={tab === 'sampling'} onClick={() => setTab('sampling')}>
            <span style={{ fontFamily: 'monospace' }}>%</span>
            <span style={{ marginLeft: 6 }}>Trace sampling</span>
          </TabBtn>
        </div>
        {tab === 'smtp' && <SMTPTab />}
        {tab === 'channels' && <ChannelsTab />}
        {tab === 'ai' && <AITab />}
        {tab === 'ldap' && <LDAPTab />}
        {tab === 'retention' && <RetentionTab />}
        {tab === 'sampling' && <SamplingTab />}
      </div>
    </>
  );
}

function TabBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} className={active ? 'active' : ''}>{children}</button>
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
        <Empty icon={<IconBell size={28} />} title="No channels yet">
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
  if (c.type === 'teams' || c.type === 'zoomchat') return c.config.webhookUrl ?? '(no webhook)';
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
  // Zoom Chat verification token — optional second-factor
  // Zoom hands out alongside the webhook URL.
  const [verificationToken, setVerificationToken] = useState(initial?.config.verificationToken ?? '');
  // WhatsApp / Twilio fields
  const [twilioSid, setTwilioSid] = useState(initial?.config.accountSid ?? '');
  const [twilioToken, setTwilioToken] = useState(initial?.config.authToken ?? '');
  const [waFrom, setWaFrom] = useState(initial?.config.from ?? '');
  const [waTo, setWaTo] = useState((initial?.config.to ?? []).join(', '));
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [minSeverity, setMinSeverity] = useState<'info' | 'warning' | 'critical'>(initial?.minSeverity ?? 'warning');
  // Routing predicates — comma-separated in the UI, parsed
  // into string arrays on save. Empty / blank inputs leave
  // the predicate unset so the channel stays a catch-all.
  const [matchServices, setMatchServices] = useState((initial?.matchRules?.services ?? []).join(', '));
  const [matchSREs, setMatchSREs] = useState((initial?.matchRules?.sreTeams ?? []).join(', '));
  const [matchOwners, setMatchOwners] = useState((initial?.matchRules?.ownerTeams ?? []).join(', '));
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
      } else if (type === 'teams') {
        if (!webhookUrl) throw new Error('Microsoft Teams webhook URL is required');
        config.webhookUrl = webhookUrl;
      } else if (type === 'zoomchat') {
        if (!webhookUrl) throw new Error('Zoom Chat webhook URL is required');
        config.webhookUrl = webhookUrl;
        if (verificationToken) config.verificationToken = verificationToken.trim();
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
      const splitCSL = (s: string) =>
        s.split(/[,;\s]+/).map(x => x.trim()).filter(Boolean);
      const matchRules = {
        services:   splitCSL(matchServices),
        sreTeams:   splitCSL(matchSREs),
        ownerTeams: splitCSL(matchOwners),
      };
      const payload = { name, type, config, enabled, minSeverity, matchRules };
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
                <option value="teams">Microsoft Teams</option>
                <option value="zoomchat">Zoom Chat</option>
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
          {type === 'teams' && (
            <Field label="Microsoft Teams incoming webhook URL">
              <input required value={webhookUrl}
                placeholder="https://outlook.office.com/webhook/..."
                onChange={e => setWebhookUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'zoomchat' && (
            <>
              <Field label="Zoom Chat incoming webhook URL">
                <input required value={webhookUrl}
                  placeholder="https://hooks.zoom.us/v3/hooks/<id>/<token>"
                  onChange={e => setWebhookUrl(e.target.value)} style={{ width: '100%' }} />
              </Field>
              <Field label="Verification token (optional)">
                <input value={verificationToken} type="password"
                  placeholder="Pasted from the Zoom webhook configuration screen"
                  onChange={e => setVerificationToken(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
            </>
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

          {/* Routing predicates — gate this channel to a
              subset of services / SRE teams / owner teams.
              Empty = catch-all; populated lists AND together
              with the existing severity threshold. Each
              channel can pin to a specific team's Zoom Chat
              while a "default" channel without rules still
              fires for everything. */}
          <details style={{ marginTop: 16, fontSize: 12, color: 'var(--text2)' }}>
            <summary style={{ cursor: 'pointer', fontWeight: 600 }}>
              Routing rules (leave empty for catch-all)
            </summary>
            <div style={{ marginTop: 8 }}>
              <Field label="Match services (comma-separated)">
                <input value={matchServices}
                  placeholder="payments, order-service"
                  onChange={e => setMatchServices(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Match SRE teams (comma-separated)">
                <input value={matchSREs}
                  placeholder="platform, sre-storefront"
                  onChange={e => setMatchSREs(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Match owner teams (comma-separated)">
                <input value={matchOwners}
                  placeholder="payments, ml"
                  onChange={e => setMatchOwners(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
                Predicates AND together. e.g. services=<code>payments</code> +
                sreTeams=<code>platform</code> means "fire only when the problem
                is on <code>payments</code> AND its catalog SRE team is
                <code>platform</code>". Service catalog metadata is the source
                of truth for sreTeam / ownerTeam lookup.
              </p>
            </div>
          </details>

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

// AITab — editable AI Copilot configuration. Admin picks a provider,
// pastes their key, optionally sets a model, hits Save. Server stores
// the override in system_settings and updates the live service so the
// next Explain call uses the new creds without restart.
//
// Two providers:
//   - Anthropic: classic sk-ant-… key.
//   - GitHub Copilot: GitHub OAuth token (ghu_…) with Copilot access;
//     server exchanges it for a session token and calls
//     api.githubcopilot.com (OpenAI-compatible).
function AITab() {
  const [loaded, setLoaded] = useState(false);
  const [provider, setProvider] = useState<AIProvider>('anthropic');
  const [model, setModel] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [hasKey, setHasKey] = useState(false);
  const [apiKey, setApiKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAISettings().then(s => {
      setProvider(s.provider || 'anthropic');
      setModel(s.model || '');
      setBaseUrl(s.baseUrl || '');
      setHasKey(s.hasKey);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey, model, baseUrl });
      setHasKey(next.hasKey);
      setApiKey('');
      setMsg({ kind: 'ok', text: next.hasKey ? 'Saved — Copilot is live.' : 'Saved — Copilot disabled.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const clearKey = async () => {
    if (!confirm('Remove the saved API key? Copilot buttons will disappear until a new key is set.')) return;
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey: '', model, baseUrl });
      setHasKey(next.hasKey);
      setApiKey('');
      setMsg({ kind: 'ok', text: 'Key cleared — Copilot is dormant.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Clear failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  // Per-provider hint shown under the key field — explains where to
  // get the token + what shape it has, so users don't paste the wrong
  // thing.
  const keyHint = provider === 'github' ? (
    <>
      Paste a GitHub OAuth token with Copilot access (starts with{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>ghu_</code>).
      You can copy it from{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>~/.config/github-copilot/hosts.json</code>{' '}
      or run your own OAuth flow. Coremetry exchanges it for a Copilot session token automatically.
    </>
  ) : provider === 'openai' ? (
    <>
      Drives any OpenAI-compatible <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>/v1/chat/completions</code> endpoint —
      real OpenAI, Ollama, LM Studio, vLLM, llama.cpp server, LocalAI, OpenWebUI.
      Set <b>Base URL</b> below to your endpoint (e.g. <code>http://ollama:11434/v1</code>).
      API key is optional for local endpoints that don't gate on it (Ollama default).
    </>
  ) : (
    <>
      Paste your Anthropic API key (starts with{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>sk-ant-</code>).
      Get one at{' '}
      <a href="https://console.anthropic.com/settings/keys" target="_blank" rel="noopener"
         style={{ color: 'var(--accent2)' }}>console.anthropic.com</a>.
    </>
  );

  const modelPlaceholder =
    provider === 'github' ? 'gpt-4o (default)' :
    provider === 'openai' ? 'gpt-4o-mini / llama3.1 / qwen2.5-coder …' :
    'claude-sonnet-4-6 (default)';

  const providerLabel =
    provider === 'github' ? 'GitHub Copilot' :
    provider === 'openai' ? 'OpenAI-compatible' :
    'Anthropic';

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>AI Copilot</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Inline natural-language explanations for traces, Problems and exceptions.
        Pick a provider, paste your key, save — buttons appear automatically on the
        trace detail page and the Problems table. The OpenAI-compatible provider
        targets self-hosted local LLMs (Ollama / LM Studio / vLLM …) so traces
        never leave your perimeter.
      </p>

      <div className={`status-banner status-banner-${hasKey || (provider === 'openai' && baseUrl) ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${hasKey || (provider === 'openai' && baseUrl) ? 'operational' : 'degraded'}`}>
          {hasKey || (provider === 'openai' && baseUrl) ? 'CONFIGURED' : 'NOT CONFIGURED'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {hasKey
            ? `Provider: ${providerLabel} — ready.`
            : provider === 'openai' && baseUrl
              ? `Provider: ${providerLabel} (no auth) — ready at ${baseUrl}.`
              : 'Not configured. Paste a key (or set a local endpoint URL) below.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Provider</div>
          <select value={provider}
                  onChange={e => setProvider(e.target.value as AIProvider)}
                  style={{ width: '100%' }}>
            <option value="anthropic">Anthropic (Claude)</option>
            <option value="github">GitHub Copilot</option>
            <option value="openai">OpenAI-compatible (Ollama / LM Studio / vLLM / OpenAI)</option>
          </select>
        </label>

        {/* Base URL — only meaningful for the openai provider. The
            field is rendered for all providers but the openai branch
            is the only one that consumes it server-side; harmless
            otherwise (saved + ignored). Keeps the form layout
            stable when switching providers. */}
        {provider === 'openai' && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              Base URL
            </div>
            <input value={baseUrl} onChange={e => setBaseUrl(e.target.value)}
                   placeholder="http://ollama:11434/v1   (or https://api.openai.com/v1 for real OpenAI)"
                   autoComplete="off" style={{ width: '100%', fontFamily: 'monospace' }} />
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
              Endpoint must serve <code>/chat/completions</code> in OpenAI's request shape.
              Common paths: Ollama → <code>http://&lt;host&gt;:11434/v1</code>,
              LM Studio → <code>http://&lt;host&gt;:1234/v1</code>,
              vLLM → <code>http://&lt;host&gt;:8000/v1</code>.
            </div>
          </label>
        )}

        <label style={{ display: 'block', marginBottom: 6 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            API key {hasKey && <span style={{ color: 'var(--text3)' }}>(saved — leave empty to keep current)</span>}
            {provider === 'openai' && (
              <span style={{ color: 'var(--text3)' }}> (optional for local endpoints)</span>
            )}
          </div>
          <input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)}
                 placeholder={hasKey ? '••••••••••••••••' :
                   provider === 'github' ? 'ghu_…' :
                   provider === 'openai' ? 'sk-… (optional)' : 'sk-ant-…'}
                 autoComplete="off" style={{ width: '100%' }} />
        </label>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14, lineHeight: 1.5 }}>
          {keyHint}
        </div>

        <label style={{ display: 'block', marginBottom: 14 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Model (optional)</div>
          <input value={model} onChange={e => setModel(e.target.value)}
                 placeholder={modelPlaceholder} style={{ width: '100%' }} />
        </label>

        {msg && (
          <div style={{
            marginBottom: 12, padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <button type="submit" disabled={busy || (!apiKey && !hasKey)}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          {hasKey && (
            <button type="button" className="sec" onClick={clearKey} disabled={busy}
                    style={{ color: 'var(--err)' }}>
              Remove key
            </button>
          )}
        </div>
      </form>

      {hasKey && (
        <div style={{ marginTop: 18, padding: 16, borderRadius: 8,
          background: 'var(--bg2)', border: '1px solid var(--border)' }}>
          <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 8 }}>What it does</h3>
          <ul style={{ fontSize: 13, lineHeight: 1.7, color: 'var(--text)', paddingLeft: 18 }}>
            <li><b><IconSparkles /> Explain this trace</b> — on any trace detail page.</li>
            <li><b><IconSparkles /></b> column on the <a href="/problems" style={{ color: 'var(--accent2)' }}>Problems</a> page —
              plain-language meaning + ranked likely causes + first three things to check.</li>
          </ul>
        </div>
      )}
    </div>
  );
}

// LDAPTab — enterprise auth configuration. Three sections:
//   1. Connection — host/port/TLS/bind credentials (Test button verifies
//      the service-account bind without saving).
//   2. Group → Role mapping — admin lists AD groups whose members
//      should land as admin / editor / viewer in Coremetry. First
//      match wins (admin > editor > viewer).
//   3. Directory user picker — search the LDAP for a user, pick a
//      role, post to /api/users/from-ldap to pre-provision them so
//      they show up in /users with explicit role even before first
//      sign-in.
//
// Bind password is never echoed back from the API; the GET endpoint
// returns the literal "__SET__" sentinel when one is saved, which we
// translate into a "leave empty to keep current" affordance.
function LDAPTab() {
  const [cfg, setCfg] = useState<LDAPConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  // Track whether the bind password input has been touched. We wipe
  // the "__SET__" sentinel on first focus so the user types a fresh
  // value into an empty box, not on top of the placeholder.
  const [pwTouched, setPwTouched] = useState(false);

  useEffect(() => {
    api.getLDAPSettings().then(c => setCfg(c)).catch(() => setCfg(emptyLDAP()));
  }, []);

  if (!cfg) return <Spinner />;

  const update = (patch: Partial<LDAPConfig>) => setCfg({ ...cfg, ...patch });

  // Strip the "__SET__" sentinel before any outbound call. Server
  // treats empty BindPassword as "preserve saved one".
  const outbound = (): LDAPConfig => {
    const c = { ...cfg };
    if (!pwTouched && c.bindPassword === '__SET__') c.bindPassword = '';
    return c;
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putLDAPSettings(outbound());
      setCfg(next);
      setPwTouched(false);
      setMsg({ kind: 'ok', text: 'Saved.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    setBusy(true); setMsg(null);
    try {
      const r = await api.testLDAPConnection(outbound());
      if (r.ok) setMsg({ kind: 'ok', text: 'Connection OK — bind succeeded.' });
      else setMsg({ kind: 'err', text: r.error || 'Connection failed' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Test failed' });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ maxWidth: 800 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>LDAP / Active Directory</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Authenticate users against your corporate directory. Domain users sign in
        with their normal credentials; their Coremetry role is resolved from AD
        group membership via the mapping table below. Local accounts (bootstrap
        admin) keep working as a fallback.
      </p>

      <form onSubmit={save} style={{
        padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        {/* ── Section 1: Enable + connection ─────────────────────────── */}
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
          <input type="checkbox" checked={cfg.enabled}
                 onChange={e => update({ enabled: e.target.checked })} />
          <span style={{ fontSize: 13, fontWeight: 600 }}>Enable LDAP authentication</span>
        </label>

        <SectionTitle>Connection</SectionTitle>
        <Row>
          <Field2 label="Host" hint="e.g. ldap.corp.example.com">
            <input value={cfg.host} onChange={e => update({ host: e.target.value })}
                   placeholder="ldap.example.com" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Port" hint="636 for LDAPS, 389 for plain/StartTLS" small>
            <input type="number" value={cfg.port || ''}
                   onChange={e => update({ port: parseInt(e.target.value, 10) || 0 })}
                   placeholder="636" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.useTLS}
                   onChange={e => update({ useTLS: e.target.checked, startTLS: e.target.checked ? false : cfg.startTLS })} />
            Use LDAPS (direct TLS, default port 636)
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.startTLS} disabled={cfg.useTLS}
                   onChange={e => update({ startTLS: e.target.checked })} />
            StartTLS upgrade (legacy, port 389)
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.skipVerify}
                   onChange={e => update({ skipVerify: e.target.checked })} />
            Skip cert verification (dev only)
          </label>
        </Row>
        <Field2 label="Internal CA certificate (PEM, optional)"
                hint="Paste the PEM bundle if your AD uses an internal CA. Leave empty to use system roots.">
          <textarea value={cfg.caCert || ''} onChange={e => update({ caCert: e.target.value })}
                    rows={3}
                    style={{ width: '100%', fontFamily: 'monospace', fontSize: 11 }}
                    placeholder="-----BEGIN CERTIFICATE-----..." />
        </Field2>

        <Row>
          <Field2 label="Bind DN (service account)" hint="Used to look up users + groups">
            <input value={cfg.bindDN} onChange={e => update({ bindDN: e.target.value })}
                   placeholder="CN=svc-coremetry,OU=Service Accounts,DC=corp,DC=example"
                   style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="Bind password"
                  hint={cfg.bindPassword === '__SET__' && !pwTouched
                    ? 'A password is saved. Leave empty to keep it; type a new one to rotate.'
                    : 'Service-account password.'}>
            <input type="password"
                   value={pwTouched ? cfg.bindPassword : (cfg.bindPassword === '__SET__' ? '' : cfg.bindPassword)}
                   onFocus={() => { if (!pwTouched && cfg.bindPassword === '__SET__') { setPwTouched(true); update({ bindPassword: '' }); } }}
                   onChange={e => { setPwTouched(true); update({ bindPassword: e.target.value }); }}
                   placeholder={cfg.bindPassword === '__SET__' && !pwTouched ? '••••••••••••' : ''}
                   autoComplete="off" style={{ width: '100%' }} />
          </Field2>
        </Row>

        {/* ── Section 2: Search ──────────────────────────────────────── */}
        <SectionTitle>User search</SectionTitle>
        <Row>
          <Field2 label="Base DN" hint="Where to search for users">
            <input value={cfg.baseDN} onChange={e => update({ baseDN: e.target.value })}
                   placeholder="OU=Users,DC=corp,DC=example" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="User search filter" hint="{{username}} is replaced at runtime">
            <input value={cfg.userSearchFilter}
                   onChange={e => update({ userSearchFilter: e.target.value })}
                   placeholder="(sAMAccountName={{username}})" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="User attribute" small>
            <input value={cfg.userAttribute} onChange={e => update({ userAttribute: e.target.value })}
                   placeholder="sAMAccountName" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Email attribute" small>
            <input value={cfg.emailAttribute} onChange={e => update({ emailAttribute: e.target.value })}
                   placeholder="mail" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Display attribute" small>
            <input value={cfg.displayAttribute} onChange={e => update({ displayAttribute: e.target.value })}
                   placeholder="displayName" style={{ width: '100%' }} />
          </Field2>
        </Row>

        {/* ── Section 3: Groups ──────────────────────────────────────── */}
        <SectionTitle>Group lookup (optional)</SectionTitle>
        <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          Some directories don't populate <code>memberOf</code> on user entries.
          Set a group base + filter to do a separate group search; otherwise leave blank.
        </p>
        <Row>
          <Field2 label="Group search base">
            <input value={cfg.groupSearchBase}
                   onChange={e => update({ groupSearchBase: e.target.value })}
                   placeholder="OU=Groups,DC=corp,DC=example" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="Group filter" hint="{{userDN}} is replaced at runtime">
            <input value={cfg.groupFilter} onChange={e => update({ groupFilter: e.target.value })}
                   placeholder="(member={{userDN}})" style={{ width: '100%' }} />
          </Field2>
        </Row>

        {/* ── Section 4: Role mapping ────────────────────────────────── */}
        <SectionTitle>Group → role mapping</SectionTitle>
        <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          When a user signs in, we walk their group memberships and pick the
          highest-privilege match. Group string can be a full DN or a CN
          fragment — match is case-insensitive substring.
        </p>
        <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse', marginBottom: 8 }}>
          <thead>
            <tr style={{ background: 'var(--bg)', color: 'var(--text2)' }}>
              <th style={{ padding: 6, textAlign: 'left' }}>Group (DN or CN substring)</th>
              <th style={{ padding: 6, textAlign: 'left', width: 160 }}>Role</th>
              <th style={{ padding: 6, width: 32 }}></th>
            </tr>
          </thead>
          <tbody>
            {(cfg.groupRoleMap || []).map((m, i) => (
              <tr key={i}>
                <td style={{ padding: 4 }}>
                  <input value={m.group}
                         onChange={e => updateMapping(cfg, setCfg, i, { group: e.target.value })}
                         placeholder="CN=Coremetry-Admins,OU=Groups,DC=corp,DC=example"
                         style={{ width: '100%' }} />
                </td>
                <td style={{ padding: 4 }}>
                  <select value={m.role}
                          onChange={e => updateMapping(cfg, setCfg, i, { role: e.target.value as Role })}>
                    <option value="admin">admin</option>
                    <option value="editor">editor</option>
                    <option value="viewer">viewer</option>
                  </select>
                </td>
                <td style={{ padding: 4, textAlign: 'center' }}>
                  <button type="button" className="sec"
                          onClick={() => removeMapping(cfg, setCfg, i)}
                          style={{ padding: '2px 8px', color: 'var(--err)' }}>×</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <button type="button" className="sec"
                onClick={() => addMapping(cfg, setCfg)}
                style={{ padding: '4px 10px', fontSize: 12 }}>
          + Add mapping
        </button>

        <Row>
          <Field2 label="Default role" hint="Applied when no group matches">
            <select value={cfg.defaultRole}
                    onChange={e => update({ defaultRole: e.target.value as Role })}
                    style={{ width: '100%' }}>
              <option value="viewer">viewer</option>
              <option value="editor">editor</option>
              <option value="admin">admin</option>
            </select>
          </Field2>
        </Row>

        {msg && (
          <div style={{
            margin: '14px 0 12px', padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
          <button type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          <button type="button" className="sec" disabled={busy} onClick={test}>
            Test connection
          </button>
        </div>
      </form>

      {cfg.enabled && (
        <LDAPUserPicker />
      )}
    </div>
  );
}

function emptyLDAP(): LDAPConfig {
  return {
    enabled: false, host: '', port: 636, useTLS: true, startTLS: false,
    skipVerify: false, caCert: '',
    bindDN: '', bindPassword: '', baseDN: '',
    userSearchFilter: '(sAMAccountName={{username}})',
    userAttribute: 'sAMAccountName', emailAttribute: 'mail',
    displayAttribute: 'displayName',
    groupSearchBase: '', groupFilter: '(member={{userDN}})',
    defaultRole: 'viewer', groupRoleMap: [],
  };
}

function updateMapping(
  cfg: LDAPConfig, set: (c: LDAPConfig) => void,
  i: number, patch: Partial<LDAPGroupRoleMapping>,
) {
  const next = [...cfg.groupRoleMap];
  next[i] = { ...next[i], ...patch };
  set({ ...cfg, groupRoleMap: next });
}
function addMapping(cfg: LDAPConfig, set: (c: LDAPConfig) => void) {
  set({ ...cfg, groupRoleMap: [...cfg.groupRoleMap, { group: '', role: 'viewer' }] });
}
function removeMapping(cfg: LDAPConfig, set: (c: LDAPConfig) => void, i: number) {
  set({ ...cfg, groupRoleMap: cfg.groupRoleMap.filter((_, j) => j !== i) });
}

// LDAPUserPicker — admin types a name/email, hits Search, picks a
// directory entry, picks a role, hits Provision. Pre-creates the
// users row so first-login lands them with the right access without
// having to wait for the group mapping to apply.
function LDAPUserPicker() {
  const [q, setQ] = useState('');
  const [results, setResults] = useState<LDAPDirectoryUser[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [provisionFor, setProvisionFor] = useState<LDAPDirectoryUser | null>(null);
  const [role, setRole] = useState<Role>('viewer');
  const [provisionMsg, setProvisionMsg] = useState<string | null>(null);

  const search = async (e?: FormEvent) => {
    if (e) e.preventDefault();
    setBusy(true); setError(null); setResults(null);
    try {
      const r = await api.searchLDAPUsers(q, 25);
      setResults(r.users ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Search failed');
    } finally {
      setBusy(false);
    }
  };

  const provision = async () => {
    if (!provisionFor) return;
    const email = provisionFor.email || provisionFor.username;
    if (!email) return;
    setBusy(true); setProvisionMsg(null);
    try {
      await api.provisionLDAPUser(email, role);
      setProvisionMsg(`Provisioned ${email} as ${role}.`);
      setProvisionFor(null);
    } catch (err) {
      setProvisionMsg(err instanceof Error ? err.message : 'Provision failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{
      marginTop: 18, padding: 16, borderRadius: 8,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 6 }}>Pre-provision a directory user</h3>
      <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 12 }}>
        Find a user in your LDAP, pick a role, and we'll create their Coremetry
        row up-front. They keep that role even if their AD groups would map them
        to a different one — useful for "trust this person specifically" cases.
      </p>
      <form onSubmit={search} style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
        <input value={q} onChange={e => setQ(e.target.value)}
               placeholder="Name, email or username" style={{ flex: 1 }} />
        <button type="submit" disabled={busy}>{busy ? 'Searching…' : 'Search'}</button>
      </form>
      {error && (
        <div style={{ color: 'var(--err)', fontSize: 12, marginBottom: 8 }}>{error}</div>
      )}
      {results && results.length === 0 && (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>No matches.</div>
      )}
      {results && results.length > 0 && (
        <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ background: 'var(--bg)', color: 'var(--text2)' }}>
              <th style={{ padding: 6, textAlign: 'left' }}>Name</th>
              <th style={{ padding: 6, textAlign: 'left' }}>Username</th>
              <th style={{ padding: 6, textAlign: 'left' }}>Email</th>
              <th style={{ padding: 6, textAlign: 'right', width: 100 }}></th>
            </tr>
          </thead>
          <tbody>
            {results.map(u => (
              <tr key={u.dn} style={{ borderTop: '1px solid var(--border)' }}>
                <td style={{ padding: 6 }}>{u.displayName || '—'}</td>
                <td style={{ padding: 6 }}><code>{u.username}</code></td>
                <td style={{ padding: 6 }}>{u.email || '—'}</td>
                <td style={{ padding: 6, textAlign: 'right' }}>
                  <button className="sec" type="button"
                          onClick={() => setProvisionFor(u)}
                          style={{ padding: '2px 8px', fontSize: 11 }}>
                    Provision
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {provisionFor && (
        <div onClick={() => setProvisionFor(null)} style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
          display: 'grid', placeItems: 'center', zIndex: 100,
        }}>
          <div onClick={e => e.stopPropagation()} style={{
            width: 380, padding: 20, borderRadius: 8,
            background: 'var(--bg2)', border: '1px solid var(--border)',
          }}>
            <div style={{ fontWeight: 600, marginBottom: 10 }}>Provision LDAP user</div>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 12 }}>
              <div>{provisionFor.displayName || provisionFor.username}</div>
              <div style={{ color: 'var(--text3)' }}>{provisionFor.email || provisionFor.dn}</div>
            </div>
            <label style={{ display: 'block', marginBottom: 14 }}>
              <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Role</div>
              <select value={role} onChange={e => setRole(e.target.value as Role)}
                      style={{ width: '100%' }}>
                <option value="viewer">Viewer (read only)</option>
                <option value="editor">Editor (dashboards / monitors / alerts)</option>
                <option value="admin">Admin (full access)</option>
              </select>
            </label>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
              <button type="button" className="sec" onClick={() => setProvisionFor(null)}>Cancel</button>
              <button type="button" onClick={provision} disabled={busy}>
                {busy ? 'Saving…' : 'Provision'}
              </button>
            </div>
          </div>
        </div>
      )}
      {provisionMsg && (
        <div style={{ marginTop: 10, fontSize: 12, color: 'var(--ok)' }}>{provisionMsg}</div>
      )}
    </div>
  );
}

// ── shared form atoms (LDAP tab only — keep here so they don't drift
//    away from their consumer).
function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      marginTop: 16, marginBottom: 8,
      fontSize: 12, fontWeight: 600, color: 'var(--text2)',
      textTransform: 'uppercase', letterSpacing: '0.5px',
    }}>{children}</div>
  );
}
function LDAPRow({ children }: { children: React.ReactNode }) {
  return <div style={{ display: 'flex', gap: 12, marginBottom: 10, flexWrap: 'wrap' }}>{children}</div>;
}
function Field2({ label, hint, small, children }: {
  label: string; hint?: string; small?: boolean; children: React.ReactNode;
}) {
  return (
    <div style={{ flex: small ? '0 1 180px' : 1, minWidth: 200 }}>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.4 }}>{hint}</div>}
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

// ── Sampling tab ────────────────────────────────────────────────────────────
//
// Hot-path policy editor. Default ratio applies to every service
// that doesn't have its own override; per-service rows let the
// admin pin a heavy service to 0.05 (drop 95%) while a low-volume
// one runs at 1.0. AlwaysKeep* are big-stick defaults — turning
// them off trades observability for raw storage savings, almost
// never worth it.
function SamplingTab() {
  const [s, setS] = useState<import('@/lib/types').SamplingSettings | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [newSvc, setNewSvc] = useState('');
  const [newRatio, setNewRatio] = useState('1');

  useEffect(() => {
    api.getSampling().then(d => setS(d ?? null)).catch(() => setS(null));
  }, []);

  if (s === undefined) return <Spinner />;
  if (s === null) {
    return <Empty icon="!" title="Failed to load sampling settings">
      Check that the backend is up and you have admin access.
    </Empty>;
  }

  const save = async () => {
    setBusy(true);
    try {
      const next = await api.putSampling({
        default:          s.default,
        services:         s.services,
        alwaysKeepErrors: s.alwaysKeepErrors,
        alwaysKeepRoots:  s.alwaysKeepRoots,
        tail:             s.tail,
      });
      setS(next);
    } catch (err) { alert(humanize(err)); }
    finally { setBusy(false); }
  };

  const updateTail = (partial: Partial<NonNullable<typeof s.tail>>) => {
    const cur = s.tail ?? { enabled: false, windowSec: 30, slowMs: 1000, maxTraces: 200_000 };
    setS({ ...s, tail: { ...cur, ...partial } });
  };

  const addOverride = () => {
    const r = parseFloat(newRatio);
    if (!newSvc.trim() || isNaN(r) || r < 0 || r > 1) return;
    setS({ ...s, services: { ...s.services, [newSvc.trim()]: r } });
    setNewSvc(''); setNewRatio('1');
  };
  const removeOverride = (svc: string) => {
    const next = { ...s.services };
    delete next[svc];
    setS({ ...s, services: next });
  };

  return (
    <div style={{ maxWidth: 720 }}>
      <h3 style={{ marginTop: 0 }}>Trace sampling</h3>
      <p style={{ color: 'var(--text2)', fontSize: 12 }}>
        Head-sampling rules applied at OTLP ingest. Errors and root spans are
        always kept (toggle below). Probabilistic ratio applies to the rest;
        same trace_id always gets the same decision so partial traces don't
        leak through.
      </p>

      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12,
        display: 'grid', gridTemplateColumns: '180px 1fr', gap: '10px 14px',
        alignItems: 'center', fontSize: 13,
      }}>
        <label>Default ratio</label>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <input type="number" min={0} max={1} step={0.01}
                 value={s.default}
                 onChange={e => setS({ ...s, default: parseFloat(e.target.value) || 0 })}
                 style={{ width: 100 }} />
          <span style={{ color: 'var(--text3)', fontSize: 11 }}>
            (0 = drop all probabilistic spans · 1 = keep everything)
          </span>
        </div>

        <label>Always keep errors</label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={s.alwaysKeepErrors}
                 onChange={e => setS({ ...s, alwaysKeepErrors: e.target.checked })} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            status_code = ERROR spans bypass the ratio
          </span>
        </label>

        <label>Always keep roots</label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={s.alwaysKeepRoots}
                 onChange={e => setS({ ...s, alwaysKeepRoots: e.target.checked })} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            parent_span_id == "" spans bypass the ratio (preserves RPS counts)
          </span>
        </label>
      </div>

      <h4 style={{ marginBottom: 8 }}>Per-service overrides</h4>
      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12,
      }}>
        {Object.keys(s.services).length === 0 && (
          <div style={{ color: 'var(--text3)', fontSize: 12, marginBottom: 8 }}>
            No overrides — every service uses the default ratio above.
          </div>
        )}
        {Object.entries(s.services).map(([svc, r]) => (
          <div key={svc} style={{
            display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6,
          }}>
            <code style={{ flex: 1, fontSize: 12 }}>{svc}</code>
            <input type="number" min={0} max={1} step={0.01}
                   value={r}
                   onChange={e => setS({
                     ...s,
                     services: { ...s.services, [svc]: parseFloat(e.target.value) || 0 },
                   })}
                   style={{ width: 80 }} />
            <button className="sec" style={{ padding: '2px 8px', fontSize: 11 }}
                    onClick={() => removeOverride(svc)}>Remove</button>
          </div>
        ))}

        <div style={{
          display: 'flex', alignItems: 'center', gap: 8,
          borderTop: '1px solid var(--border)', paddingTop: 10, marginTop: 8,
        }}>
          <input value={newSvc} onChange={e => setNewSvc(e.target.value)}
                 placeholder="service-name" style={{ flex: 1 }} />
          <input type="number" min={0} max={1} step={0.01}
                 value={newRatio} onChange={e => setNewRatio(e.target.value)}
                 style={{ width: 80 }} />
          <button className="sec" style={{ padding: '4px 10px', fontSize: 12 }}
                  onClick={addOverride}>Add</button>
        </div>
      </div>

      <h4 style={{ marginBottom: 8 }}>Tail sampling (buffered)</h4>
      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12, fontSize: 13,
      }}>
        <p style={{ color: 'var(--text2)', fontSize: 12, marginTop: 0 }}>
          Buffers each trace for the decision window, then keeps it if any
          span had an error, the root duration exceeded the slow-trace
          threshold, or it falls under the probabilistic ratio. Late-
          arriving spans of decided traces follow the prior verdict.
        </p>
        <div style={{
          display: 'grid', gridTemplateColumns: '180px 1fr', gap: '10px 14px',
          alignItems: 'center',
        }}>
          <label>Enabled</label>
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <input type="checkbox" checked={s.tail?.enabled ?? false}
                   onChange={e => updateTail({ enabled: e.target.checked })} />
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              when on, head ratios are bypassed for traces — tail decides instead
            </span>
          </label>

          <label>Decision window</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={5} max={300}
                   value={s.tail?.windowSec ?? 30}
                   onChange={e => updateTail({ windowSec: parseInt(e.target.value) || 30 })}
                   style={{ width: 80 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>seconds (default 30)</span>
          </div>

          <label>Slow trace threshold</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={50} max={60000}
                   value={s.tail?.slowMs ?? 1000}
                   onChange={e => updateTail({ slowMs: parseInt(e.target.value) || 1000 })}
                   style={{ width: 80 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>
              ms (root duration above this = always keep)
            </span>
          </div>

          <label>Max in-flight traces</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={1000} max={1_000_000} step={1000}
                   value={s.tail?.maxTraces ?? 200_000}
                   onChange={e => updateTail({ maxTraces: parseInt(e.target.value) || 200_000 })}
                   style={{ width: 100 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>
              memory bound (~5 spans × 500 B per trace)
            </span>
          </div>
        </div>
        {s.tailStats && s.tailStats.enabled && (
          <div style={{
            marginTop: 10, padding: 8,
            background: 'var(--bg1)', borderRadius: 4,
            fontFamily: 'ui-monospace, monospace', fontSize: 11, color: 'var(--text3)',
          }}>
            open: {s.tailStats.openTraces.toLocaleString()} traces ·
            flushed: {s.tailStats.flushedSpans.toLocaleString()} spans ·
            dropped: {s.tailStats.droppedSpans.toLocaleString()} spans ·
            evicted: {s.tailStats.evictedTraces.toLocaleString()} traces
          </div>
        )}
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <button onClick={save} disabled={busy}>
          {busy ? 'Saving…' : 'Save & apply'}
        </button>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          Head-stage drops since boot: <b>{s.droppedSinceBoot.toLocaleString()}</b>
        </span>
      </div>
    </div>
  );
}
