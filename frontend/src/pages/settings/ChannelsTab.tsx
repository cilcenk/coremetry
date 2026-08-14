import { useEffect, useState } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button, useConfirm } from '@/components/ui';
import { api } from '@/lib/api';
import type { NotificationChannel } from '@/lib/types';
import { IconBell } from '@/components/icons';
import { FlashBox, humanize } from './shared';
import { ChannelModal } from './ChannelModal';
import { QueryError } from '@/components/QueryError';
import { readState } from '@/lib/readState';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// v0.9.875 (tutarlılık denetimi BT16) — bildirim kanalları paylaşılan
// primitife. Severity sıralaması SEVİYEYE göre (rozet sırası), alfabetik
// değil: "en gürültülü kanal hangisi" sorusu böyle cevaplanıyor.
const SEVERITY_RANK: Record<string, number> = { critical: 4, error: 3, warning: 2, info: 1 };

const CHANNEL_COLS: DataTableColumn<NotificationChannel>[] = [
  { id: 'name',   label: 'Name',                 sortValue: c => c.name, naturalDir: 'asc', width: 190 },
  { id: 'type',   label: 'Type',                 sortValue: c => c.type, naturalDir: 'asc', width: 130 },
  { id: 'target', label: 'Recipients / target',  sortValue: c => summarizeChannel(c), naturalDir: 'asc', flex: true },
  { id: 'sev',    label: 'Min severity',         sortValue: c => SEVERITY_RANK[String(c.minSeverity).toLowerCase()] ?? 0, width: 130 },
  { id: 'status', label: 'Status',               sortValue: c => (c.enabled ? 1 : 0), width: 100 },
];

// ── Channels tab ────────────────────────────────────────────────────────────

export function ChannelsTab() {
  const confirm = useConfirm();
  const [items, setItems] = useState<NotificationChannel[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<NotificationChannel | 'new' | null>(null);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const dt = useDataTable<NotificationChannel>({
    storageKey: 'settings-channels', columns: CHANNEL_COLS, rows: items ?? [],
    initialSort: { id: 'name', dir: 'asc' },
  });

  const refresh = () => {
    setItems(undefined);
    api.listChannels().then(d => setItems(d ?? [])).catch(() => setItems(null));
  };
  useEffect(refresh, []);

  const onDelete = async (c: NotificationChannel) => {
    if (!await confirm({
      title: 'Bildirim kanalı silinsin mi?',
      body: <><b>{c.name}</b> kanalı silinecek. Bu kanala yönlendirilmiş alert
        kuralları bildirimlerini SESSİZCE kaybeder.</>,
      confirmLabel: 'Kanalı sil',
      danger: true,
    })) return;
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

      {readState(items) === 'loading' && <Spinner />}
      {/* v0.9.858 (UX denetimi K6) — hata "No channels yet" olarak
          sunuluyordu: operatör var olan kanalların üstüne yeni kanal
          kurmaya yönlendiriliyordu. */}
      {readState(items) === 'error' && (
        <QueryError onRetry={refresh}>
          Channels could not be loaded — this is a failed read, not an empty list.
          Existing channels may still be configured.
        </QueryError>
      )}
      {readState(items) === 'empty' && (
        <Empty icon={<IconBell size={28} />} title="No channels yet">
          Create one to start receiving alert notifications.
        </Empty>
      )}
      {items && items.length > 0 && (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} trailing={[210]} />
            <DataTableHead dt={dt} trailing={<th></th>} />
            <tbody>
              {dt.sortedRows.map(c => (
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
