import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { DevOpsFlavor, DevOpsTestResult } from '@/lib/types';

// DevOpsTab — Azure DevOps Server / TFS bağlantısı (v0.9.829).
//
// BİLİNÇLİ DAR DİLİM: yalnız bağlantı katmanı. Repo eşleme ve
// kod-inceleme sonraki adımda; şu an bu ayarı okuyan bir yüzey
// YOK. Kart açıklaması bunu açıkça söylüyor — "kaydettim, artık
// stack trace'te kod göreceğim" beklentisi yaratmak, özelliğin
// kendisinden daha pahalıya patlar.
//
// TempoTab şablonu: PAT hiç geri dönmez (hasPat = "kayıtlı"
// göstergesi), boş bırakılan PAT alanı saklı değeri korur, TLS
// atlama kutusu aynı uyarı metniyle.
export function DevOpsTab() {
  const [loaded, setLoaded] = useState(false);
  const [baseUrl, setBaseUrl] = useState('');
  const [collection, setCollection] = useState('');
  const [project, setProject] = useState('');
  const [username, setUsername] = useState('');
  const [pat, setPat] = useState('');
  const [hasPat, setHasPat] = useState(false);
  const [flavor, setFlavor] = useState<DevOpsFlavor>('auto');
  const [insecure, setInsecure] = useState(false);
  const [detected, setDetected] = useState<{ flavor?: DevOpsFlavor; apiVersion?: string }>({});
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [test, setTest] = useState<DevOpsTestResult | null>(null);

  useEffect(() => {
    api.getDevOpsSettings().then(s => {
      setBaseUrl(s.baseUrl || '');
      setCollection(s.collection || '');
      setProject(s.project || '');
      setUsername(s.username || '');
      setHasPat(s.hasPat);
      setFlavor((s.flavor || 'auto') as DevOpsFlavor);
      setInsecure(!!s.insecureSkipVerify);
      setDetected({ flavor: s.detectedFlavor, apiVersion: s.detectedApiVersion });
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  // PAT boşsa gövdeden tamamen çıkar — sunucu sözleşmesi "boş =
  // saklıyı koru", ama alanı hiç göndermemek niyeti daha net
  // ifade ediyor ve aynı yola düşüyor.
  const buildInput = () => ({
    baseUrl, collection, project, username, flavor,
    insecureSkipVerify: insecure,
    ...(pat ? { pat } : {}),
  });

  const runTest = async () => {
    setBusy(true); setMsg(null); setTest(null);
    try {
      const r = await api.testDevOpsSettings(buildInput());
      setTest(r);
      if (r.ok && r.detectedFlavor) {
        setDetected({ flavor: r.detectedFlavor, apiVersion: r.apiVersion });
      }
    } catch (err) {
      setTest({ ok: false, projectCount: 0,
        error: err instanceof Error ? err.message : 'Test başarısız' });
    } finally {
      setBusy(false);
    }
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putDevOpsSettings(buildInput());
      setHasPat(next.hasPat);
      setPat('');
      setDetected({ flavor: next.detectedFlavor, apiVersion: next.detectedApiVersion });
      setMsg({ kind: 'ok', text: next.baseUrl
        ? 'Kaydedildi — bağlantı bilgileri saklandı (tüm pod’lar <30s içinde eşitlenir).'
        : 'Kaydedildi — bağlantı temizlendi.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Kayıt başarısız' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  const configured = baseUrl.trim().length > 0;
  const flavorLabel = (f?: DevOpsFlavor) =>
    f === 'tfs' ? 'TFS' : f === 'azure-devops-server' ? 'Azure DevOps Server' : 'Auto';

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>
        Kod entegrasyonu (Azure DevOps / TFS)
      </h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Şirket içi Azure DevOps Server / TFS koleksiyonuna okuma bağlantısı.
        <strong style={{ color: 'var(--text1)' }}> Şimdilik yalnız bağlantı</strong> —
        repo eşleme ve kod-inceleme sonraki adım. Yani burayı doldurmak
        henüz hiçbir ekranı değiştirmez; amaç sunucu adresinin ve PAT’ın
        çalıştığını, o özellik gelmeden önce doğrulayabilmek.
      </p>

      <div className={`status-banner status-banner-${configured ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${configured ? 'operational' : 'degraded'}`}>
          {configured ? 'YAPILANDIRILDI' : 'YAPILANDIRILMADI'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {configured
            ? `${baseUrl}${collection ? ` / ${collection}` : ''}`
            : 'Sunucu adresi girilmedi.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Sunucu URL</div>
          <input value={baseUrl}
            onChange={e => setBaseUrl(e.target.value)}
            placeholder="https://devops.sirket.local/tfs"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Sondaki eğik çizgi serbest. Çağrı:
            <code style={{ marginLeft: 4 }}>{'{url}/{collection}/_apis/projects'}</code>
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Collection</div>
          <input value={collection}
            onChange={e => setCollection(e.target.value)}
            placeholder="DefaultCollection"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Koleksiyonu sunucu URL’sine dahil ettiyseniz boş bırakın.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Project <span style={{ color: 'var(--text3)' }}>(opsiyonel)</span>
          </div>
          <input value={project}
            onChange={e => setProject(e.target.value)}
            placeholder="boş bırakılırsa yalnız koleksiyon doğrulanır"
            style={{ width: '100%' }} />
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Kullanıcı adı <span style={{ color: 'var(--text3)' }}>(opsiyonel)</span>
          </div>
          <input value={username}
            onChange={e => setUsername(e.target.value)}
            placeholder="PAT kullanıyorsanız boş bırakın"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            PAT’ta kullanıcı adı boş olağandır; eski TFS/NTLM kurulumları
            hesap adı isteyebilir.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Personal Access Token (PAT)
            {hasPat && <span style={{ color: 'var(--ok)', marginLeft: 8 }}>· kayıtlı</span>}
          </div>
          <input type="password" value={pat}
            onChange={e => setPat(e.target.value)}
            placeholder={hasPat ? '(boş bırak = saklı değer korunur)' : 'PAT yapıştır…'}
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Yeterli yetki: <strong>Code (Read)</strong>. Token hiçbir zaman geri
            gösterilmez; değiştirmek için yenisini yapıştırın.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Sürüm (flavor)</div>
          <select value={flavor}
            onChange={e => setFlavor(e.target.value as DevOpsFlavor)}
            style={{ width: '100%' }}>
            <option value="auto">Auto (URL şeklinden tahmin + ping ile doğrula)</option>
            <option value="azure-devops-server">Azure DevOps Server (api-version 6.0)</option>
            <option value="tfs">TFS (api-version 4.1)</option>
          </select>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Auto her açılışta yeniden tespit eder — sonuç ayara yazılmaz, böylece
            sunucu yükseltildiğinde eski tahmin yalan söylemez.
            {detected.flavor && (
              <> Son tespit: <strong>{flavorLabel(detected.flavor)}</strong>
                {detected.apiVersion ? ` · api-version ${detected.apiVersion}` : ''}.</>
            )}
          </div>
        </label>

        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={insecure}
            onChange={e => setInsecure(e.target.checked)} />
          <span style={{ fontSize: 13 }}>
            TLS doğrulamasını atla
            <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
              (kendinden imzalı / iç CA sertifikaları — POC)
            </span>
          </span>
        </label>

        {test && (
          <div style={{
            marginBottom: 12, fontSize: 12, padding: '8px 10px', borderRadius: 6,
            background: 'var(--bg0)', border: '1px solid var(--border)',
            color: test.ok ? 'var(--ok)' : 'var(--err)',
          }}>
            {test.ok ? (
              <>
                ✓ {test.projectCount} proje
                {' · tespit: '}{flavorLabel(test.detectedFlavor)}
                {test.apiVersion ? ` ${test.apiVersion}` : ''}
                {test.projectChecked && ` · "${project}" projesi doğrulandı`}
              </>
            ) : (
              <>✗ {test.error || 'Bağlantı kurulamadı'}</>
            )}
          </div>
        )}

        {msg && (
          <div style={{ marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <Button type="button" variant="secondary" disabled={busy || !configured}
            onClick={runTest}>
            {busy ? 'Çalışıyor…' : 'Bağlantıyı test et'}
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Çalışıyor…' : 'Kaydet'}
          </Button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 8 }}>
          Test kaydetmez — formdaki değerlerle dener. PAT alanı boşsa saklı
          token kullanılır.
        </div>
      </form>
    </div>
  );
}
