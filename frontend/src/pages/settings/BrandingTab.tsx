import { useEffect, useState, type FormEvent, type ChangeEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { DEFAULT_BRANDING, invalidateBranding, type BrandingSettings } from '@/lib/branding';
import { Field, Row } from './shared';

// BrandingTab — white-label / customisation form. Admin paints the
// login page (logo + title + button label + footer) and the
// browser tab title. Everything is optional; an empty value
// reverts to the bundled Coremetry default. Saved overlay is
// applied immediately via invalidateBranding() so the operator
// doesn't have to reload to see the result.
//
// Logo upload reads the local file as a data URI and caps the
// raw size at 200 KB — big enough for a 200 px PNG, small
// enough that the system_settings row stays cheap to fetch on
// every login page render.
export function BrandingTab() {
  const [loaded, setLoaded] = useState(false);
  const [b, setB] = useState<BrandingSettings>({});
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getBranding()
      .then(v => { setB(v ?? {}); setLoaded(true); })
      .catch(() => setLoaded(true));
  }, []);

  if (!loaded) return <Spinner />;

  const set = (k: keyof BrandingSettings, v: string) =>
    setB(prev => ({ ...prev, [k]: v }));

  const onLogo = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (!f) return;
    if (f.size > 200 * 1024) {
      setMsg({ kind: 'err', text: `Logo file too large (${(f.size / 1024).toFixed(0)} KB) — keep it under 200 KB.` });
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result;
      if (typeof result === 'string') {
        set('logoDataUri', result);
        setMsg(null);
      }
    };
    reader.onerror = () => setMsg({ kind: 'err', text: 'Failed to read file.' });
    reader.readAsDataURL(f);
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      await api.putBranding(b);
      await invalidateBranding();
      setMsg({ kind: 'ok', text: 'Saved — branding applied immediately.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const resetAll = async () => {
    if (!confirm('Reset all branding to the Coremetry defaults? Saved logo + custom strings will be cleared.')) return;
    setBusy(true); setMsg(null);
    try {
      await api.putBranding({});
      setB({});
      await invalidateBranding();
      setMsg({ kind: 'ok', text: 'Reset — Coremetry defaults restored.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Reset failed' });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ maxWidth: 720 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Branding</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        White-label the login page + browser tab title. Empty fields fall back to the
        Coremetry defaults shown as placeholders. Changes apply immediately — no
        restart, no reload needed.
      </p>

      <form onSubmit={save} style={{
        padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <Row>
          <Field label="App name" flex={1}>
            <input value={b.appName ?? ''} onChange={e => set('appName', e.target.value)}
                   placeholder={DEFAULT_BRANDING.appName} style={{ width: '100%' }} />
          </Field>
          <Field label="Browser tab title" flex={1}>
            <input value={b.browserTitle ?? ''} onChange={e => set('browserTitle', e.target.value)}
                   placeholder={DEFAULT_BRANDING.browserTitle} style={{ width: '100%' }} />
          </Field>
        </Row>

        <Field label="Login page title">
          <input value={b.loginTitle ?? ''} onChange={e => set('loginTitle', e.target.value)}
                 placeholder="Sign in to Coremetry" style={{ width: '100%' }} />
        </Field>
        <Field label="Login subtitle (optional — shown under the title)">
          <textarea value={b.loginSubtitle ?? ''} onChange={e => set('loginSubtitle', e.target.value)}
                    placeholder='e.g. "Acme Bank observability. Access requires VPN."'
                    rows={2} style={{ width: '100%', resize: 'vertical' }} />
        </Field>

        <Row>
          <Field label="Sign-in button label" flex={1}>
            <input value={b.signInButtonLabel ?? ''} onChange={e => set('signInButtonLabel', e.target.value)}
                   placeholder={DEFAULT_BRANDING.signInButtonLabel} style={{ width: '100%' }} />
          </Field>
          <Field label="Username field label" flex={1}>
            <input value={b.usernameLabel ?? ''} onChange={e => set('usernameLabel', e.target.value)}
                   placeholder='e.g. "Corporate ID" or "Domain user"' style={{ width: '100%' }} />
          </Field>
        </Row>

        <Field label="Footer text (small line at the bottom of the login card)">
          <input value={b.footerText ?? ''} onChange={e => set('footerText', e.target.value)}
                 placeholder='e.g. "© Acme Bank · Internal use only"' style={{ width: '100%' }} />
        </Field>

        <Field label="UI language">
          <select value={b.language ?? 'en'}
                  onChange={e => set('language', e.target.value)}
                  style={{ fontSize: 13, padding: '4px 8px', minWidth: 200 }}>
            <option value="en">English (default)</option>
            <option value="tr">Türkçe</option>
          </select>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Drives sidebar labels, login strings, common buttons, page titles.
            Applies to every operator hitting this Coremetry instance.
          </div>
        </Field>

        <Field label="Primary color (CSS — e.g. #4f46e5, rgb(79,70,229))">
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <input value={b.primaryColor ?? ''} onChange={e => set('primaryColor', e.target.value)}
                   placeholder="leave empty for the bundled accent"
                   style={{ flex: 1 }} />
            {b.primaryColor && (
              <span style={{
                width: 32, height: 28, borderRadius: 4,
                background: b.primaryColor,
                border: '1px solid var(--border)',
              }} />
            )}
          </div>
        </Field>

        <Field label="Logo (PNG / SVG / JPG, ≤ 200 KB)">
          <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
            <input type="file" accept="image/png,image/svg+xml,image/jpeg,image/webp"
                   onChange={onLogo} />
            {b.logoDataUri && (
              <>
                <img src={b.logoDataUri} alt="logo preview"
                     style={{ maxHeight: 48, maxWidth: 140, objectFit: 'contain',
                              border: '1px solid var(--border)', borderRadius: 4,
                              padding: 4, background: 'var(--bg)' }} />
                <Button type="button" variant="danger" size="sm"
                  onClick={() => set('logoDataUri', '')}>
                  Remove
                </Button>
              </>
            )}
          </div>
        </Field>

        {msg && (
          <div style={{
            marginTop: 14, padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 14 }}>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
          <Button type="button" variant="danger" onClick={resetAll} disabled={busy}>
            Reset to defaults
          </Button>
        </div>
      </form>

      <AnnouncementSection />
    </div>
  );
}

// AnnouncementSection — v0.8.486 (operatör isteği): sayfa-üstü duyuru
// şeridi. Metin + opsiyonel link + ton; kaydet revizyon damgasını
// yeniler, kapatmış kullanıcılara şerit yeniden görünür.
function AnnouncementSection() {
  const [a, setA] = useState<import('@/lib/announcement').AnnouncementView | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  useEffect(() => {
    api.getAnnouncement().then(setA).catch(() => setA(null));
  }, []);

  if (a === undefined) return <div style={{ marginTop: 24 }}><Spinner /></div>;
  if (a === null) return null;

  const save = async () => {
    setBusy(true); setMsg(null);
    try {
      const saved = await api.putAnnouncement(a);
      setA(saved);
      setMsg('Kaydedildi — kapatmış kullanıcılara da yeniden görünür.');
    } catch (e) {
      setMsg(e instanceof Error ? e.message : 'Kaydedilemedi');
    } finally { setBusy(false); }
  };

  return (
    <div style={{ marginTop: 28, paddingTop: 18, borderTop: '1px solid var(--border)' }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>
        Duyuru şeridi
        {a.enabled && a.text
          ? <span className="badge b-ok" style={{ marginLeft: 8 }}>aktif</span>
          : <span className="badge b-gray" style={{ marginLeft: 8 }}>kapalı</span>}
      </h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 12 }}>
        Tüm kullanıcılara sayfa üstünde gösterilen genel duyuru — ör. destek
        e-postası, wiki adresi, planlı bakım notu. Kullanıcı kapatabilir;
        metni güncellediğinde herkese yeniden görünür.
      </p>
      <Row>
        <Field label="Duyuru metni">
          <input value={a.text ?? ''} maxLength={500} style={{ width: '100%' }}
            placeholder="Coremetry ile ilgili sorularınız için: apm-destek@…"
            onChange={e => setA({ ...a, text: e.target.value })} />
        </Field>
      </Row>
      <Row>
        <Field label="Link (opsiyonel)">
          <input value={a.linkUrl ?? ''} placeholder="http://wiki.local/coremetry"
            style={{ width: '100%' }}
            onChange={e => setA({ ...a, linkUrl: e.target.value })} />
        </Field>
        <Field label="Link etiketi">
          <input value={a.linkLabel ?? ''} maxLength={80} placeholder="Wiki"
            style={{ width: '100%' }}
            onChange={e => setA({ ...a, linkLabel: e.target.value })} />
        </Field>
        <Field label="Ton">
          <select value={a.tone ?? 'info'}
            onChange={e => setA({ ...a, tone: e.target.value as 'info' | 'warn' })}>
            <option value="info">Bilgi</option>
            <option value="warn">Önemli (amber)</option>
          </select>
        </Field>
      </Row>
      <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginTop: 10 }}>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
          <input type="checkbox" checked={a.enabled}
            onChange={e => setA({ ...a, enabled: e.target.checked })} />
          Şerit aktif
        </label>
        <Button onClick={() => { void save(); }} disabled={busy}>
          {busy ? 'Kaydediliyor…' : 'Kaydet'}
        </Button>
        {msg && <span style={{ fontSize: 12, color: 'var(--text2)' }}>{msg}</span>}
      </div>
    </div>
  );
}
