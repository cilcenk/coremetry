import { useEffect, useState } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { NotificationChannel } from '@/lib/types';
import { IconBell } from '@/components/icons';
import { FlashBox, humanize } from './shared';
import { ChannelModal } from './ChannelModal';

// ── Channels tab ────────────────────────────────────────────────────────────

export function ChannelsTab() {
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
        <Button variant="primary" onClick={() => setEditing('new')} style={{ marginLeft: 'auto' }}>+ New channel</Button>
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
                    <Button variant="secondary" size="sm" onClick={() => onTest(c)} style={{ marginRight: 6 }}>Test</Button>
                    <Button variant="secondary" size="sm" onClick={() => setEditing(c)} style={{ marginRight: 6 }}>Edit</Button>
                    <Button variant="danger" size="sm" onClick={() => onDelete(c)}>Delete</Button>
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
  if (c.type === 'teams') return c.config.webhookUrl ?? '(no webhook)';
  if (c.type === 'zoomchat') {
    // New OAuth-shape channels read the chat channel JID; legacy
    // ones still carry the webhook URL — show whichever exists so
    // the operator can spot which channels still need migration.
    // Proxy hosts get appended in parens so the list view shows
    // a non-default routing target without expanding the row.
    const proxy = c.config.apiBaseUrl ? ` via ${c.config.apiBaseUrl}` : '';
    if (c.config.channelId) return `channel: ${c.config.channelId}${proxy}`;
    if (c.config.toContact) return `DM: ${c.config.toContact}${proxy}`;
    if (c.config.webhookUrl) return '⚠ legacy webhook — please reconfigure';
    return '(not configured)';
  }
  if (c.type === 'webhook') return c.config.url ?? '(no url)';
  if (c.type === 'whatsapp') return (c.config.to ?? []).join(', ') || '(no recipients)';
  return '';
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}
