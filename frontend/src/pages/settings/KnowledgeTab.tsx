import { useEffect, useRef, useState } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { Field2, FlashBox, Row } from './shared';

// KnowledgeTab — doküman soru-cevap (RAG) yapılandırması (v0.8.491).
// v0.8.441'de AI Copilot sekmesinin altına RagSection olarak doğdu;
// wiki kaynakları + doküman kataloğu büyüyünce kendi sekmesine ayrıldı
// (operatör onaylı sadeleştirme #5). Davranış birebir aynı — save
// handler'lar, sentinel sır koruması, senkron akışı değişmedi.
//
// Embedding endpoint'i girilmedikçe RAG tamamen kapalı (chat bugünkü
// gibi çalışır); girilince yüklenen dokümanlar + taranan wiki sayfaları
// chat'te kaynak atıflı cevaplara dönüşür.

// SourceRow — kaynak editörünün yerel satır modeli (v0.8.451). Sunucu
// şifreyi asla geri göndermez; hasPassword/hasHeader "kayıtlı" durumunu
// taşır, boş bırakılan alan kayıtlıyı korur (SMTP deseni).
type SourceRow = {
  url: string;
  username: string;
  password: string;   // operatörün BU oturumda yazdığı; '' = korunur
  authHeader: string; // '' = korunur (kayıtlıysa)
  hasPassword: boolean;
  hasHeader: boolean;
};

export function KnowledgeTab() {
  const [cfg, setCfg] = useState<import('@/lib/types').RagConfigView | null | undefined>(undefined);
  const [docs, setDocs] = useState<import('@/lib/types').RagDocument[] | null | undefined>(undefined);
  const [apiKey, setApiKey] = useState('');
  const [sources, setSources] = useState<SourceRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const load = () => {
    api.getRagConfig().then(c => {
      setCfg(c);
      setSources((c.sources ?? []).map(s0 => ({
        url: s0.url,
        username: s0.username ?? '',
        password: '',
        authHeader: '',
        hasPassword: s0.password === '********',
        hasHeader: s0.authHeader === '********',
      })));
    }).catch(() => setCfg(null));
    api.listRagDocuments().then(r => setDocs(r.documents)).catch(() => setDocs(null));
  };
  useEffect(load, []);

  if (cfg === undefined) return <Spinner />;
  if (cfg === null) return <Empty icon="📄" title="RAG ayarları yüklenemedi" />;

  const save = async () => {
    setBusy(true); setMsg(null);
    try {
      // Boş şifre/header kayıtlıyken '********' sentineliyle gider —
      // sunucu URL eşleşmesinden mevcut değeri devralır; yeni yazılan
      // düz gider (bir daha asla geri dönmez).
      const outSources = sources
        .filter(s0 => s0.url.trim())
        .map(s0 => ({
          url: s0.url.trim(),
          username: s0.username.trim() || undefined,
          password: s0.password ? s0.password : (s0.hasPassword ? '********' : undefined),
          authHeader: s0.authHeader.trim() ? s0.authHeader.trim() : (s0.hasHeader ? '********' : undefined),
        }));
      const next = await api.putRagConfig({
        endpoint: cfg.endpoint, model: cfg.model, enabled: cfg.enabled,
        topK: cfg.topK, apiKey: apiKey || undefined, sources: outSources,
      });
      setCfg(next); setApiKey('');
      setSources(prev => prev.filter(s0 => s0.url.trim()).map(s0 => ({
        ...s0,
        password: '',
        authHeader: '',
        hasPassword: s0.hasPassword || !!s0.password,
        hasHeader: s0.hasHeader || !!s0.authHeader.trim(),
      })));
      setMsg({ kind: 'ok', text: 'Kaydedildi.' });
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    } finally { setBusy(false); }
  };

  const upload = async (f: File) => {
    setBusy(true); setMsg(null);
    try {
      const r = await api.uploadRagDocument(f);
      setMsg({ kind: 'ok', text: `${f.name}: ${r.chunks} parça indekslendi.` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    } finally { setBusy(false); if (fileRef.current) fileRef.current.value = ''; }
  };

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>
        Doküman soru-cevap (RAG)
        {cfg.enabled && cfg.endpoint
          ? <span className="badge b-ok" style={{ marginLeft: 8 }}>aktif</span>
          : <span className="badge b-gray" style={{ marginLeft: 8 }}>kapalı</span>}
      </h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 12 }}>
        Runbook / prosedür / mimari dokümanlarını yükle; Copilot chat sorulara bu
        dokümanlardan <b>kaynak atıflı</b> cevap versin. OpenAI-uyumlu bir
        <code> /v1/embeddings</code> endpoint'i gerekir (vLLM/KServe'de bge-m3 önerilir).
      </p>

      <Row>
        <Field2 label="Embedding endpoint" hint="ör. http://bge-m3.ai.svc:8000/v1">
          <input value={cfg.endpoint} onChange={e => setCfg({ ...cfg, endpoint: e.target.value })}
                 placeholder="http://…/v1" style={{ width: '100%' }} />
        </Field2>
        <Field2 label="Model" small hint="ör. BAAI/bge-m3">
          <input value={cfg.model} onChange={e => setCfg({ ...cfg, model: e.target.value })}
                 style={{ width: '100%' }} />
        </Field2>
        <Field2 label="Top-K" small hint="cevaba girecek parça sayısı (1-20)">
          <input type="number" min={1} max={20} value={cfg.topK ?? 5}
                 onChange={e => setCfg({ ...cfg, topK: Number(e.target.value) })}
                 style={{ width: '100%' }} />
        </Field2>
      </Row>
      <Row>
        <Field2 label="API key (opsiyonel)" small
          hint={cfg.hasKey ? 'kayıtlı — boş bırakırsan korunur' : 'endpoint auth istemiyorsa boş bırak'}>
          <input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)}
                 placeholder={cfg.hasKey ? '********' : ''} style={{ width: '100%' }} />
        </Field2>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13, marginTop: 18 }}>
          <input type="checkbox" checked={cfg.enabled}
                 onChange={e => setCfg({ ...cfg, enabled: e.target.checked })} />
          RAG aktif
        </label>
        <Button onClick={() => { void save(); }} disabled={busy} style={{ marginTop: 12 }}>
          {busy ? 'Kaydediliyor…' : 'Kaydet'}
        </Button>
      </Row>

      {/* Wiki / URL kaynakları (v0.8.442; v0.8.451 yapılandırılmış
          editör) — kaynak başına URL + opsiyonel Basic kimlik
          (on-prem Azure DevOps: kullanıcı boş, PAT şifreye) veya ham
          header. Şifre/header kayıtlıysa boş alan korur; yeni değer
          yazınca değişir, hiçbir sır geri echo edilmez. */}
      <div style={{ marginTop: 16 }}>
        <h3 style={{ fontSize: 13, fontWeight: 600, margin: '0 0 6px' }}>Wiki / URL kaynakları</h3>
        <p style={{ fontSize: 11.5, color: 'var(--text3)', margin: '0 0 6px' }}>
          Aynı host + path altındaki sayfalar taranır (≤200 sayfa, derinlik 3),
          30 dk'da bir otomatik senkron; değişmeyen sayfa yeniden indekslenmez.
          Kimlik isteyen wiki'de (ör. on-prem Azure DevOps) kullanıcı+şifre gir —
          PAT kullanıyorsan kullanıcıyı boş bırak, PAT'i şifre alanına yaz.
        </p>
        <div style={{ display: 'grid', gap: 6 }}>
          {sources.map((src, i) => (
            <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <input value={src.url} placeholder="https://azuredevops.banka.local/DefaultCollection/Proje/_wiki/…"
                onChange={e => setSources(p => p.map((x, j) => j === i ? { ...x, url: e.target.value } : x))}
                spellCheck={false}
                style={{ flex: 3, fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
              <input value={src.username} placeholder="kullanıcı (PAT'te boş)"
                autoComplete="off"
                onChange={e => setSources(p => p.map((x, j) => j === i ? { ...x, username: e.target.value } : x))}
                style={{ flex: 1, fontSize: 12 }} />
              <input type="password" value={src.password}
                placeholder={src.hasPassword ? '******** (kayıtlı)' : 'şifre / PAT'}
                autoComplete="new-password"
                title={src.hasPassword ? 'Kayıtlı — boş bırakırsan korunur' : ''}
                onChange={e => setSources(p => p.map((x, j) => j === i ? { ...x, password: e.target.value } : x))}
                style={{ flex: 1, fontSize: 12 }} />
              <input value={src.authHeader}
                placeholder={src.hasHeader ? '******** (kayıtlı header)' : 'Header: değer (ops.)'}
                title="Ham auth header — doluysa Basic yerine bu gönderilir"
                spellCheck={false}
                onChange={e => setSources(p => p.map((x, j) => j === i ? { ...x, authHeader: e.target.value } : x))}
                style={{ flex: 1, fontFamily: 'ui-monospace, monospace', fontSize: 11.5 }} />
              <Button variant="secondary" size="sm" type="button" aria-label="Kaynağı kaldır"
                title="Kaynağı kaldır (kayıtlı sırrıyla birlikte)"
                onClick={() => setSources(p => p.filter((_, j) => j !== i))}>✕</Button>
            </div>
          ))}
          <div>
            <Button variant="secondary" size="sm" type="button"
              onClick={() => setSources(p => [...p, {
                url: '', username: '', password: '', authHeader: '',
                hasPassword: false, hasHeader: false,
              }])}>
              ＋ Kaynak ekle
            </Button>
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, marginTop: 6, alignItems: 'center' }}>
          <Button variant="secondary" size="sm" type="button" disabled={busy}
            onClick={() => { void save(); }}>
            Kaynakları kaydet
          </Button>
          <Button variant="secondary" size="sm" type="button"
            disabled={busy || !cfg.enabled || !cfg.endpoint}
            title={!cfg.endpoint ? 'Önce embedding endpoint girip kaydet' : 'Tüm kaynakları şimdi tara'}
            onClick={async () => {
              setBusy(true); setMsg(null);
              try {
                const r = await api.syncRagSources();
                setMsg({ kind: 'ok', text: `Senkron: ${r.pages} sayfa · ${r.indexed} indekslendi · ${r.skipped} değişmemiş · ${r.pruned} silindi${r.errors?.length ? ` · ${r.errors.length} hata` : ''}` });
                load();
              } catch (e) {
                setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
              } finally { setBusy(false); }
            }}>
            ⟳ Şimdi senkronize et
          </Button>
        </div>
      </div>

      <div style={{ marginTop: 16, display: 'flex', alignItems: 'center', gap: 10 }}>
        <h3 style={{ fontSize: 13, fontWeight: 600, margin: 0 }}>Dokümanlar</h3>
        <input ref={fileRef} type="file" accept=".md,.txt" style={{ display: 'none' }}
               onChange={e => { const f = e.target.files?.[0]; if (f) void upload(f); }} />
        <Button variant="secondary" size="sm" type="button"
          disabled={busy || !cfg.enabled || !cfg.endpoint}
          title={!cfg.endpoint ? 'Önce embedding endpoint girip kaydet' : 'md / txt yükle (≤5MB)'}
          onClick={() => fileRef.current?.click()}>
          ⬆ Doküman yükle
        </Button>
      </div>

      {docs === undefined && <Spinner />}
      {docs === null && <Empty icon="📄" title="Doküman listesi yüklenemedi" />}
      {docs && docs.length === 0 && (
        <p style={{ fontSize: 12, color: 'var(--text3)', marginTop: 8 }}>Henüz doküman yok.</p>
      )}
      {docs && docs.length > 0 && (
        <div className="table-wrap" style={{ marginTop: 8 }}>
          <table>
            <thead><tr><th>Doküman</th><th>Kaynak</th><th>Parça</th><th>Boyut</th><th>Yükleyen</th><th></th></tr></thead>
            <tbody>
              {docs.map(d => (
                <tr key={d.docId}>
                  <td className="mono" style={{ fontSize: 12 }}>{d.docName}</td>
                  <td><span className="badge b-gray">{d.source}</span></td>
                  <td className="mono" style={{ textAlign: 'right' }}>{d.chunks}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{(d.bytes / 1024).toFixed(1)} KB</td>
                  <td style={{ fontSize: 11, color: 'var(--text2)' }}>{d.uploadedBy || '—'}</td>
                  <td style={{ textAlign: 'right' }}>
                    <Button variant="danger" size="sm" type="button" disabled={busy}
                      onClick={async () => {
                        if (!confirm(`${d.docName} silinsin mi?`)) return;
                        try { await api.deleteRagDocument(d.docId); load(); }
                        catch (e) { setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) }); }
                      }}>
                      Sil
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
    </div>
  );
}
