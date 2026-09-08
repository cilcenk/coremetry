// AiChatRetentionPanel.tsx — v0.10.561 (CoSRE Faz 5c). Sohbet arşivi
// (saved_views page='ai-chat') saklama süresi; AiBudgetPanel kalıbı: kendi
// GET/PUT'u, kutu boş = varsayılan 90, 0 = süpürme kapalı. Worker saatte bir
// süpürür (leader kilidi), silinen sayı sunucu logunda.
import { useEffect, useState, type FormEvent } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { Field2, FlashBox, Row, SectionTitle, humanize } from './shared';
import { retentionSummaryTR, retentionToForm, retentionToWire } from './aiChatRetention';

export function AiChatRetentionPanel() {
  const [form, setForm] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.aiChatRetention()
      .then(c => { if (!cancelled) { setForm(retentionToForm(c)); setLoaded(true); } })
      .catch(e => { if (!cancelled) { setFlash({ kind: 'err', text: humanize(e) }); setLoaded(true); } });
    return () => { cancelled = true; };
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const wire = retentionToWire(form);
    if (typeof wire === 'string') { setFlash({ kind: 'err', text: wire }); return; }
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putAIChatRetention(wire);
      setForm(retentionToForm(saved));
      setFlash({ kind: 'ok', text: retentionSummaryTR(saved) });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={save} style={{ marginTop: 18, padding: 16, borderRadius: 8, background: 'var(--bg2)', border: '1px solid var(--border)', maxWidth: 640 }}>
      <SectionTitle>Sohbet arşivi</SectionTitle>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 10 }}>
        CoSRE sohbetleri son yazımlarından bu kadar gün sonra silinir; boş = 90 (varsayılan), 0 = süpürme kapalı. Silme geri alınamaz.
      </div>
      <Row>
        <Field2 label="Saklama (gün)" small hint="0 = kapalı · en fazla 3650">
          <input inputMode="numeric" value={form} onChange={e => setForm(e.target.value)} disabled={!loaded || busy} placeholder="90" />
        </Field2>
      </Row>
      <div style={{ marginTop: 12 }}>
        <Button type="submit" variant="primary" loading={busy} disabled={!loaded}>Kaydet</Button>
      </div>
      {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
    </form>
  );
}
