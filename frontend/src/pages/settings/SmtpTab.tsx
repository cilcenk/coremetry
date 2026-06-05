import { useEffect, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { SMTPSettings } from '@/lib/types';
import { Field, Row, FlashBox, humanize } from './shared';

// ── SMTP tab ────────────────────────────────────────────────────────────────

export function SMTPTab() {
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
        <Button type="submit" variant="primary" disabled={busy}>{busy ? 'Saving…' : 'Save settings'}</Button>
        <div style={{ flex: 1 }} />
        <input type="email" value={testTo} placeholder="recipient@example.com"
          onChange={e => setTestTo(e.target.value)} style={{ width: 240 }} />
        <Button type="button" variant="secondary" onClick={sendTest} disabled={busy || !s.configured}>
          Send test email
        </Button>
      </div>
      {!s.configured && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
          Save valid SMTP settings before testing.
        </div>
      )}
    </form>
  );
}
