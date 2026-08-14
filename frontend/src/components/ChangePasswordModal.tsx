import { useState, FormEvent } from 'react';
import { api } from '@/lib/api';
import { Modal, Field, Button, Stack } from '@/components/ui';

// Refactored to lean on the new ui/ primitives — Modal handles
// focus-trap + ESC + backdrop, Field carries label/hint/error
// wiring, Button surfaces the loading state. Ten lines of
// inline-style chrome went away; only the form logic remains.
export function ChangePasswordModal({ onClose }: { onClose: () => void }) {
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirmPw, setConfirmPw] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (next !== confirmPw) { setError('Passwords do not match'); return; }
    setBusy(true); setError(null);
    try {
      await api.changeOwnPassword(current, next);
      setDone(true);
      setTimeout(onClose, 1200);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      const body = msg.replace(/^HTTP \d+:\s*/, '');
      try {
        const j = JSON.parse(body);
        setError(j?.error ?? body);
      } catch { setError(body); }
    } finally {
      setBusy(false);
    }
  };

  if (done) {
    return (
      <Modal open={true} onClose={onClose} title="Change password" size="sm">
        <div style={{ color: 'var(--ok)', fontSize: 13 }}>
          ✓ Password updated.
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      open={true}
      onClose={onClose}
      title="Change password"
      size="sm"
      initialFocus="input[type=password]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="change-pw-form" loading={busy}>Update</Button>
        </>
      }>
      <form id="change-pw-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field
            label="Current password"
            type="password" required value={current}
            onChange={e => setCurrent(e.target.value)} />
          <Field
            label="New password"
            hint="At least 6 characters."
            type="password" required minLength={6} value={next}
            onChange={e => setNext(e.target.value)} />
          <Field
            label="Confirm new password"
            type="password" required minLength={6} value={confirmPw}
            onChange={e => setConfirmPw(e.target.value)} />
          {error && (
            <div style={{
              color: 'var(--err)', fontSize: 12,
              padding: '6px 10px', background: 'rgba(220,38,38,0.08)',
              border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
            }}>
              {error}
            </div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
