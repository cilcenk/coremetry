import { useState } from 'react';
import { Button } from '@/components/ui';

// ZoomChannel mirrors the backend ZoomChannel struct.
interface ZoomChannelRow {
  id: string;
  jid: string;
  name: string;
  type?: number;
}

// ZoomChannelPicker — small button next to the Channel ID input
// that fetches every channel the configured S2S OAuth app can
// see and opens a searchable picker. Removes the
// memorise-the-JID requirement: a Zoom workspace can have
// hundreds of channels, so the modal includes an inline search
// box that filters by name / id / JID as the operator types.
// Click a row to inject the JID into the form.
export function ZoomChannelPicker({
  existingChannelId,
  accountId, clientId, clientSecret,
  oauthBaseUrl, apiBaseUrl, insecureSkipVerify, onPick,
}: {
  existingChannelId?: string;
  accountId: string;
  clientId: string;
  clientSecret: string;
  oauthBaseUrl: string;
  apiBaseUrl: string;
  insecureSkipVerify?: boolean;
  onPick: (jid: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [rows, setRows] = useState<ZoomChannelRow[] | null>(null);
  const [search, setSearch] = useState('');

  const canFetch = (
    // For an unsaved channel we need all three credential fields
    // inline; for an existing channel the saved (redacted)
    // secret can be reused server-side via existingChannelId.
    (accountId.trim() && clientId.trim() && clientSecret.trim()) ||
    !!existingChannelId
  );

  const load = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await fetch('/api/channels/zoom/list-channels', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          existingChannelId: existingChannelId,
          accountId: accountId.trim(),
          clientId: clientId.trim(),
          clientSecret: clientSecret.trim(),
          oauthBaseUrl: oauthBaseUrl.trim(),
          apiBaseUrl: apiBaseUrl.trim(),
          insecureSkipVerify: !!insecureSkipVerify,
        }),
      });
      if (!r.ok) {
        const body = await r.text();
        // Backend returns partial channels on truncation. Try to
        // surface those alongside the warning so the operator
        // can still pick from what we got.
        try {
          const parsed = JSON.parse(body);
          if (Array.isArray(parsed.channels) && parsed.channels.length > 0) {
            setRows(parsed.channels);
          }
          setErr(parsed.error ?? `HTTP ${r.status}`);
        } catch {
          setErr(`HTTP ${r.status}: ${body.slice(0, 200)}`);
        }
        return;
      }
      const j = await r.json();
      setRows(j.channels ?? []);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const onOpen = () => {
    setOpen(true);
    if (!rows && !busy) load();
  };

  const filtered = (rows ?? []).filter(r => {
    if (!search.trim()) return true;
    const t = search.toLowerCase();
    return (
      r.name.toLowerCase().includes(t) ||
      r.jid.toLowerCase().includes(t) ||
      r.id.toLowerCase().includes(t)
    );
  });

  const channelType = (t?: number) =>
    t === 1 ? 'DM'
    : t === 2 ? 'Group'
    : t === 3 ? 'Public'
    : t === 4 ? 'Private'
    : '—';

  return (
    <>
      <button type="button" className="sec"
        disabled={!canFetch}
        title={canFetch
          ? 'List channels via the configured S2S OAuth app'
          : 'Enter Account ID / Client ID / Client Secret (or save first), then try again'}
        onClick={onOpen}
        style={{ whiteSpace: 'nowrap', fontSize: 12 }}>
        List my channels…
      </button>
      {open && (
        <div onClick={() => setOpen(false)} style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
          display: 'grid', placeItems: 'center', zIndex: 250,
        }}>
          <div onClick={e => e.stopPropagation()} style={{
            width: 720, maxWidth: '94vw', maxHeight: '82vh',
            display: 'flex', flexDirection: 'column',
            padding: 18, borderRadius: 8,
            background: 'var(--bg2)', border: '1px solid var(--border)',
          }}>
            <div style={{
              display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 10,
            }}>
              <div style={{ fontSize: 14, fontWeight: 700 }}>
                Pick a Zoom channel
              </div>
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                {rows ? `${filtered.length} of ${rows.length}` : ''}
              </span>
              <span style={{ marginLeft: 'auto' }}>
                <Button variant="secondary" size="sm" disabled={busy}
                  onClick={load} style={{ marginRight: 8 }}>
                  Refresh
                </Button>
                <Button variant="secondary" onClick={() => setOpen(false)}>Close</Button>
              </span>
            </div>

            <input value={search} onChange={e => setSearch(e.target.value)}
              placeholder="Filter by name, ID, or JID…"
              autoFocus
              style={{ marginBottom: 10, fontSize: 13 }} />

            {busy && <div style={{ fontSize: 12, color: 'var(--text3)' }}>Loading channels…</div>}
            {err && (
              <div style={{
                fontSize: 11, color: 'var(--err)', padding: '6px 8px',
                borderRadius: 4, marginBottom: 8,
                background: 'rgba(220,38,38,0.08)', border: '1px solid rgba(220,38,38,0.3)',
              }}>{err}</div>
            )}
            {rows && rows.length === 0 && !busy && !err && (
              <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                No channels visible to this S2S app. The bot user must be a
                member of the channel for it to appear here.
              </div>
            )}

            <div style={{ flex: 1, overflowY: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
              <table style={{ width: '100%' }}>
                <thead style={{ position: 'sticky', top: 0, background: 'var(--bg1)', zIndex: 1 }}>
                  <tr>
                    <th style={{ textAlign: 'left' }}>Name</th>
                    <th style={{ textAlign: 'left' }}>Type</th>
                    <th style={{ textAlign: 'left' }}>JID</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map(r => (
                    <tr key={r.id || r.jid}
                      onClick={() => { onPick(r.jid); setOpen(false); }}
                      style={{ cursor: 'pointer' }}
                      onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3)')}
                      onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
                      <td style={{ fontSize: 12, fontWeight: 600 }}>{r.name || '(unnamed)'}</td>
                      <td style={{
                        fontSize: 10, color: 'var(--text3)',
                        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      }}>{channelType(r.type)}</td>
                      <td style={{
                        fontSize: 11, color: 'var(--text2)',
                        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      }}>{r.jid}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 8 }}>
              The bot user behind your S2S OAuth app must be a member of a channel
              for it to appear in this list. If a channel is missing, add the bot
              from the Zoom web UI (channel settings → People → Add) and click
              Refresh.
            </div>
          </div>
        </div>
      )}
    </>
  );
}
