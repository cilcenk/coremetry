import { useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { SettingRow, SettingsLoadError, useSettingsLoad } from './shared';

// LogBridgeTab — dış log sistemine köprü şablonları (v0.9.657).
//
// v0.9.655 backend'i CoSRE cevaplarındaki request_id'lerden tıklanabilir
// linkler üretiyor, ama şablonu girmenin tek yolu `curl` ile admin
// ucuna vurmaktı. Özellik yazıldı, kullanılamıyordu.
//
// Emsal: KibanaTab (v0.5.236) — aynı sınıf, dış derin-link yapılandırması.
//
// ORTAM BAŞINA ŞABLON: aynı kurulum birden çok ortamın verisini
// taşıyabiliyor ve her ortamın log arayüzü ayrı bir adres. Ortam servis
// adının SONEKİNDEN çözülüyor (-int/-uat/-prep); soneksiz ad prod demek
// ve "default" şablonunu alıyor.

// ENV_HINT — her ortam alanının altındaki açıklama. Sıra ekranda
// göründüğü sıra: önce prod (en sık), sonra test ortamları.
const ENV_ROWS: { key: string; label: string; hint: string }[] = [
  { key: 'default', label: 'Prod (varsayılan)', hint: 'Servis adı -int/-uat/-prep ile BİTMEYEN her servis bu adresi kullanır.' },
  { key: 'int', label: 'int', hint: 'Servis adı "-int" ile biten servisler.' },
  { key: 'uat', label: 'uat', hint: 'Servis adı "-uat" ile biten servisler.' },
  { key: 'prep', label: 'prep', hint: 'Servis adı "-prep" ile biten servisler.' },
];

export function LogBridgeTab() {
  const [tpls, setTpls] = useState<Record<string, string>>({});
  const [placeholder, setPlaceholder] = useState('{value}');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.getCorrelationLink(),
    s => {
      setTpls(s.templates ?? {});
      if (s.placeholder) setPlaceholder(s.placeholder);
    },
  );

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const r = await api.putCorrelationLink(tpls);
      const n = Object.keys(r.templates ?? {}).length;
      setMsg({ kind: 'ok', text: n > 0
        ? `Kaydedildi — ${n} ortam yapılandırıldı.`
        : 'Kaydedildi — köprü kapalı, link çizilmeyecek.' });
      setTpls(r.templates ?? {});
    } catch (err) {
      // Backend hangi ortamın hatalı olduğunu gövdede söylüyor; olduğu
      // gibi gösteriyoruz — "geçersiz şablon" tek başına yetersiz.
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Kaydedilemedi' });
    } finally {
      setBusy(false);
    }
  };

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
  if (!loaded) return <Spinner />;

  const configured = Object.values(tpls).filter(v => v.trim() !== '').length;

  return (
    <div style={{ maxWidth: 720 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Log sistemi köprüsü</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        CoSRE bir analizde örnek <code>request_id</code> bulduğunda, bu
        şablonlarla dış log arayüzünüze tıklanabilir bir link üretir.
        Coremetry log sisteminize proxy yapmaz; yalnız linki kurar.
      </p>

      <div className={`status-banner status-banner-${configured > 0 ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${configured > 0 ? 'operational' : 'degraded'}`}>
          {configured > 0 ? 'AÇIK' : 'YAPILANDIRILMADI'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {configured > 0
            ? `${configured} ortam için link üretilecek.`
            : 'Kapalı — cevaplarda log linki çizilmiyor.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 14 }}>
          Şablon <code>{placeholder}</code> yer tutucusunu taşımak zorunda —
          request_id oraya yazılır (URL-encode edilerek). Örnek şekil:{' '}
          <code style={{ whiteSpace: 'nowrap' }}>
            https://log-host/path?requestId={placeholder}
          </code>
        </div>

        {ENV_ROWS.map(({ key, label, hint }) => (
          <SettingRow key={key} label={label} hint={hint}>
            <input
              value={tpls[key] ?? ''}
              onChange={e => setTpls(t => ({ ...t, [key]: e.target.value }))}
              placeholder={`https://…?requestId=${placeholder}`}
              style={{ width: '100%' }} />
          </SettingRow>
        ))}

        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 12 }}>
          Boş bırakılan ortam kapalıdır. Bir ortamın şablonu yoksa prod
          şablonuna düşülür; o da yoksa link çizilmez.
        </div>

        {msg && (
          <div style={{ marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <Button type="submit" variant="primary" loading={busy}>
          Kaydet
        </Button>
      </form>
    </div>
  );
}
