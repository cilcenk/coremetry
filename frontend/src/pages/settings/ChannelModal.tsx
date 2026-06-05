import { useState, type FormEvent } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { NotificationChannel, ChannelType } from '@/lib/types';
import { Field, Row, FlashBox, humanize } from './shared';
import { ZoomChannelPicker } from './ZoomChannelPicker';

export function ChannelModal({ initial, onClose, onSaved }: {
  initial: NotificationChannel | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [type, setType] = useState<ChannelType>(initial?.type ?? 'email');
  const [recipients, setRecipients] = useState((initial?.config.recipients ?? []).join(', '));
  const [webhookUrl, setWebhookUrl] = useState(initial?.config.webhookUrl ?? '');
  const [url, setUrl] = useState(initial?.config.url ?? '');
  // Zoom Chat Server-to-Server OAuth fields. clientSecret is
  // write-only — the GET endpoint never echoes it back, so the
  // initial value is always empty. Existing channels keep their
  // secret intact unless the operator types a replacement.
  const [zoomAccountId, setZoomAccountId] = useState(initial?.config.accountId ?? '');
  const [zoomClientId, setZoomClientId] = useState(initial?.config.clientId ?? '');
  const [zoomClientSecret, setZoomClientSecret] = useState('');
  const [zoomChannelId, setZoomChannelId] = useState(initial?.config.channelId ?? '');
  const [zoomToContact, setZoomToContact] = useState(initial?.config.toContact ?? '');
  // Optional proxy / sandbox host overrides. Empty → public
  // Zoom defaults (api.zoom.us + zoom.us). Banks routing
  // outbound traffic through a corporate gateway fill these.
  const [zoomAPIBaseURL, setZoomAPIBaseURL] = useState(initial?.config.apiBaseUrl ?? '');
  const [zoomOAuthBaseURL, setZoomOAuthBaseURL] = useState(initial?.config.oauthBaseUrl ?? '');
  // TLS verification toggle — defaults to off (verify enabled).
  // Operators in corp networks where api.zoom.us is fronted by
  // a MITM proxy with a private CA can flip this on as a
  // workaround. Public Zoom traffic should always verify.
  const [zoomSkipVerify, setZoomSkipVerify] = useState(
    initial?.config.insecureSkipVerify ?? false);
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
  const [matchClusters, setMatchClusters] = useState((initial?.matchRules?.clusters ?? []).join(', '));
  const [matchQuietHours, setMatchQuietHours] = useState(initial?.matchRules?.quietHours ?? '');
  const [matchQuietHoursTz, setMatchQuietHoursTz] = useState(initial?.matchRules?.quietHoursTz ?? '');
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
        if (!zoomAccountId.trim()) throw new Error('Zoom Account ID is required');
        if (!zoomClientId.trim()) throw new Error('Zoom Client ID is required');
        // Secret is required on a NEW channel; on edit, leaving it
        // blank means "keep the saved secret" — the server detects
        // that by passing through the existing value.
        if (!initial && !zoomClientSecret.trim()) {
          throw new Error('Zoom Client Secret is required');
        }
        if (!zoomChannelId.trim() && !zoomToContact.trim()) {
          throw new Error('Either a Channel ID or a contact email is required');
        }
        config.accountId = zoomAccountId.trim();
        config.clientId = zoomClientId.trim();
        if (zoomClientSecret.trim()) config.clientSecret = zoomClientSecret.trim();
        if (zoomChannelId.trim()) config.channelId = zoomChannelId.trim();
        if (zoomToContact.trim()) config.toContact = zoomToContact.trim();
        if (zoomAPIBaseURL.trim()) config.apiBaseUrl = zoomAPIBaseURL.trim();
        if (zoomOAuthBaseURL.trim()) config.oauthBaseUrl = zoomOAuthBaseURL.trim();
        if (zoomSkipVerify) config.insecureSkipVerify = true;
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
        services:     splitCSL(matchServices),
        sreTeams:     splitCSL(matchSREs),
        ownerTeams:   splitCSL(matchOwners),
        clusters:     splitCSL(matchClusters),
        quietHours:   matchQuietHours.trim(),
        quietHoursTz: matchQuietHoursTz.trim(),
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
              <div style={{
                fontSize: 11, color: 'var(--text2)', lineHeight: 1.6,
                padding: '8px 10px', borderRadius: 4,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                marginBottom: 8,
              }}>
                Server-to-Server OAuth flow. Create a "Server-to-Server OAuth" app in the
                Zoom App Marketplace with the <code>chat_message:write:admin</code> scope,
                then paste its credentials below. Coremetry exchanges them for an access
                token (~1h cache) and posts to Zoom's REST API.
              </div>
              <Row>
                <Field label="Account ID" flex={1}>
                  <input required value={zoomAccountId}
                    placeholder="ABC1234d-XYZ..."
                    onChange={e => setZoomAccountId(e.target.value)} style={{ width: '100%' }} />
                </Field>
                <Field label="Client ID" flex={1}>
                  <input required value={zoomClientId}
                    placeholder="from the S2S OAuth app"
                    onChange={e => setZoomClientId(e.target.value)} style={{ width: '100%' }} />
                </Field>
              </Row>
              <Field label={initial
                  ? 'Client Secret (leave empty to keep saved value)'
                  : 'Client Secret'}>
                <input required={!initial} value={zoomClientSecret} type="password"
                  placeholder={initial ? '•••••••• (unchanged)' : 'never echoed back after save'}
                  onChange={e => setZoomClientSecret(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Channel ID (JID) — target chat channel">
                <div style={{ display: 'flex', gap: 6 }}>
                  <input value={zoomChannelId}
                    placeholder='e.g. "1234567890abcdef@xmpp.zoom.us" — copy from Zoom channel info'
                    onChange={e => setZoomChannelId(e.target.value)} style={{ flex: 1 }} />
                  <ZoomChannelPicker
                    existingChannelId={initial?.id}
                    accountId={zoomAccountId}
                    clientId={zoomClientId}
                    clientSecret={zoomClientSecret}
                    oauthBaseUrl={zoomOAuthBaseURL}
                    apiBaseUrl={zoomAPIBaseURL}
                    insecureSkipVerify={zoomSkipVerify}
                    onPick={jid => setZoomChannelId(jid)} />
                </div>
              </Field>
              <Field label="Or DM contact email (fallback if Channel ID is empty)">
                <input value={zoomToContact} type="email"
                  placeholder="oncall@example.com"
                  onChange={e => setZoomToContact(e.target.value)} style={{ width: '100%' }} />
              </Field>
              {/* Optional API + OAuth host overrides — proxy /
                  sandbox use cases. Leave empty for public Zoom. */}
              <details style={{ marginTop: 4 }}>
                <summary style={{ cursor: 'pointer', fontSize: 12, color: 'var(--text2)' }}>
                  Advanced: proxy / sandbox host overrides
                </summary>
                <div style={{ paddingTop: 8 }}>
                  <Row>
                    <Field label="API base URL (chat messages)" flex={1}>
                      <input value={zoomAPIBaseURL}
                        placeholder="https://api.zoom.us (default)"
                        onChange={e => setZoomAPIBaseURL(e.target.value)} style={{ width: '100%' }} />
                    </Field>
                    <Field label="OAuth base URL (token exchange)" flex={1}>
                      <input value={zoomOAuthBaseURL}
                        placeholder="https://zoom.us (default)"
                        onChange={e => setZoomOAuthBaseURL(e.target.value)} style={{ width: '100%' }} />
                    </Field>
                  </Row>
                  <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
                    Banks routing outbound traffic through a corporate gateway can point both
                    fields at their proxy. Endpoint paths stay the same
                    (<code>/v2/chat/users/me/messages</code> and <code>/oauth/token</code>) —
                    only the host changes. Leave empty to hit Zoom's public hosts.
                  </div>
                  <label style={{
                    display: 'flex', alignItems: 'flex-start', gap: 8,
                    marginTop: 12, fontSize: 12, cursor: 'pointer',
                  }}>
                    <input type="checkbox"
                      checked={zoomSkipVerify}
                      onChange={e => setZoomSkipVerify(e.target.checked)}
                      style={{ marginTop: 2 }} />
                    <span>
                      <span style={{ fontWeight: 600 }}>Skip TLS certificate verification</span>
                      <span style={{ display: 'block', color: 'var(--text3)', fontSize: 11, marginTop: 2, lineHeight: 1.5 }}>
                        Disables certificate trust checks on the OAuth + chat calls. Turn this on
                        only when the corporate proxy fronting <code>api.zoom.us</code> terminates
                        TLS with a private CA the pod doesn't trust. Equivalent to <code>curl -k</code> —
                        public Zoom hosts should leave it off.
                      </span>
                    </span>
                  </label>
                </div>
              </details>
              {/* Legacy webhook nudge — surfaces when editing a
                  channel that still carries the pre-v0.4.78 shape. */}
              {initial?.config.webhookUrl && !initial?.config.accountId && (
                <div style={{
                  fontSize: 11, color: 'var(--warn)', padding: '6px 10px',
                  borderRadius: 4, background: 'rgba(245,158,11,0.10)',
                  border: '1px solid rgba(245,158,11,0.30)',
                  marginTop: 4,
                }}>
                  ⚠ This channel still uses the legacy webhook URL. Fill the fields
                  above and save to migrate; the webhook URL will be cleared.
                </div>
              )}
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
              <Field label="Match k8s/openshift clusters (comma-separated)">
                <input value={matchClusters}
                  placeholder="prod-eu-west, prod-eu-central"
                  onChange={e => setMatchClusters(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <div style={{ display: 'grid',
                            gridTemplateColumns: '1fr 1fr', gap: 8 }}>
                <Field label="Quiet hours (HH:MM-HH:MM, may cross midnight)">
                  <input value={matchQuietHours}
                    placeholder="22:00-07:00"
                    onChange={e => setMatchQuietHours(e.target.value)}
                    style={{ width: '100%' }} />
                </Field>
                <Field label="Quiet hours timezone (IANA, default UTC)">
                  <input value={matchQuietHoursTz}
                    placeholder="Europe/Istanbul"
                    onChange={e => setMatchQuietHoursTz(e.target.value)}
                    style={{ width: '100%' }} />
                </Field>
              </div>
              <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
                Predicates AND together — every non-empty rule must match.
                e.g. services=<code>payments</code> +
                clusters=<code>prod-eu-west</code> +
                quietHours=<code>22:00-07:00</code> means "fire only on the
                payments service in eu-west, AND only outside the 10pm–7am
                window". Service catalog metadata is the source of truth for
                sreTeam / ownerTeam lookup; clusters come from the problem's
                enriched cluster list (k8s.cluster.name or
                openshift.cluster.name resource attrs).
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
            <Button type="button" variant="secondary" onClick={onClose}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? 'Saving…' : initial ? 'Update' : 'Create'}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
