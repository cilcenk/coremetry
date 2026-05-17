import { useEffect, useState, FormEvent } from 'react';
import { useAuth } from '@/components/AuthProvider';
import { TelescopeIcon } from '@/components/TelescopeIcon';
import { ThemeToggle } from '@/components/ThemeToggle';
import { api, type AuthConfigResponse } from '@/lib/api';
import { useBranding } from '@/lib/branding';
import { useT } from '@/lib/i18n';

export default function LoginPage() {
  const { login } = useAuth();
  // Admin-set branding overlay (logo, login title, button label,
  // username field label, footer line). Falls back to the
  // bundled defaults for any field left empty.
  const brand = useBranding();
  const t = useT();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [config, setConfig] = useState<AuthConfigResponse | null>(null);

  // Pull auth config + auto-sign-in when demo mode is on. The demo
  // viewer creds are intentionally public (they're returned by an
  // unauth'd endpoint) so the user lands directly in the app
  // without seeing this form. On non-demo deployments the form
  // renders normally and the operator types their own creds.
  useEffect(() => {
    api.authConfig().then(c => {
      setConfig(c);
      if (c.demo?.enabled && c.demo.email && c.demo.password) {
        setEmail(c.demo.email);
        setPassword(c.demo.password);
        // Auto-submit. Don't block on user interaction.
        setBusy(true);
        login(c.demo.email, c.demo.password).catch((err: unknown) => {
          const msg = err instanceof Error ? err.message : 'Demo login failed';
          setError(msg);
          setBusy(false);
        });
      }
    }).catch(() => setConfig({
      local: { enabled: true }, oidc: { enabled: false },
    }));
  }, [login]);

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
        ? t('login.invalid')
        : msg);
    } finally {
      setBusy(false);
    }
  };

  const oidcEnabled = !!config?.oidc.enabled;
  const oidcLabel = config?.oidc.displayName || 'SSO';
  const demoEnabled = !!config?.demo?.enabled;
  const ldapEnabled = !!config?.ldap?.enabled;

  // Build version is unauthenticated so we can show it before the
  // operator has a session — useful for support / matching a
  // running instance to a release tag.
  const [version, setVersion] = useState<string>('');
  useEffect(() => {
    api.version().then(v => setVersion(v?.version ?? '')).catch(() => {});
  }, []);

  return (
    <div style={{
      position: 'fixed', inset: 0, display: 'grid', placeItems: 'center',
      background: 'var(--bg)',
    }}>
      {/* Theme toggle in the top-right corner — same control the rest of
          the app uses, here so users can flip the theme before signing in. */}
      <div style={{ position: 'fixed', top: 16, right: 16, zIndex: 1 }}>
        <ThemeToggle />
      </div>
      <form onSubmit={onSubmit} style={{
        width: 340, padding: 32, borderRadius: 10,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        boxShadow: '0 12px 36px rgba(0,0,0,0.25)',
      }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          {brand.logoDataUri ? (
            // Two-row layout when an operator logo is uploaded:
            // (1) the custom logo dominates the header, (2) an
            // "OpenTelemetry mark + Coremetry" co-branding row
            // sits underneath on its own line so the OTel
            // provenance + product name stay visible. Revised
            // from v0.5.163's small "powered by" line which felt
            // like an afterthought instead of co-branding.
            <>
              <img src={brand.logoDataUri} alt={brand.appName}
                   style={{ maxHeight: 72, maxWidth: 220, objectFit: 'contain' }} />
              <div style={{
                marginTop: 12,
                display: 'inline-flex', alignItems: 'center', gap: 8,
              }}>
                <TelescopeIcon size={22} />
                <span style={{ fontSize: 22, fontWeight: 700, letterSpacing: '0.5px' }}>
                  Coremetry
                </span>
              </div>
            </>
          ) : (
            // No custom logo: the bundled mark + appName is the
            // entire branding (unchanged from pre-v0.5.163).
            <>
              <div style={{
                display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
                width: 64, height: 64, borderRadius: 16,
                background: 'rgba(66,92,199,0.12)',
                border: '1px solid rgba(66,92,199,0.35)',
              }}>
                <TelescopeIcon size={40} />
              </div>
              <div style={{ fontSize: 22, fontWeight: 700, marginTop: 10, letterSpacing: '0.5px' }}>
                {brand.appName}
              </div>
            </>
          )}
          <div style={{ color: 'var(--text3)', fontSize: 11, marginTop: 4 }}>
            {brand.loginTitle === `Sign in to ${brand.appName}`
              ? t('login.signInToContinue')
              : brand.loginTitle}
          </div>
          {brand.loginSubtitle && (
            <div style={{ color: 'var(--text2)', fontSize: 12, marginTop: 8, lineHeight: 1.5 }}>
              {brand.loginSubtitle}
            </div>
          )}
        </div>

        {demoEnabled && (
          <div style={{
            marginBottom: 14, padding: '8px 12px', borderRadius: 6,
            background: 'rgba(63,185,80,0.10)',
            border: '1px solid rgba(63,185,80,0.35)',
            color: 'var(--text2)', fontSize: 12, lineHeight: 1.4,
          }}>
            <b style={{ color: 'var(--ok)' }}>Demo mode</b> — credentials are pre-filled,
            just hit <i>Sign in</i>. Anyone with this URL has the same access.
          </div>
        )}

        {oidcEnabled && (
          <>
            <button type="button"
              onClick={() => { window.location.href = '/api/auth/oidc/start'; }}
              style={{
                width: '100%', padding: '9px 12px', marginBottom: 14,
                background: 'var(--bg)', color: 'var(--text)',
                border: '1px solid var(--border)',
              }}>
              ⚿ {t('login.signInWith')} {oidcLabel}
            </button>
            <div style={{
              display: 'flex', alignItems: 'center', gap: 8,
              color: 'var(--text3)', fontSize: 11, margin: '6px 0 14px',
            }}>
              <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
              <span>{t('login.orLocal')}</span>
              <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
            </div>
          </>
        )}

        {ldapEnabled && (
          <div style={{
            marginBottom: 14, padding: '6px 10px', borderRadius: 6,
            background: 'rgba(66,92,199,0.08)', border: '1px solid rgba(66,92,199,0.30)',
            color: 'var(--text2)', fontSize: 11, lineHeight: 1.4,
          }}>
            Sign in with your <b style={{ color: 'var(--text)' }}>domain account</b> (username or email).
          </div>
        )}

        <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
          {ldapEnabled
            ? (brand.usernameLabel === 'Email' ? t('login.usernameOrEmail') : brand.usernameLabel)
            : (brand.usernameLabel === 'Email' ? t('login.email') : brand.usernameLabel)}
        </label>
        {/* Always text — `type="email"` triggered HTML5 @-validation
            which blocked LDAP users typing a bare sAMAccountName, and
            before /api/auth/config resolved the flag flipped briefly
            anyway. Backend owns the format check; the input type
            doesn't add safety beyond a soft hint, so we drop it. */}
        <input type="text" autoComplete="username" required autoFocus
          value={email} onChange={e => setEmail(e.target.value)}
          style={{ width: '100%', marginBottom: 14 }} />

        <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
          {t('login.password')}
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
            <div>{error}</div>
            {error === t('login.invalid') && (
              <div style={{
                marginTop: 6, fontSize: 11, fontWeight: 400,
                color: 'var(--text2)',
              }}>
                {t('login.invalidHint')}
              </div>
            )}
          </div>
        )}

        <button type="submit" disabled={busy}
          style={{ width: '100%', padding: '8px 12px' }}>
          {busy
            ? t('login.signingIn')
            : (brand.signInButtonLabel === 'Sign in' ? t('login.signIn') : brand.signInButtonLabel)}
        </button>

        {brand.footerText && (
          <div style={{
            marginTop: 14, textAlign: 'center',
            fontSize: 11, color: 'var(--text2)', lineHeight: 1.5,
          }}>
            {brand.footerText}
          </div>
        )}

        {/* Build version — only rendered once /api/version answers, so
            the form doesn't reflow during initial paint. The
            "· powered by OpenTelemetry" tag is non-removable
            even with custom branding — it's an acknowledgement
            of the open standard Coremetry is built on, not a
            vendor watermark, so the branding override leaves
            it intact. */}
        {version && (
          <div style={{
            marginTop: 18, textAlign: 'center',
            fontSize: 10, color: 'var(--text3)',
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
            letterSpacing: '0.3px',
          }}>
            {brand.appName} {version}
            <span style={{ marginLeft: 6, color: 'var(--text3)' }}>
              · powered by{' '}
              <a href="https://opentelemetry.io"
                 target="_blank" rel="noopener"
                 style={{ color: 'var(--accent2)', textDecoration: 'none' }}>
                OpenTelemetry
              </a>
            </span>
          </div>
        )}
      </form>
    </div>
  );
}
