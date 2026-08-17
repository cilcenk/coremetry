import { useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { useSettingsLoad, SettingsLoadError } from './shared';
import { vmFloorToForm, vmFloorToWire } from './vmForm';
import type { VMAuthType, VMSettingsInput, VMTestResult } from '@/lib/types';

// MetricsBackendTab — external VictoriaMetrics READ backend (v0.9.1150,
// Faz 1). TempoTab is the template; the Test button follows DevOpsTab.
//
// The copy is deliberate about SCOPE, because "metrics come from
// VictoriaMetrics now" is not what this switch does. It repoints the
// surfaces an operator drives by hand (catalogue + picker, Explore,
// dashboard metric panels, MCP query_metric, filter suggestions). Every
// span-derived surface and the fixed-name infra panels stay on
// ClickHouse. An operator who reads "metrics backend" as "all metrics"
// will file a bug the first time a JVM panel disagrees, so the form says
// it up front.
//
// Admin-only (the whole Settings area is), and the saved token reads the
// operator's entire VM.
export function MetricsBackendTab() {
  const [enabled, setEnabled] = useState(false);
  const [baseUrl, setBaseUrl] = useState('');
  const [authType, setAuthType] = useState<VMAuthType>('none');
  const [token, setToken] = useState('');
  const [hasToken, setHasToken] = useState(false);
  const [insecureSkipVerify, setInsecureSkipVerify] = useState(false);
  // DİZİ state, sayı değil — '' tek dürüst "üstüne yazma yok" gösterimi.
  // Çeviri vmForm.ts'te ve tablo-testli; gerekçesi orada.
  const [rateWindowFloor, setRateWindowFloor] = useState('');
  const [allowUnfilteredPercentiles, setAllowUnfilteredPercentiles] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [test, setTest] = useState<VMTestResult | null>(null);

  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.getVMSettings(),
    s => {
      setEnabled(s.enabled);
      setBaseUrl(s.baseUrl || '');
      setAuthType((s.authType || 'none') as VMAuthType);
      setHasToken(s.hasToken);
      setInsecureSkipVerify(!!s.insecureSkipVerify);
      setRateWindowFloor(vmFloorToForm(s.rateWindowFloorS));
      setAllowUnfilteredPercentiles(!!s.allowUnfilteredPercentiles);
    },
  );

  const buildInput = (): VMSettingsInput => ({
    enabled, baseUrl, authType,
    token, // empty preserved on the server side
    insecureSkipVerify,
    // Bu ikisinde "boş = sakla" kuralı YOK: 0 ve false burada anlamlı
    // değerler ("varsayılan taban", "koruma açık"), token'ın tersine.
    rateWindowFloorS: vmFloorToWire(rateWindowFloor),
    allowUnfilteredPercentiles,
  });

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null); setTest(null);
    try {
      const next = await api.putVMSettings(buildInput());
      setHasToken(next.hasToken);
      setToken('');
      // Sunucu NE SAKLADIĞININ tek yetkilisi: dönen snapshot'tan yeniden
      // tohumla. Aralık dışı bir değer 400 atar (buraya hiç gelmez) ve kutu
      // operatörün yazdığını hata mesajıyla birlikte tutar — doğru olan bu.
      setRateWindowFloor(vmFloorToForm(next.rateWindowFloorS));
      setAllowUnfilteredPercentiles(!!next.allowUnfilteredPercentiles);
      setMsg({
        kind: 'ok',
        text: next.enabled
          ? 'Kaydedildi — metrik keşif ve sorgu yüzeyleri artık VictoriaMetrics’ten okuyor.'
          : 'Kaydedildi — metrik okumaları ClickHouse’ta.',
      });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Kaydetme başarısız' });
    } finally {
      setBusy(false);
    }
  };

  // Test probes the values IN THE FORM without saving — the operator
  // pastes a URL, verifies it, and only then ticks Enabled. The server
  // treats the submitted form as enabled for the probe's duration.
  const runTest = async () => {
    setBusy(true); setMsg(null); setTest(null);
    try {
      setTest(await api.testVMSettings(buildInput()));
    } catch (err) {
      setTest({ ok: false, error: err instanceof Error ? err.message : 'Test başarısız' });
    } finally {
      setBusy(false);
    }
  };

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
  if (!loaded) return <Spinner />;

  const ready = enabled && baseUrl.trim().length > 0;

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Metrik okuma backend’i</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Zaten VictoriaMetrics çalıştıran kurulumlar için: aynı serilerin bir
        kopyasını Coremetry’nin ClickHouse’unda tutmak yerine metrikleri
        doğrudan VM’den okuyun (Prometheus uyumlu HTTP API).
      </p>

      <div className={`status-banner status-banner-${ready ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${ready ? 'operational' : 'degraded'}`}>
          {ready ? 'VICTORIAMETRICS' : 'CLICKHOUSE'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {ready
            ? `Metrik okumaları ${baseUrl} adresinden.`
            : 'Kapalı — metrik okumaları Coremetry’nin ClickHouse’undan.'}
        </span>
      </div>

      {/* KAPSAM. Bu satır olmadan "metrik backend'i" ifadesi "TÜM
          metrikler" diye okunur ve ilk JVM paneli uyuşmadığında bug
          olarak açılır. */}
      <div style={{
        marginTop: 12, padding: '10px 12px', borderRadius: 6,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        fontSize: 12, color: 'var(--text2)', lineHeight: 1.6,
      }}>
        <b style={{ color: 'var(--text)' }}>Neyi kapsar:</b> metrik kataloğu ve
        adı seçiciler, Explore, dashboard <code>metric</code> panelleri, filtre
        anahtarı/değeri önerileri, MCP <code>query_metric</code> ve{' '}
        <code>list_metric_names</code>. Histogram ısı haritaları,
        yüzdelikler (p50/p95/p99) ve PromQL vekili de dahil (Faz 2).
        <br />
        <b style={{ color: 'var(--text)' }}>Neyi kapsamaz (ClickHouse’ta kalır):</b>{' '}
        span türevli her şey (servisler, operasyonlar, topoloji, trace’ler,
        exception’lar) ve sabit adlı iç okuyucular — hosts, altyapı, JVM
        panelleri, veritabanı kapasitesi.
        <br />
        <b style={{ color: 'var(--text)' }}>Yedeğe düşmez:</b> VM açık ve
        erişilemez durumdaysa metrik uçları <b>502</b> ile VM’nin kendi
        hatasını döner. Sessizce ClickHouse’a düşmek, sormadığınız bir
        kaynaktan gelen sayıları göstermek olurdu.
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={enabled}
            onChange={e => setEnabled(e.target.checked)} />
          <span style={{ fontSize: 13 }}>VictoriaMetrics’ten oku</span>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Base URL</div>
          <input value={baseUrl}
            onChange={e => setBaseUrl(e.target.value)}
            placeholder="http://victoria-metrics:8428  ·  cluster: http://vmselect:8481/select/0/prometheus"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Sondaki eğik çizgi opsiyonel. Çağrılan uçlar:{' '}
            <code>/api/v1/query_range</code>, <code>/api/v1/labels</code>,{' '}
            <code>/api/v1/label/&#123;name&#125;/values</code>. Tek düğüm için
            vmsingle adresi, cluster için <b>vmselect</b>’in{' '}
            <code>/select/&lt;accountID&gt;/prometheus</code> ön eki.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Kimlik doğrulama</div>
          <select value={authType}
            onChange={e => setAuthType(e.target.value as VMAuthType)}
            style={{ width: '100%' }}>
            <option value="none">Yok (VM’in kendisinde kimlik doğrulama yoktur)</option>
            <option value="bearer">Bearer token (vmauth / JWT’li ingress)</option>
          </select>
        </label>

        {authType === 'bearer' && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              Bearer token
              {hasToken && <span style={{ color: 'var(--ok)', marginLeft: 8 }}>· saklı</span>}
            </div>
            <input type="password" value={token}
              onChange={e => setToken(e.target.value)}
              placeholder={hasToken ? '(saklı değeri korumak için boş bırakın)' : 'token’ı yapıştırın…'}
              style={{ width: '100%' }} />
          </label>
        )}

        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={insecureSkipVerify}
            onChange={e => setInsecureSkipVerify(e.target.checked)} />
          <span style={{ fontSize: 13 }}>
            TLS doğrulamayı atla
            <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
              (yalnız kendinden imzalı sertifika / POC)
            </span>
          </span>
        </label>

        {/* SORGU AYARLARI — bağlantı değil, gönderilen ifadenin ŞEKLİ.
            Ayrı bir bölüm, çünkü ikisi de "VM'e nasıl bağlanılır"
            sorusuna değil "VM'e ne sorulur" sorusuna cevap veriyor ve
            biri (koruma) kapatıldığında etkisi tüm kurulumda görülür. */}
        <div style={{
          marginTop: 4, marginBottom: 12, paddingTop: 12,
          borderTop: '1px solid var(--border)',
        }}>
          <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>Sorgu ayarları</div>

          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              Rate penceresi tabanı (s)
            </div>
            <input value={rateWindowFloor}
              onChange={e => setRateWindowFloor(e.target.value)}
              inputMode="numeric"
              placeholder="300 (varsayılan)"
              style={{ width: '100%' }} />
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
              <code>rate</code> ve <code>last</code> çevirilerinin en dar
              lookbehind penceresi. VM, yazılmamış bir pencereyi{' '}
              <code>max(step, scrape_interval)</code>’a genişletir; Coremetry
              pencereyi hep açıkça yazdığı için o genişletme bize kalıyor ve
              scrape aralığı VM’den okunamıyor — bu taban onun yerine geçer.
              Export aralığı bilinen bir kurulumda 300s gereğinden geniş
              olabilir (her rate grafiğinden beş dakikalık detayı düzler).
              Boş bırakın = varsayılan; aksi hâlde <b>10–3600</b> sn.
              <br />
              <b style={{ color: 'var(--text2)' }}>Etkilemediği yerler:</b>{' '}
              <code>increase</code> ve histogram ısı haritası. Onların
              penceresi bir TOPLAM: genişletmek grafik hâlâ “bir kova” derken
              sayıyı katlar.
            </div>
          </label>

          <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, marginBottom: 4 }}>
            <input type="checkbox" checked={allowUnfilteredPercentiles}
              onChange={e => setAllowUnfilteredPercentiles(e.target.checked)}
              style={{ marginTop: 3 }} />
            <span style={{ fontSize: 13 }}>
              Filtresiz yüzdeliklere izin ver
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 3, lineHeight: 1.5 }}>
                Varsayılan olarak <b>kapalı</b>: yüzdelik (p50/p95/p99),
                histogram ısı haritası ve histogram ailesindeki{' '}
                <code>avg</code> sorguları <b>ne servis ne de etiket filtresi</b>{' '}
                taşıdığında <b>400</b> ile reddedilir — böyle bir sorgu kova
                serilerinin tamamını taratır ve milyonlarca serili bir VM’de
                paylaşılan <code>vmselect</code>’i herkes için yavaşlatır.
                Reddedilen sorgu ekranda ne yapılacağını söyler. Açmak eski
                davranışa döner; <code>rate</code>/<code>increase</code>{' '}
                zaten korumaya girmez (kolları <code>_count</code> okur, kova
                değil).
              </div>
            </span>
          </label>
        </div>

        {test && (
          <div style={{
            marginBottom: 12, fontSize: 12,
            color: test.ok ? 'var(--ok)' : 'var(--err)',
          }}>
            {test.ok
              ? <>✓ Bağlantı kuruldu — VM Prometheus API’siyle cevap veriyor.</>
              : <>✗ {test.error || 'Bağlantı kurulamadı'}</>}
          </div>
        )}

        {msg && (
          <div style={{
            marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <Button type="submit" variant="primary" loading={busy}>
            Kaydet
          </Button>
          <Button type="button" variant="secondary" disabled={busy || !baseUrl.trim()}
            onClick={() => void runTest()}>
            Bağlantıyı test et
          </Button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 8, lineHeight: 1.5 }}>
          Test kaydetmez — formdaki değerlerle <code>up</code> sorgusunu dener
          (5 sn). Token alanı boşsa saklı token kullanılır. Boş sonuç da
          BAŞARIDIR: VM erişilebilir ve API’yi konuşuyor demektir.
        </div>
      </form>
    </div>
  );
}
