// AiProfilesPanel — v0.10.176 (dilim B; spec onayı 2026-08-30): model
// profilleri tablosu + düzenleme modalı + yüzey eşlemesi. Her profil kendi
// sağlayıcı/endpoint/anahtar/model'i (ai_settings_profiles.go); admin bir
// profili VARSAYILAN yapar, aşağıdaki eski form o profili düzenler.
// Anahtar hiç geri gelmez (hasKey); boş anahtar = mevcut korunur.
import { useEffect, useState, type FormEvent } from 'react';
import { Button, Badge, Modal, SelectField, useConfirm } from '@/components/ui';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import type { AIModelProfile, AIModelProfileInput, AIProfilesPayload, AIProvider, AIProfileTestResult } from '@/lib/types';
import { Field2, FlashBox, Row } from './shared';
import { slugifyProfileId, PROFILE_ID_RE, tuningSummary, endpointLabel, profileUsable } from './aiProfiles';

const COLS: DataTableColumn<AIModelProfile>[] = [
  { id: 'label',    label: 'Profil',    sortValue: p => p.label || p.id,                naturalDir: 'asc', width: 230 },
  { id: 'provider', label: 'Sağlayıcı', sortValue: p => p.provider,                     naturalDir: 'asc', width: 115 },
  { id: 'model',    label: 'Model',     sortValue: p => p.model ?? '',                  naturalDir: 'asc', width: 160 },
  { id: 'endpoint', label: 'Endpoint',  sortValue: p => endpointLabel(p.provider, p.baseUrl), naturalDir: 'asc', width: 210 },
  { id: 'key',      label: 'Anahtar',   sortValue: p => (p.hasKey ? 1 : 0),             width: 90 },
  { id: 'tuning',   label: 'Tuning',    sortValue: p => tuningSummary(p),                width: 130 },
];

type Draft = { id: string; label: string; provider: AIProvider; baseUrl: string; apiKey: string; model: string; skipTls: boolean; maxTokens: string; temperature: string; timeoutS: string };
const emptyDraft = (): Draft => ({ id: '', label: '', provider: 'openai', baseUrl: '', apiKey: '', model: '', skipTls: false, maxTokens: '', temperature: '', timeoutS: '' });
const draftOf = (p: AIModelProfile): Draft => ({
  id: p.id, label: p.label ?? '', provider: p.provider, baseUrl: p.baseUrl ?? '', apiKey: '', model: p.model ?? '', skipTls: !!p.skipTls,
  maxTokens: p.maxTokens ? String(p.maxTokens) : '', temperature: p.temperature === undefined || p.temperature === null ? '' : String(p.temperature), timeoutS: p.timeoutS ? String(p.timeoutS) : '',
});

export function AiProfilesPanel({ payload, onChange }: { payload: AIProfilesPayload; onChange: (p: AIProfilesPayload) => void }) {
  const confirm = useConfirm();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [tests, setTests] = useState<Record<string, AIProfileTestResult | 'pending'>>({});
  const [surface, setSurface] = useState({ intent: payload.surfaceMap.intent ?? '', background: payload.surfaceMap.background ?? '' });
  // silme/varsayılan sonrası sunucunun haritası yeniden yüklenir (bayat kimlik 400 verirdi, inceleme #4)
  useEffect(() => { setSurface({ intent: payload.surfaceMap.intent ?? '', background: payload.surfaceMap.background ?? '' }); }, [payload.surfaceMap.intent, payload.surfaceMap.background]);
  const dt = useDataTable<AIModelProfile>({ storageKey: 'settings-ai-profiles', columns: COLS, rows: payload.profiles, initialSort: { id: 'label', dir: 'asc' } });

  const run = async (fn: () => Promise<AIProfilesPayload>, ok: string) => {
    setBusy(true); setMsg(null);
    try { onChange(await fn()); setMsg({ kind: 'ok', text: ok }); }
    catch (e) { setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) }); }
    finally { setBusy(false); }
  };
  const saveDraft = async (e: FormEvent) => {
    e.preventDefault();
    if (!draft) return;
    const id = isNew ? (draft.id || slugifyProfileId(draft.label)) : draft.id;
    if (!PROFILE_ID_RE.test(id)) { setMsg({ kind: 'err', text: 'Kimlik [a-z0-9][a-z0-9_-]{0,39} olmalı (etiketten türetilir, düzenlenebilir).' }); return; }
    const body: AIModelProfileInput = {
      label: draft.label.trim() || undefined, provider: draft.provider, baseUrl: draft.baseUrl.trim() || undefined,
      apiKey: draft.apiKey || undefined, model: draft.model.trim() || undefined, skipTls: draft.skipTls,
      maxTokens: draft.maxTokens ? Number(draft.maxTokens) : undefined,
      temperature: draft.temperature === '' ? undefined : Number(draft.temperature),
      timeoutS: draft.timeoutS ? Number(draft.timeoutS) : undefined,
    };
    await run(() => api.putAIProfile(id, body), isNew ? `Profil eklendi: ${id}` : `Profil güncellendi: ${id}`);
    setDraft(null);
  };
  const test = async (id: string) => {
    setTests(t => ({ ...t, [id]: 'pending' }));
    try { const r = await api.testAIProfile(id); setTests(t => ({ ...t, [id]: r })); }
    catch (e) { setTests(t => ({ ...t, [id]: { ok: false, ms: 0, profile: id, error: e instanceof Error ? e.message : String(e) } })); }
  };
  const saveSurface = () => run(() => api.putAISurfaceMap({ intent: surface.intent, background: surface.background }), 'Yüzey eşlemesi kaydedildi');
  const options = payload.profiles.map(p => <option key={p.id} value={p.id}>{p.label || p.id}</option>);

  return (
    <div style={{ marginBottom: 20 }}>
      <Row>
        <h3 style={{ fontSize: 13, fontWeight: 600, margin: 0 }}>Model profilleri</h3>
        <span className="field-hint">{payload.profiles.length} profil · varsayılan: <b>{payload.defaultProfile}</b> · aşağıdaki form varsayılan profili düzenler</span>
        <span style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
          <ResetLayoutButton dt={dt} />
          <Button variant="secondary" size="sm" onClick={() => { setIsNew(true); setDraft(emptyDraft()); }}>Profil ekle</Button>
        </span>
      </Row>
      <div className="table-wrap is-fit" style={{ marginTop: 8 }}>
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} trailing={[330]} />
          <DataTableHead dt={dt} trailing={<th></th>} />
          <tbody>
            {dt.sortedRows.map(p => {
              const t = tests[p.id];
              return (
                <tr key={p.id}>
                  <td style={{ fontWeight: 600, overflow: 'hidden' }} title={p.id}>
                    <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{p.label || p.id}</div>
                    {/* v0.10.179 — rozet kendi satırında: ellipsis rozeti yutuyordu (178 canlı görüntüsü) */}
                    <div className="field-hint mono" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                      {p.label ? p.id : null}{p.default && <Badge tone="success">varsayılan</Badge>}
                    </div>
                  </td>
                  <td><span className="badge b-gray">{p.provider}</span></td>
                  <td className="mono" style={{ fontSize: 11 }} title={p.model}>{p.model || '—'}</td>
                  <td className="mono" style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={endpointLabel(p.provider, p.baseUrl)}>{endpointLabel(p.provider, p.baseUrl)}</td>
                  <td>{p.hasKey ? <span className="badge b-ok">stored</span> : profileUsable(p) ? <span className="badge b-gray">no auth</span> : <span className="badge b-warn">yok</span>}</td>
                  <td style={{ fontSize: 11, color: 'var(--text2)' }}>{tuningSummary(p)}</td>
                  <td style={{ textAlign: 'right' }}>
                    <Button variant="secondary" size="sm" onClick={() => { setIsNew(false); setDraft(draftOf(p)); }}>Düzenle</Button>{' '}
                    {!p.default && <Button variant="secondary" size="sm" disabled={busy} onClick={() => run(() => api.defaultAIProfile(p.id), `Varsayılan: ${p.id}`)}>Varsayılan yap</Button>}{' '}
                    <Button variant="secondary" size="sm" loading={t === 'pending'} onClick={() => test(p.id)}>Bağlantıyı dene</Button>{' '}
                    {!p.default && <Button variant="ghost-danger" size="sm" disabled={busy} onClick={async () => {
                      if (!await confirm({ title: 'Profil silinsin mi?', body: <><b>{p.label || p.id}</b> silinecek; bu profile eşlenmiş yüzeyler varsayılana döner.</>, confirmLabel: 'Sil', danger: true })) return;
                      await run(() => api.deleteAIProfile(p.id), `Silindi: ${p.id}`);
                    }}>Sil</Button>}
                    {t && t !== 'pending' && (
                      <div className={`field-hint ${t.ok ? '' : 'is-err'}`} style={{ textAlign: 'right' }} title={t.error ?? t.sample}>
                        {t.ok ? `✓ ${t.ms} ms${t.sample ? ` · «${t.sample}»` : ''}` : `✗ ${t.error ?? 'hata'}`}
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end', marginTop: 12, flexWrap: 'wrap' }}>
        <SelectField label="Niyet sınıflandırıcısı (chat-intent)" value={surface.intent} onChange={e => setSurface(s => ({ ...s, intent: e.target.value }))} hint="Serbest soru sınıflandırma çağrısı için profil; boş = varsayılan (küçük yerel model önerilir). Eşleşmeyen sorunun genel bilgi cevabı (chat-general) VARSAYILAN profilden gelir — sınıflandırıcıya seçilen küçük model genel cevap yazmaz.">
          <option value="">varsayılan</option>{options}
        </SelectField>
        <SelectField label="Arka plan açıklayıcılar (auto-explain)" value={surface.background} onChange={e => setSurface(s => ({ ...s, background: e.target.value }))} hint="Problem/exception otomatik özetleri için profil; boş = varsayılan.">
          <option value="">varsayılan</option>{options}
        </SelectField>
        <Button variant="secondary" size="sm" disabled={busy} onClick={saveSurface}>Eşlemeyi kaydet</Button>
      </div>
      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}

      <Modal open={!!draft} onClose={() => setDraft(null)} title={isNew ? 'Yeni model profili' : `Profili düzenle · ${draft?.id ?? ''}`}
        footer={<Row><span style={{ marginLeft: 'auto' }} /><Button variant="secondary" onClick={() => setDraft(null)}>Vazgeç</Button><Button variant="primary" type="submit" form="ai-profile-form" loading={busy}>Kaydet</Button></Row>}>
        {draft && (
          <form id="ai-profile-form" onSubmit={saveDraft}>
            <Field2 label="Etiket" hint="Tabloda ve seçicilerde görünen ad"><input value={draft.label} onChange={e => setDraft({ ...draft, label: e.target.value, id: isNew ? slugifyProfileId(e.target.value) : draft.id })} placeholder="Büyük model (gemma4-31b)" /></Field2>
            <Field2 label="Kimlik" hint={isNew ? 'Etiketten türetilir; [a-z0-9][a-z0-9_-]{0,39}' : 'Kimlik değiştirilemez'}><input className="mono" value={draft.id} disabled={!isNew} onChange={e => setDraft({ ...draft, id: e.target.value })} /></Field2>
            <SelectField label="Sağlayıcı" value={draft.provider} onChange={e => setDraft({ ...draft, provider: e.target.value as AIProvider })}>
              <option value="openai">OpenAI-uyumlu (vLLM / Ollama / LM Studio / OpenAI)</option>
              <option value="anthropic">Anthropic</option>
              <option value="github">GitHub Copilot</option>
            </SelectField>
            {draft.provider === 'openai' && <Field2 label="Base URL" hint="Örn. http://vllm:8000/v1 — her profil kendi endpoint'ine gider"><input value={draft.baseUrl} onChange={e => setDraft({ ...draft, baseUrl: e.target.value })} placeholder="http://vllm:8000/v1" /></Field2>}
            <Field2 label="Model"><input value={draft.model} onChange={e => setDraft({ ...draft, model: e.target.value })} placeholder="gemma4-31b" /></Field2>
            <Field2 label="API anahtarı" hint={isNew ? 'Yerel endpoint için boş bırakılabilir' : 'Boş = mevcut anahtar korunur'}><input type="password" autoComplete="new-password" value={draft.apiKey} onChange={e => setDraft({ ...draft, apiKey: e.target.value })} /></Field2>
            {draft.provider === 'openai' && <label style={{ display: 'flex', gap: 8, alignItems: 'center', fontSize: 12, marginBottom: 12 }}><input type="checkbox" checked={draft.skipTls} onChange={e => setDraft({ ...draft, skipTls: e.target.checked })} /> TLS doğrulamasını atla (öz-imzalı yerel uç)</label>}
            <Row>
              <Field2 label="Max tokens" small hint="boş = küresel"><input value={draft.maxTokens} onChange={e => setDraft({ ...draft, maxTokens: e.target.value })} inputMode="numeric" /></Field2>
              <Field2 label="Temperature" small hint="boş = küresel; 0 bir değerdir"><input value={draft.temperature} onChange={e => setDraft({ ...draft, temperature: e.target.value })} inputMode="decimal" /></Field2>
              <Field2 label="Timeout (s)" small hint="boş = küresel"><input value={draft.timeoutS} onChange={e => setDraft({ ...draft, timeoutS: e.target.value })} inputMode="numeric" /></Field2>
            </Row>
          </form>
        )}
      </Modal>
    </div>
  );
}
