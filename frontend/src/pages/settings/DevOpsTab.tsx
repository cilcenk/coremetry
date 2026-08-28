import { useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Badge, Button, Field, useConfirm } from '@/components/ui';
import { api } from '@/lib/api';
import { useSettingsLoad, SettingsLoadError } from './shared';
import { fmtDateTime } from '@/lib/utils';
import type { DevOpsFlavor, DevOpsResolveDryRun, DevOpsTestResult, SchemaCatalogSummary } from '@/lib/types';

// DevOpsTab — Azure DevOps Server / TFS bağlantısı (v0.9.829),
// v0.9.830'da adlandırma konvansiyonu eklendi.
//
// TempoTab şablonu: PAT hiç geri dönmez (hasPat = "kayıtlı"
// göstergesi), boş bırakılan PAT alanı saklı değeri korur, TLS
// atlama kutusu aynı uyarı metniyle.
//
// Konvansiyon alanları VİRGÜLLE yazılır, sunucuya dizi gider.
// Alan boş bırakılırsa sunucu nil kaydeder ve varsayılan uygulanır;
// snapshot çözülmüş değeri geri döndüğü için kutu asla boş kalmaz —
// operatör çözücünün gerçekte neyi soyduğunu ekranda görür.
// splitList — "bsa-, svc-" → ['bsa-','svc-']. Boş girdi [] döner ve
// sunucu onu nil'e çevirip varsayılana düşer (cleanConventionList).
function splitList(s: string): string[] {
  return s.split(',').map(x => x.trim()).filter(Boolean);
}

export function DevOpsTab() {
  const confirm = useConfirm();
  const [baseUrl, setBaseUrl] = useState('');
  const [collection, setCollection] = useState('');
  const [project, setProject] = useState('');
  const [username, setUsername] = useState('');
  const [pat, setPat] = useState('');
  const [hasPat, setHasPat] = useState(false);
  const [flavor, setFlavor] = useState<DevOpsFlavor>('auto');
  const [insecure, setInsecure] = useState(false);
  // v0.10.75 — organizasyon kod araması. Varsayılan KAPALI:
  // ayrı bir uzantı (Code Search) ve PAT kapsamı istiyor.
  const [codeSearch, setCodeSearch] = useState(false);
  const [repoPrefixes, setRepoPrefixes] = useState('');
  const [branchOrder, setBranchOrder] = useState('');
  // v0.10.112 — uygulama paket önekleri + deneme tavanı (operatör-raporlu
  // "tavan çerçeve sınıflarına gidiyor"). Tavan metin olarak tutulur:
  // boş kutu = varsayılan, sunucu 0'ı "ayar yok" okur.
  const [appPrefixes, setAppPrefixes] = useState('');
  const [lookupLimit, setLookupLimit] = useState('');
  const [effectiveLimit, setEffectiveLimit] = useState(6);
  const [detected, setDetected] = useState<{ flavor?: DevOpsFlavor; apiVersion?: string }>({});
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [test, setTest] = useState<DevOpsTestResult | null>(null);
  // v0.9.1242 — çözüm provası. AYRI busy bayrağı: kaydet/test ile aynı
  // `busy`i paylaşsalardı bir prova, form butonlarını da kilitlerdi.
  const [dryService, setDryService] = useState('');
  const [dryBusy, setDryBusy] = useState(false);
  const [dry, setDry] = useState<DevOpsResolveDryRun | null>(null);
  const [dryErr, setDryErr] = useState('');
  // v0.10.115 — şema kataloğu (SQLCODE'lu Explain'lerde kolon tanımı).
  // Kolon içeriği ekrana DÖNMEZ; yalnız sayı/tarih. CSV metin olarak
  // yüklenir (dosya seçici de okuyup metne çevirir).
  const [schema, setSchema] = useState<SchemaCatalogSummary | null>(null);
  const [schemaFlavor, setSchemaFlavor] = useState('db2');
  const [schemaCsv, setSchemaCsv] = useState('');
  const [schemaBusy, setSchemaBusy] = useState(false);
  const [schemaMsg, setSchemaMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const loadSchema = () => api.getSchemaCatalog().then(setSchema).catch(() => setSchema(null));
  const importSchema = async (e: FormEvent) => {
    e.preventDefault();
    if (!schemaCsv.trim()) return;
    setSchemaBusy(true); setSchemaMsg(null);
    try {
      const out = await api.putSchemaCatalog({ csv: schemaCsv, flavor: schemaFlavor, source: 'settings-upload' });
      setSchema(out); setSchemaCsv('');
      setSchemaMsg({ kind: 'ok', text: `${out.tables} tablo · ${out.columns} kolon yüklendi.` });
    } catch (err) {
      setSchemaMsg({ kind: 'err', text: String((err as Error).message || err) });
    } finally { setSchemaBusy(false); }
  };
  const clearSchema = async () => {
    // v0.10.133 — yıkıcı: katalog sunucudan silinir, geri dönüşü CSV'yi
    // yeniden yüklemek (destructiveConfirm kapısı; v0.10.115'te onaysızdı).
    if (!await confirm({
      title: 'Şema kataloğu silinsin mi?',
      body: <>Kayıtlı DB2/Oracle kolon kataloğu sunucudan kaldırılacak; SQL hatası açıklamaları
        yeni bir CSV yüklenene kadar <b>şema bağlamı olmadan</b> üretilecek.</>,
      confirmLabel: 'Kataloğu sil',
      danger: true,
    })) return;
    setSchemaBusy(true); setSchemaMsg(null);
    try { setSchema(await api.deleteSchemaCatalog()); setSchemaMsg({ kind: 'ok', text: 'Katalog temizlendi.' }); }
    catch (err) { setSchemaMsg({ kind: 'err', text: String((err as Error).message || err) }); }
    finally { setSchemaBusy(false); }
  };
  const readCsvFile = (f: File | undefined) => {
    if (!f) return;
    const rd = new FileReader();
    rd.onload = () => setSchemaCsv(String(rd.result || ''));
    rd.readAsText(f);
  };

  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.getDevOpsSettings(),
    s => {
      void loadSchema();
      setBaseUrl(s.baseUrl || '');
      setCollection(s.collection || '');
      setProject(s.project || '');
      setUsername(s.username || '');
      setHasPat(s.hasPat);
      setFlavor((s.flavor || 'auto') as DevOpsFlavor);
      setInsecure(!!s.insecureSkipVerify);
      setCodeSearch(!!s.codeSearch);
      setRepoPrefixes((s.repoPrefixes || []).join(', '));
      setBranchOrder((s.branchOrder || []).join(', '));
      setAppPrefixes((s.appPrefixes || []).join(', '));
      setLookupLimit(s.codeLookupLimit ? String(s.codeLookupLimit) : '');
      setEffectiveLimit(s.effectiveLookupLimit || 6);
      setDetected({ flavor: s.detectedFlavor, apiVersion: s.detectedApiVersion });
    },
  );

  // PAT boşsa gövdeden tamamen çıkar — sunucu sözleşmesi "boş =
  // saklıyı koru", ama alanı hiç göndermemek niyeti daha net
  // ifade ediyor ve aynı yola düşüyor.
  const buildInput = () => ({
    baseUrl, collection, project, username, flavor,
    insecureSkipVerify: insecure,
    codeSearch,
    repoPrefixes: splitList(repoPrefixes),
    branchOrder: splitList(branchOrder),
    appPrefixes: splitList(appPrefixes),
    codeLookupLimit: Math.max(0, parseInt(lookupLimit, 10) || 0),
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

  // runDryRun — servis → depo/branş/ağaç provası (v0.9.1242).
  //
  // Sunucu başarısızlığı 200 + {ok:false, steps} olarak döner (test
  // ucunun duruşu): "depo bulunamadı" operatörün sorusuna verilmiş
  // BAŞARILI bir cevaptır. catch yalnız gerçek taşıma/yetki hataları
  // için — o hâlde adım listesi yoktur, tek satır hata gösterilir.
  const runDryRun = async (e: FormEvent) => {
    e.preventDefault();
    const svc = dryService.trim();
    if (!svc) return;
    setDryBusy(true); setDry(null); setDryErr('');
    try {
      setDry(await api.resolveDevOpsDryRun(svc));
    } catch (err) {
      setDryErr(err instanceof Error ? err.message : 'Çözüm denemesi başarısız');
    } finally {
      setDryBusy(false);
    }
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putDevOpsSettings(buildInput());
      setHasPat(next.hasPat);
      setPat('');
      // Sunucu konvansiyonu ÇÖZÜLMÜŞ döner: alanı boş bırakan operatör
      // kaydettiği anda varsayılanı kutuda görür.
      setRepoPrefixes((next.repoPrefixes || []).join(', '));
      setBranchOrder((next.branchOrder || []).join(', '));
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

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
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
        Yapılandırıldığında CoSRE, exception ve trace açıklamalarında
        stack trace’teki uygulama satırlarının <strong style={{ color: 'var(--text)' }}>
        kaynak kodunu</strong> da okuyabilir (AI çekmecesindeki
        “Kodu da incele” kutusu). Kod yalnız modele gider; AI çağrı
        kaydına kod gövdesi değil, yalnız <code>dosya:aralık</code> özeti yazılır.
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
            placeholder="boş bırakılırsa servis önekinden türetilir (bsa- → BSA)"
            style={{ width: '100%' }} />
          {/* v0.9.1183 — boş Project'in ARTIK iki anlamı var ve ikisi de
              burada yazılı olmalı: bağlantı testi hâlâ yalnız koleksiyonu
              doğrular, ama kod çekimi artık servis önekinden proje türetiyor.
              Yazılmazsa operatör türetmenin varlığını hiç öğrenemez —
              tam da bu alan boş kaldığı için kod bağlamı sessizce
              çalışmıyordu. */}
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Bağlantı testi boşken yalnız koleksiyonu doğrular. <b>Kod çekiminde</b> boş
            bırakılırsa proje, servis adının eşleşen önekinden türetilir
            (<code>bsa-…</code> → <code>BSA</code>). Buraya yazılan değer türetmeyi ezer.
          </div>
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

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Servis adı önekleri <span style={{ color: 'var(--text3)' }}>(virgülle)</span>
          </div>
          <input value={repoPrefixes}
            onChange={e => setRepoPrefixes(e.target.value)}
            placeholder="bsa-"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Servis adından depo adı türetilirken soyulur; ortam eki
            (<code>-prod/-int/-uat/-prep</code>) her hâlükârda soyulur.
            Örnek: <code>bsa-odeme-servisi-prod</code> → <code>odeme-servisi</code>.
            Katalogdaki <strong>Repository</strong> alanı doluysa O kazanır —
            elle pin konvansiyonu ezer.
          </div>
        </label>

        {/* v0.10.112 — UYGULAMA PAKET ÖNEKLERİ + DENEME TAVANI.
            Kurum-içi çerçeve sınıfları (RestFilter, BasicDispatcher…) JDK/
            Spring listesinde olmadığı için uygulama sayılıyor ve stack'te
            iş sınıfından önce geldikleri için tavanı önce onlar yiyordu.
            Önek listesi iş sınıflarını başa alır; tavan ayarlanabilir. */}
        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Uygulama paket önekleri <span style={{ color: 'var(--text3)' }}>(virgülle)</span>
          </div>
          <input value={appPrefixes}
            onChange={e => setAppPrefixes(e.target.value)}
            placeholder="com.banka.odeme., com.banka.kart."
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Bu öneklerle başlayan stack frame'leri <strong>önce</strong> denenir;
            kurum-içi çerçeve ve kütüphane sınıfları (ör. <code>com.banka.core.rest.*</code>)
            arkaya düşer, atılmaz. Boş = stack sırası (yalnız JDK/Spring/JBoss elenir).
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Kod çekme deneme tavanı <span style={{ color: 'var(--text3)' }}>(1–30, boş = varsayılan)</span>
          </div>
          <input value={lookupLimit} inputMode="numeric"
            onChange={e => setLookupLimit(e.target.value.replace(/[^0-9]/g, ''))}
            placeholder={String(effectiveLimit)}
            style={{ width: 120 }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Bir açıklama için en fazla kaç dosya çekilir. Ağaçta bulunamayan frame
            ve aynı dosyanın başka satırı tavandan düşmez. Yürürlükte: <strong>{effectiveLimit}</strong>.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Branş sırası <span style={{ color: 'var(--text3)' }}>(virgülle)</span>
          </div>
          <input value={branchOrder}
            onChange={e => setBranchOrder(e.target.value)}
            placeholder="release, master"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            İlk <strong>var olan</strong> branş kullanılır. Hiçbiri yoksa deponun
            kendi varsayılan branşına düşülür.
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

        {/* v0.10.75 — ORGANİZASYON KOD ARAMASI.
            Depo servis adından konvansiyonla çözülüyor; hatanın atıldığı
            sınıf başka bir depoda olduğunda konvansiyon onu bulamıyor.
            Arama o boşluğu kapatıyor ama AYRI bir uzantı ve PAT kapsamı
            istiyor — o yüzden varsayılan kapalı ve ne yaptığı burada
            yazılı. */}
        <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, cursor: 'pointer' }}>
          <input type="checkbox" checked={codeSearch} style={{ marginTop: 3 }}
            onChange={e => setCodeSearch(e.target.checked)} />
          <span style={{ fontSize: 13 }}>
            Organizasyon geneli kod araması
            <span style={{ display: 'block', marginTop: 2, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
              Depo ağacında bulunamayan stack frame'leri için tüm organizasyonda
              <code style={{ margin: '0 4px' }}>Sınıf.metot</code>
              aranır ve bulunan deponun kendisinden çekilir. Azure DevOps
              <b> Code Search</b> uzantısı ve PAT'te <b>Code (Read)</b> kapsamı
              gerekir; yoksa açıklama yine üretilir, künyeye “kod araması
              başarısız” yazılır.
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
          <Button type="button" variant="secondary" disabled={!configured}
            onClick={runTest} loading={busy}>
            Bağlantıyı test et
          </Button>
          <Button type="submit" variant="primary" loading={busy}>
            Kaydet
          </Button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 8 }}>
          Test kaydetmez — formdaki değerlerle dener. PAT alanı boşsa saklı
          token kullanılır.
        </div>
      </form>

      {/* v0.9.1242 — çözüm provası. Ayrı bir <form>: ayar formunun
          İÇİNDE olsaydı servis adı kutusunda Enter'a basmak ayarları
          kaydederdi. Zincir KAYITLI ayarlarla koşar, o yüzden de
          burada — kaydetmeden denemek başka bir sorunun cevabı. */}
      <form onSubmit={runDryRun} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 6 }}>Çözümü dene</h3>
        <p style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12 }}>
          Bir servis adı yazın; <strong style={{ color: 'var(--text)' }}>aynı çözüm
          zinciri</strong> (katalog pini → depo adı → proje → branş → depo ağacı)
          AI’a hiç uğramadan koşar. Konvansiyonu denemek için artık gerçek bir
          exception ve tam bir LLM turu gerekmiyor. Salt okuma: hiçbir şey
          kaydedilmez, kod-çekme isabet sayaçları etkilenmez.
        </p>
        <Field label="Servis adı" value={dryService}
          onChange={e => setDryService(e.target.value)}
          placeholder="bsa-odeme-servisi-prod"
          hint={configured
            ? 'Kayıtlı ayarlarla denenir — kaydetmediğiniz değişiklikler hesaba katılmaz.'
            : 'Önce sunucu adresini girip kaydedin.'} />
        <div style={{ display: 'flex', gap: 8, marginTop: 4 }}>
          <Button type="submit" variant="secondary"
            disabled={!configured || !dryService.trim()} loading={dryBusy}>
            Çözümü dene
          </Button>
        </div>

        {dryBusy && <div style={{ marginTop: 12 }}><Spinner /></div>}

        {!dryBusy && dryErr && (
          <div style={{ marginTop: 12, fontSize: 12, color: 'var(--err)' }}>✗ {dryErr}</div>
        )}

        {!dryBusy && !dryErr && dry && (
          <div style={{ marginTop: 12 }}>
            {/* Özet satırı — adımlardan türer, çözülemeyen alan hiç yazılmaz. */}
            <div style={{ fontSize: 12, marginBottom: 8, color: 'var(--text2)' }}>
              <Badge tone={dry.ok ? 'success' : 'danger'}>
                {dry.ok ? 'ÇÖZÜLDÜ' : 'ÇÖZÜLEMEDİ'}
              </Badge>
              <span style={{ marginLeft: 8 }}>
                {dry.repo
                  ? <>
                      <strong style={{ color: 'var(--text)' }}>{dry.repo}</strong>
                      {dry.branch ? ` @ ${dry.branch}` : ''}
                      {dry.project ? ` · proje ${dry.project}` : ''}
                      {dry.fileCount ? ` · ${dry.fileCount} dosya` : ''}
                    </>
                  : `“${dry.service}” için depo adı üretilemedi.`}
              </span>
            </div>
            <ol style={{ listStyle: 'none', padding: 0, margin: 0,
              display: 'flex', flexDirection: 'column', gap: 6 }}>
              {(dry.steps || []).map((st, i) => (
                <li key={`${st.key}-${i}`}
                  style={{ display: 'flex', gap: 8, alignItems: 'baseline', fontSize: 12 }}>
                  {/* v0.10.58 — ÜÇ DURUM. "Depo adı" ve "Proje" adımları
                      DevOps'a sorulmadan, saf türetmeyle üretiliyor; onlara
                      yeşil tik vermek operatöre "doğrulandı" der ve tam da
                      bu ekranın cevaplaması gereken soruyu ("depo doğru mu")
                      cevaplamış gibi yapar. Operatör bildirdi: "Kodu incele
                      dediğimde doğru repoyu bulmuyor" — ekranda iki yeşil
                      tik dururken. */}
                  <Badge tone={st.derived ? 'info' : st.ok ? 'success' : 'danger'}>
                    {st.derived ? '~' : st.ok ? '✓' : '✗'}
                  </Badge>
                  <span style={{ minWidth: 110, color: 'var(--text2)' }}>{st.label}</span>
                  {/* Uzun gerekçe (çıkmaz cümleleri üç kaynağı birden
                      anlatıyor) kırpılmaz: kesilen bir teşhis, teşhis
                      değildir. */}
                  <span style={{ color: st.derived ? 'var(--text2)' : st.ok ? 'var(--text)' : 'var(--err)',
                    wordBreak: 'break-word' }}>{st.detail}</span>
                </li>
              ))}
            </ol>
            {/* Zincir ilk kırmızıda durur; koşmamış adımlar listede YOK. */}
            {!dry.ok && (
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 8 }}>
                Zincir ilk hatada durdu — sonraki adımlar hiç çalıştırılmadı.
              </div>
            )}
          </div>
        )}
      </form>

      {/* v0.10.115 — ŞEMA KATALOĞU. SQLCODE/SQLSTATE'li hatalarda model hedef
          kolonun tipini/uzunluğunu bilmediği için tahmin yürütüyordu
          ("muhtemelen telefon numarası"). Katalog SALT-OKUNUR bir anlık
          görüntü: operatör aşağıdaki SELECT'i kendi tarafında koşturur,
          CSV'yi yükler. Canlı DB bağlantısı yok — sürücü, kimlik, ağ yok. */}
      <form onSubmit={importSchema} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 6 }}>Şema kataloğu (SQL hataları için)</h3>
        <p style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 10 }}>
          <code>SQLCODE</code>/<code>SQLSTATE</code>/<code>ORA-</code> taşıyan hatalarda AI,
          hedef tablonun <strong style={{ color: 'var(--text)' }}>kolon tanımlarını</strong>
          (tip, uzunluk, NULL) kanıt olarak alır. Aşağıdaki salt-okunur sorguyu DB
          tarafında koşturup CSV çıktısını yükleyin; katalog Coremetry'de saklanır,
          DB'ye bağlantı kurulmaz.
        </p>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8, flexWrap: 'wrap' }}>
          {schema && schema.importedAt > 0 ? (
            <Badge tone="success">{schema.tables} tablo · {schema.columns} kolon · {fmtDateTime(new Date(schema.importedAt))}{schema.flavor ? ` · ${schema.flavor}` : ''}</Badge>
          ) : (
            <Badge>katalog yüklü değil</Badge>
          )}
          <label style={{ fontSize: 12, color: 'var(--text2)' }}>
            Veritabanı{' '}
            <select value={schemaFlavor} onChange={e => setSchemaFlavor(e.target.value)}>
              {['db2', 'oracle', 'postgres', 'mysql', 'mssql'].map(f => <option key={f} value={f}>{f}</option>)}
            </select>
          </label>
        </div>
        <details style={{ marginBottom: 8 }}>
          <summary style={{ fontSize: 12, color: 'var(--text2)', cursor: 'pointer' }}>Katalog sorgusu ({schemaFlavor}) — DB tarafında koşturun, çıktıyı CSV alın</summary>
          <pre style={{ fontSize: 11, marginTop: 6, padding: 8, background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 6, overflowX: 'auto', whiteSpace: 'pre-wrap' }}>
            {schema?.snapshotSql?.[schemaFlavor] || '—'}
          </pre>
        </details>
        <label style={{ display: 'block', marginBottom: 8 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            CSV (başlık satırlı; virgül / noktalı virgül / sekme) — yapıştırın ya da dosya seçin
          </div>
          <textarea value={schemaCsv} onChange={e => setSchemaCsv(e.target.value)} rows={5}
            placeholder={'TABSCHEMA,TABNAME,COLNAME,TYPENAME,LENGTH,SCALE,NULLS\nBSA,INT_TFRAUD,TELNO,VARCHAR,10,0,N'}
            className="mono" style={{ width: '100%' }} />
          <input type="file" accept=".csv,.txt,text/csv" style={{ marginTop: 6, fontSize: 12 }}
            onChange={e => readCsvFile(e.target.files?.[0])} />
        </label>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <Button type="submit" variant="primary" disabled={!schemaCsv.trim()} loading={schemaBusy}>Kataloğu yükle</Button>
          <Button type="button" variant="danger" disabled={!schema || schema.importedAt === 0 || schemaBusy} onClick={clearSchema}>Kataloğu sil</Button>
          {schemaMsg && <span style={{ fontSize: 12, color: schemaMsg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>{schemaMsg.text}</span>}
        </div>
      </form>
    </div>
  );
}
