// AiBudgetPanel — v0.10.411 (CoSRE denetimi E8): AI maliyet/gecikme
// bütçesi. Üç tavan (günlük token, günlük $, p95 ms); 0/boş = tavan yok.
// Rozet /ai sayfasında (son 24 saat). Kutular STRING state (aiTuning
// deseni): çeviri saf modülde (aiBudget.ts), burada yalnız form.
import { useEffect, useState, type FormEvent } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { budgetToForm, budgetToWire, type BudgetForm } from './aiBudget';
import { Field2, FlashBox, Row, SectionTitle, humanize } from './shared';

export function AiBudgetPanel() {
  const [form, setForm] = useState<BudgetForm>({ dailyTokens: '', dailyCostUsd: '', p95Ms: '' });
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.aiBudget()
      .then(st => { if (!cancelled) { setForm(budgetToForm(st.budget)); setLoaded(true); } })
      .catch(e => { if (!cancelled) { setFlash({ kind: 'err', text: humanize(e) }); setLoaded(true); } });
    return () => { cancelled = true; };
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const wire = budgetToWire(form);
    if (typeof wire === 'string') { setFlash({ kind: 'err', text: wire }); return; }
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putAIBudget(wire);
      setForm(budgetToForm(saved));
      setFlash({ kind: 'ok', text: 'Bütçe kaydedildi — rozet /ai sayfasında (son 24 saat).' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const set = (k: keyof BudgetForm) => (e: { target: { value: string } }) => setForm(f => ({ ...f, [k]: e.target.value }));

  return (
    <form onSubmit={save} style={{ marginTop: 18, padding: 16, borderRadius: 8, background: 'var(--bg2)', border: '1px solid var(--border)', maxWidth: 640 }}>
      <SectionTitle>Bütçe</SectionTitle>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 10 }}>
        Son 24 saatin kullanımı bu tavanlarla kıyaslanır; boş = tavan yok. %80 uyarı, %100 aşım.
      </div>
      <Row>
        <Field2 label="Günlük token" small hint="girdi + çıktı toplamı">
          <input inputMode="numeric" value={form.dailyTokens} onChange={set('dailyTokens')} disabled={!loaded || busy} />
        </Field2>
        <Field2 label="Günlük $" small hint="fiyatı bilinmeyen model varsa yargılanmaz">
          <input inputMode="decimal" value={form.dailyCostUsd} onChange={set('dailyCostUsd')} disabled={!loaded || busy} />
        </Field2>
        <Field2 label="p95 gecikme (ms)" small>
          <input inputMode="numeric" value={form.p95Ms} onChange={set('p95Ms')} disabled={!loaded || busy} />
        </Field2>
      </Row>
      <div style={{ marginTop: 12 }}>
        <Button type="submit" variant="primary" loading={busy} disabled={!loaded}>Kaydet</Button>
      </div>
      {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
    </form>
  );
}
