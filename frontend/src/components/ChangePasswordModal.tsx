'use client';
import { useState, FormEvent } from 'react';
import { api } from '@/lib/api';

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

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 360, padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        boxShadow: '0 12px 36px rgba(0,0,0,0.3)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>
          Change password
        </div>

        {done ? (
          <div style={{ color: 'var(--ok)', fontSize: 13 }}>
            ✓ Password updated.
          </div>
        ) : (
          <form onSubmit={submit}>
            <Field label="Current password">
              <input type="password" required autoFocus value={current}
                onChange={e => setCurrent(e.target.value)} style={{ width: '100%' }} />
            </Field>
            <Field label="New password (min 6 chars)">
              <input type="password" required minLength={6} value={next}
                onChange={e => setNext(e.target.value)} style={{ width: '100%' }} />
            </Field>
            <Field label="Confirm new password">
              <input type="password" required minLength={6} value={confirmPw}
                onChange={e => setConfirmPw(e.target.value)} style={{ width: '100%' }} />
            </Field>
            {error && (
              <div style={{
                color: 'var(--err)', fontSize: 12, marginTop: 6,
                padding: '6px 10px', background: 'rgba(220,38,38,0.08)',
                border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
              }}>
                {error}
              </div>
            )}
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 18 }}>
              <button type="button" className="sec" onClick={onClose}>Cancel</button>
              <button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Update'}</button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 12 }}>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}
