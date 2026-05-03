'use client';
import { useEffect, useState, FormEvent } from 'react';
import { useAuth } from '@/components/AuthProvider';
import { api, type AuthConfigResponse } from '@/lib/api';

export default function LoginPage() {
  const { login } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [config, setConfig] = useState<AuthConfigResponse | null>(null);

  // Pull auth config so we know whether to show the SSO button.
  useEffect(() => {
    api.authConfig().then(setConfig).catch(() => setConfig({
      local: { enabled: true }, oidc: { enabled: false },
    }));
  }, []);

  // Surface OIDC failure messages bubbled back via ?error=…
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const params = new URLSearchParams(window.location.search);
    const e = params.get('error');
    if (e) setError(`SSO sign-in failed: ${e}`);
  }, []);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await login(email.trim(), password);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Login failed';
      setError(msg.includes('invalid credentials') || msg.includes('401')
        ? 'Invalid email or password'
        : msg);
    } finally {
      setBusy(false);
    }
  };

  const oidcEnabled = !!config?.oidc.enabled;
  const oidcLabel = config?.oidc.displayName || 'SSO';

  return (
    <div style={{
      position: 'fixed', inset: 0, display: 'grid', placeItems: 'center',
      background: 'var(--bg)',
    }}>
      <form onSubmit={onSubmit} style={{
        width: 340, padding: 32, borderRadius: 10,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        boxShadow: '0 12px 36px rgba(0,0,0,0.25)',
      }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <div style={{ fontSize: 30, color: 'var(--accent)' }}>⬡</div>
          <div style={{ fontSize: 20, fontWeight: 600, marginTop: 4 }}>Qmetry</div>
          <div style={{ color: 'var(--text3)', fontSize: 12, marginTop: 4 }}>
            Sign in to continue
          </div>
        </div>

        {oidcEnabled && (
          <>
            <button type="button"
              onClick={() => { window.location.href = '/api/auth/oidc/start'; }}
              style={{
                width: '100%', padding: '9px 12px', marginBottom: 14,
                background: 'var(--bg)', color: 'var(--text)',
                border: '1px solid var(--border)',
              }}>
              ⚿ Sign in with {oidcLabel}
            </button>
            <div style={{
              display: 'flex', alignItems: 'center', gap: 8,
              color: 'var(--text3)', fontSize: 11, margin: '6px 0 14px',
            }}>
              <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
              <span>or sign in locally</span>
              <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
            </div>
          </>
        )}

        <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
          Email
        </label>
        <input type="email" autoComplete="username" required autoFocus
          value={email} onChange={e => setEmail(e.target.value)}
          style={{ width: '100%', marginBottom: 14 }} />

        <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
          Password
        </label>
        <input type="password" autoComplete="current-password" required
          value={password} onChange={e => setPassword(e.target.value)}
          style={{ width: '100%', marginBottom: 18 }} />

        {error && (
          <div style={{
            color: 'var(--err)', fontSize: 12, marginBottom: 12,
            padding: '6px 10px', background: 'rgba(220,38,38,0.08)',
            border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
          }}>
            {error}
          </div>
        )}

        <button type="submit" disabled={busy}
          style={{ width: '100%', padding: '8px 12px' }}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </div>
  );
}
