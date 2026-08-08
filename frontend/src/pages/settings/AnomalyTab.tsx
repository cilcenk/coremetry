import { useEffect, useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { AnomalyTrackedConfig, AnomalySensitivityConfig, ExceptionTriageConfig } from '@/lib/types';
import { Field, FlashBox, humanize } from './shared';

// ── Anomaly promotion tab ───────────────────────────────────────
//
// Tunes the evaluator's anomaly auto-promotion (v0.5.59). The
// detector continuously flags "pattern X is occurring N× more
// than baseline" rows on /anomalies; when they sustain past
// the configured thresholds the evaluator graduates them to
// first-class Problems so the existing notify pipeline pages
// the on-call. Master enable flag lets operators kill the
// feature for a chatty detector without changing thresholds.
export function AnomalyPromotionTab() {
  type Cfg = {
    enabled: boolean; minPeakRatio: number; criticalPeakRatio: number;
    minSustainedSec: number; minCount: number;
  };
  const [cfg, setCfg] = useState<Cfg | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAnomalyPromotion()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putAnomalyPromotion(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Saved — next evaluator tick picks it up automatically.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  if (!cfg) {
    return (
      <div style={{ maxWidth: 640 }}>
        <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Anomaly auto-promotion</h2>
        {flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />}
      </div>
    );
  }

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Anomaly auto-promotion</h2>
      {/* v0.9.827 — DÜRÜSTLÜK DÜZELTMESİ.
          Eski metin "the anomaly detector" diyordu ve bu, sayfadaki en
          pahalı yanlış anlamaydı: operatör bu kutucuğu kapatıp METRİK
          dedektörünün susacağını sanıyordu. Aşağıdaki vidalar YALNIZ
          log/trace desen anomalilerinin (recorder → AnomalyEvent)
          terfi hattını yönetiyor. Metrik dedektörü (error_rate, p99,
          istek hızı) tamamen ayrı bir hat ve eşikleri "Dedektör
          hassasiyeti" bölümünde. */}
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 10, lineHeight: 1.55 }}>
        Log ve iz (trace) verisinde tekrarlayan desenler kendi hareketli
        ortalamalarına göre işaretlenir; bu bölüm o işaretlerden hangilerinin
        birinci sınıf <b>Problem</b>&apos;e terfi edeceğini ayarlar. Dedektör çok
        konuşkansa eşikleri sıkın, ya da kalibre ederken tümden kapatın.
      </p>
      <p style={{
        fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55,
        padding: '8px 10px', border: '1px solid var(--border)', borderRadius: 4,
      }}>
        <b>Kapsam:</b> bu vidalar yalnız <b>log/iz desen anomalilerinin terfi
        hattını</b> yönetir. <b>Metrik</b> dedektörü (hata oranı, P99 gecikme,
        istek hızı) ayrı bir hattır ve bu kutucuktan etkilenmez &mdash; onun
        eşikleri aşağıdaki <b>Dedektör hassasiyeti</b> bölümünde.
      </p>

      <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 16 }}>
        <input type="checkbox" checked={cfg.enabled}
          onChange={e => setCfg({ ...cfg, enabled: e.target.checked })} />
        <span style={{ fontSize: 13, color: 'var(--text)' }}>
          Güçlü log/iz anomalilerini Problem&apos;e terfi ettir
        </span>
      </label>

      <div style={{ display: 'grid', gap: 12, opacity: cfg.enabled ? 1 : 0.5 }}>
        <Field label="Minimum peak ratio (× baseline)">
          <input type="number" min={1} max={1000} step={0.5}
            value={cfg.minPeakRatio}
            onChange={e => setCfg({ ...cfg, minPeakRatio: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            5× = pattern is occurring at least 5 times more than its
            rolling baseline. Default 5.
          </div>
        </Field>

        {/* v0.9.247 — severity cut-off, previously a hard-coded 20 in the
            evaluator. It sat invisibly ABOVE the promotion gate, so an
            operator raising "minimum peak ratio" to 20+ to get FEWER pages
            silently turned every surviving promotion critical. Surfacing it
            makes the two decisions independent: the gate controls HOW MANY
            promote, this controls how many of those page. */}
        <Field label="Critical at peak ratio (x baseline)">
          <input type="number" min={cfg.minPeakRatio} max={1000} step={0.5}
            value={cfg.criticalPeakRatio}
            onChange={e => setCfg({ ...cfg, criticalPeakRatio: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            At or above this ratio a promoted anomaly opens as <b>critical</b>;
            below it, <b>warning</b>. Must be &ge; the minimum peak ratio &mdash;
            setting them equal means every promoted anomaly pages. Default 20.
            {cfg.criticalPeakRatio <= cfg.minPeakRatio && (
              <div style={{ color: 'var(--warn)', marginTop: 4 }}>
                Su an esit: promote edilen HER anomali critical acilir.
              </div>
            )}
          </div>
        </Field>

        <Field label="Minimum sustained (seconds since started_at)">
          <input type="number" min={60} max={86400} step={60}
            value={cfg.minSustainedSec}
            onChange={e => setCfg({ ...cfg, minSustainedSec: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Filters out one-tick flares. Default 300s (5 min).
          </div>
        </Field>

        <Field label="Minimum count">
          <input type="number" min={1} max={1000000} step={1}
            value={cfg.minCount}
            onChange={e => setCfg({ ...cfg, minCount: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Absolute volume floor — a 100× ratio on 2 occurrences is
            meaningless. Default 10.
          </div>
        </Field>
      </div>

      <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
        <Button variant="primary" onClick={save} disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
        {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
      </div>

      <hr style={{ border: 0, borderTop: '1px solid var(--border)', margin: '28px 0 22px' }} />
      <TrackedMetricsSection />

      <hr style={{ border: 0, borderTop: '1px solid var(--border)', margin: '28px 0 22px' }} />
      <SensitivitySection />

      <hr style={{ border: 0, borderTop: '1px solid var(--border)', margin: '28px 0 22px' }} />
      <EscalationSection />

      <hr style={{ border: 0, borderTop: '1px solid var(--border)', margin: '28px 0 22px' }} />
      <ExceptionTriageSection />
    </div>
  );
}

// ── İzlenen metrikler (v0.9.800) ────────────────────────────────
//
// Yukarıdaki bölüm bir sinyalin Problem'e TERFİ edip etmeyeceğini
// ayarlıyor; bu bölüm sinyalin hiç ÖLÇÜLÜP ölçülmeyeceğini. İkisi ayrı
// sorular: promosyonu kısmak gürültüyü azaltır ama metrik yine
// hesaplanır ve /anomalies'te görünür.
//
// request_rate VARSAYILAN OLARAK KAPALI (operatör 2026-08-09): istek
// hacmi bu filoda kampanya, batch penceresi ve nöbet devriyle normalde
// de sıçrayıp düşüyor — "istek hızı beklenenden farklı" bir olay değil.
// Metrik koddan sökülmedi, vidaya bağlandı: geri açmak tek tık.
const TRACKED_METRICS: { key: string; label: string; hint: string }[] = [
  { key: 'error_rate', label: 'Hata oranı (error_rate)', hint: 'Yükselen hata oranı. Gerçek olay sinyali — açık kalması önerilir.' },
  { key: 'p99_ms', label: 'P99 gecikme (p99_ms)', hint: 'Yükselen kuyruk gecikmesi. Gerçek olay sinyali — açık kalması önerilir.' },
  { key: 'request_rate', label: 'İstek hızı (request_rate)', hint: 'Hacim sıçraması VE düşüşü. Varsayılan kapalı: false-pozitif üretiyor (kampanya, batch, nöbet devri).' },
];

function TrackedMetricsSection() {
  const [cfg, setCfg] = useState<AnomalyTrackedConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAnomalyTracked()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putAnomalyTracked(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Kaydedildi — dedektör bir sonraki taramada yeni seti kullanır.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  // En az biri açık kalmalı. Backend zaten 400 döner; operatör kaydet
  // düğmesine basmadan görmeli — motoru tümden kapatmanın yolu bu vida
  // değil (Background ayarları).
  const noneOn = !!cfg && !TRACKED_METRICS.some(m => cfg[m.key]);

  return (
    <div>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>İzlenen metrikler</h2>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55 }}>
        Dedektör her taramada yalnız burada seçili metrikleri ölçer: kapalı bir metrik
        için baseline sorgusu bile açılmaz, dolayısıyla ne yeni tespit üretilir ne de
        tarama maliyeti ödenir. Üstteki promosyon kapısından farkı şu &mdash; o, ölçülen
        bir sapmanın <b>Problem&apos;e dönüşüp dönüşmeyeceğini</b> ayarlar; bu ise
        sapmanın <b>hiç aranıp aranmayacağını</b>.
      </p>

      {!cfg ? (
        flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />
      ) : (
        <>
          <div style={{ display: 'grid', gap: 12 }}>
            {TRACKED_METRICS.map(m => (
              <div key={m.key}>
                <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <input type="checkbox" checked={!!cfg[m.key]}
                    onChange={e => setCfg({ ...cfg, [m.key]: e.target.checked })} />
                  <span style={{ fontSize: 13, color: 'var(--text)' }}>{m.label}</span>
                </label>
                <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, marginLeft: 24 }}>
                  {m.hint}
                </div>
              </div>
            ))}
          </div>

          {noneOn && (
            <div style={{ fontSize: 11, color: 'var(--warn)', marginTop: 10 }}>
              En az bir metrik açık kalmalı. Anomali motorunu tümden kapatmak için
              Background ayarlarını kullanın.
            </div>
          )}

          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 12, lineHeight: 1.5 }}>
            Bir metriği kapatmak açık kayıtları silmez: yeni tespit üretilmez, mevcut
            açık anomaliler bayat ufkuyla kendiliğinden kapanır. Elle kurulmuş alarm
            kuralları bu vidadan etkilenmez.
          </div>

          <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
            <Button variant="primary" onClick={save} disabled={busy || noneOn}>
              {busy ? 'Kaydediliyor…' : 'Kaydet'}
            </Button>
            {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
          </div>
        </>
      )}
    </div>
  );
}

// ── Dedektör hassasiyeti (v0.9.826) ─────────────────────────────
//
// Üstteki bölüm hangi metriğin ÖLÇÜLECEĞİNİ ayarlıyor; bu bölüm
// ölçülenin ne zaman OLAY sayılacağını.
//
// Neden vida gerekti: bu eşikler bugüne kadar ALTI kez kodda değişti
// (v0.8.220 dwell, v0.9.48 düz-MAD tabanı, v0.9.180 mutlak taban,
// v0.9.193 criticalZ 5→6…) ve her seferinde operatör bir çentik ötede
// aynı duvara çarptı. Son vaka: medyanı 1.90ms olan bir op 9.69ms'e
// çıkınca 8.0σ critical anomali açılıyordu — istatistiksel olarak
// gerçek, operasyonel olarak hiçbir şey.
const SENSITIVITY_METRICS: { key: string; label: string; unit: string }[] = [
  { key: 'error_rate', label: 'Hata oranı (error_rate)', unit: '%' },
  { key: 'p99_ms', label: 'P99 gecikme (p99_ms)', unit: 'ms' },
  { key: 'request_rate', label: 'İstek hızı (request_rate)', unit: '/sn' },
];

// Alan sözlüğü — her vidanın NE kapattığını tek cümlede söyler.
// Ayrı bir tablo çünkü metrik başına üç kez tekrarlanıyor ve metinlerin
// ayrışması (aynı vidanın iki metrikte farklı anlatılması) bu ekranın
// en olası bozulma biçimi.
const SENSITIVITY_FIELDS: {
  key: 'floorPct' | 'absFloor' | 'minAbsDelta' | 'minMAD' | 'minBaselineRate';
  label: string; hint: string; step: number; unitKind: 'ratio' | 'value' | 'rate';
}[] = [
  { key: 'floorPct', label: 'Göreli değişim tabanı', step: 0.01, unitKind: 'ratio',
    hint: '|fark| / medyan. 0.10 = medyanın %10\'undan küçük hareketler olay sayılmaz.' },
  { key: 'absFloor', label: 'Mutlak değer tabanı', step: 0.5, unitKind: 'value',
    hint: 'Güncel değer bunun ALTINDAysa yükseliş yönlü anomali açılmaz. Düşüşleri etkilemez.' },
  { key: 'minAbsDelta', label: 'Mutlak fark tabanı', step: 0.5, unitKind: 'value',
    hint: '|güncel − medyan| bunun altındaysa açılmaz. Küçük medyanlarda yüzde tabanının kör noktasını kapatır.' },
  { key: 'minMAD', label: 'En küçük MAD (σ tabanı)', step: 0.1, unitKind: 'value',
    hint: 'Sapma ölçüsünün alt sınırı. Çok sıkı bir geçmişte z\'nin patlamasını engeller — asıl gürültü kaynağı budur.' },
  { key: 'minBaselineRate', label: 'En düşük hacim', step: 0.5, unitKind: 'rate',
    hint: 'Son 5 dakikanın istek hızı bunun altındaysa AÇILMAZ. Düşük hacimde yüzdeler gürültüdür. Çözülmeyi etkilemez.' },
];

function SensitivitySection() {
  const [cfg, setCfg] = useState<AnomalySensitivityConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAnomalySensitivity()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      // Sunucu kelepçeleyip KAYDEDİLENİ döndürüyor; onu state'e yazmak
      // şart — operatör anlamsız bir değer girdiyse (negatif, aralık
      // dışı) alanın varsayılana döndüğünü GÖRMELİ, yoksa yazdığını
      // kaydedilmiş sanır.
      const saved = await api.putAnomalySensitivity(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Kaydedildi — dedektör bir sonraki taramada yeni eşikleri kullanır.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const setMetricField = (metric: string, field: string, v: number) => {
    if (!cfg) return;
    setCfg({
      ...cfg,
      metrics: { ...cfg.metrics, [metric]: { ...cfg.metrics[metric], [field]: v } },
    });
  };

  return (
    <div>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Dedektör hassasiyeti</h2>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55 }}>
        Bir sapmanın ne zaman <b>olay</b> sayılacağını belirler. Dedektör her metriği
        kendi 5 dakikalık geçmişine göre puanlar (medyan + MAD ile sağlam z-skoru);
        aşağıdaki tabanlar, istatistiksel olarak büyük ama operasyonel olarak
        önemsiz sapmaları eler. Örnek: medyanı 1,90 ms olan bir işlemin 9,69 ms&apos;e
        çıkması 8σ&apos;dır ama kimsenin fark etmediği bir şeydir.
      </p>

      {!cfg ? (
        flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />
      ) : (
        <>
          <div style={{ display: 'grid', gap: 12, marginBottom: 22 }}>
            <Field label="Kritik z-skoru (açılma eşiği)">
              <input type="number" min={1} max={50} step={0.5}
                value={cfg.criticalZ}
                onChange={e => setCfg({ ...cfg, criticalZ: Number(e.target.value) })} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Bu σ değerinin üstündeki sapmalar <b>critical</b> sayılır. Dedektör yalnız
                critical sapmalarda Problem açtığı için (v0.9.193) bu, fiilen açılma
                eşiğidir: yükseltmek daha az, düşürmek daha çok anomali demek.
                Varsayılan 6,0.
              </div>
            </Field>

            <Field label="Süreklilik (kaç 5-dakikalık dilim)">
              <input type="number" min={1} max={24} step={1}
                value={cfg.dwellBuckets}
                onChange={e => setCfg({ ...cfg, dwellBuckets: Number(e.target.value) })} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Bir anomalinin açılması için üst üste bu kadar dilimde ateşlemesi gerekir
                &mdash; anlık bir sıçrama sayfa göndermez. Varsayılan 3
                ({(cfg.dwellBuckets || 3) * 5} dakika sürekli). Çözülme bundan
                etkilenmez: tek bir dilimin banda dönmesi problemi kapatır.
              </div>
            </Field>
          </div>

          {SENSITIVITY_METRICS.map(m => {
            const s = cfg.metrics?.[m.key];
            if (!s) return null;
            // Birim etiketi alan tipine göre: oran birimsiz, değer
            // metriğin birimi, hacim her zaman istek/sn.
            const unitFor = (kind: string) =>
              kind === 'ratio' ? '' : kind === 'rate' ? 'istek/sn' : m.unit;
            return (
              <div key={m.key} style={{ marginBottom: 22 }}>
                <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text)', marginBottom: 8 }}>
                  {m.label}
                </div>
                <div style={{ display: 'grid', gap: 10, marginLeft: 12 }}>
                  {SENSITIVITY_FIELDS.map(f => (
                    <Field key={f.key} label={`${f.label}${unitFor(f.unitKind) ? ` (${unitFor(f.unitKind)})` : ''}`}>
                      <input type="number" min={0} step={f.step}
                        value={s[f.key]}
                        onChange={e => setMetricField(m.key, f.key, Number(e.target.value))} />
                      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                        {f.hint} <b>0 = kapalı.</b>
                      </div>
                    </Field>
                  ))}
                </div>
              </div>
            );
          })}

          {/* v0.9.827 — dedektör → incident kapısı.
              Bu çağrı bugüne kadar KOŞULSUZDU ve Settings'teki hiçbir vida
              ona ulaşmıyordu: üstteki "terfi ettir" kutucuğu BAŞKA bir
              hattı yönetiyor. Operatör onu kapatıp metrik dedektörünün
              incident açmaya devam ettiğini görüyordu. */}
          <div style={{ borderTop: '1px solid var(--border)', paddingTop: 16, marginTop: 4 }}>
            <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <input type="checkbox" checked={cfg.attachToIncident !== false}
                onChange={e => setCfg({ ...cfg, attachToIncident: e.target.checked })} />
              <span style={{ fontSize: 13, color: 'var(--text)' }}>
                Dedektör problemini otomatik incident&apos;a bağla
              </span>
            </label>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, marginLeft: 24, lineHeight: 1.5 }}>
              Açıkken, dedektörün açtığı her metrik problemi servis ve şiddetine göre
              aktif bir incident&apos;a iliştirilir (yoksa yenisi açılır). Varsayılan
              açık &mdash; bugüne kadarki davranış budur.
              <br />
              <b>Kapatırsanız problem yine açılır ve bildirim yine gider</b>; yalnız
              incident açılmaz. Bu vida &laquo;bana haber verme&raquo; değil,
              &laquo;bunu olay yönetimine sokma&raquo; demektir.
            </div>
          </div>

          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 16, lineHeight: 1.5 }}>
            Bu eşikler yalnız <b>metrik anomali dedektörünü</b> bağlar. Elle kurduğunuz
            alarm kuralları kendi eşiklerini kullanır; log/iz desen anomalileri ise
            yukarıdaki terfi hattından geçer. Sertleştirmek açık kayıtları silmez: yeni
            tespit üretilmez, mevcut anomaliler kendi bantlarına dönünce kapanır.
          </div>

          <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
            <Button variant="primary" onClick={save} disabled={busy}>
              {busy ? 'Kaydediliyor…' : 'Kaydet'}
            </Button>
            {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
          </div>
        </>
      )}
    </div>
  );
}

// ── Age-based escalation (v0.9.248) ─────────────────────────────
//
// Lives on this tab rather than its own because an operator arrives
// here asking one question — "why am I getting so many pages?" — and
// the two halves of the answer are the promotion gate above and this
// ladder. It is NOT scoped to anomalies though: every open Problem
// climbs it, whichever rule opened it. The copy says so, because
// putting it under a heading called "Anomaly auto-promotion" would
// otherwise imply a narrower blast radius than it has.
//
// The windows were hard-coded 15 min / 30 min until now, which meant
// tightening promotion barely helped: anything that got through
// escalated itself to critical half an hour later and re-fired the
// notify channel on the way (escalateStaleProblems calls
// SendProblemAlert on every bump).
function EscalationSection() {
  type Esc = { enabled: boolean; infoToWarningSec: number; warningToCriticalSec: number };
  const [cfg, setCfg] = useState<Esc | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getProblemEscalation()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putProblemEscalation(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Saved — next evaluator tick picks it up automatically.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const mins = (sec: number) => `${Math.round(sec / 60)} min`;

  return (
    <div>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Age-based escalation</h2>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55 }}>
        An open Problem nobody acks climbs the severity ladder on its own:
        info &rarr; warning &rarr; critical. Each bump re-fires the notify channel, so
        severity-gated pagers hear about it again. This applies to <b>every</b> Problem,
        not just promoted anomalies &mdash; turn it off if &quot;still open after 30
        minutes&quot; is normal for your fleet.
      </p>

      {!cfg ? (
        flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />
      ) : (
        <>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 16 }}>
            <input type="checkbox" checked={cfg.enabled}
              onChange={e => setCfg({ ...cfg, enabled: e.target.checked })} />
            <span style={{ fontSize: 13, color: 'var(--text)' }}>
              Escalate open Problems as they age
            </span>
          </label>

          <div style={{ display: 'grid', gap: 12, opacity: cfg.enabled ? 1 : 0.5 }}>
            <Field label="info → warning after (seconds)">
              <input type="number" min={60} max={604800} step={60}
                value={cfg.infoToWarningSec}
                onChange={e => setCfg({ ...cfg, infoToWarningSec: Number(e.target.value) })}
                disabled={!cfg.enabled} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Currently {mins(cfg.infoToWarningSec)}. Default 900s (15 min).
              </div>
            </Field>

            <Field label="warning → critical after (seconds)">
              <input type="number" min={cfg.infoToWarningSec} max={604800} step={60}
                value={cfg.warningToCriticalSec}
                onChange={e => setCfg({ ...cfg, warningToCriticalSec: Number(e.target.value) })}
                disabled={!cfg.enabled} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Currently {mins(cfg.warningToCriticalSec)}. Default 1800s (30 min).
                Must be &ge; the info &rarr; warning window.
              </div>
            </Field>
          </div>

          <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
            <Button variant="primary" onClick={save} disabled={busy}>
              {busy ? 'Saving…' : 'Save'}
            </Button>
            {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
          </div>
        </>
      )}
    </div>
  );
}

// ── Exception triyajı (v0.9.775) ────────────────────────────────
//
// Aynı tabda, çünkü operatör buraya tek bir soruyla geliyor: "bir şey
// ne zaman ve ne kadar süre gündemimde kalsın?" Üstteki iki bölüm
// Problem tarafını (promosyon kapısı + yaşa göre tırmanma) ayarlıyor;
// bu bölüm exception gruplarının P1/P2/P3 basamağını ayarlıyor.
//
// Neden bir vida gerekti: pencere üç kez sabit olarak öteledi
// (v0.9.627 5dk→1sa, v0.9.699 uçurum→basamak, v0.9.775 1sa→4sa) ve
// operatör her seferinde bir çentik ötede aynı duvara çarptı. "Ne kadar
// taze hâlâ acildir" filoya, nöbet devrine ve ekrana bakma sıklığına
// bağlı — kodda tahmin edilecek bir sayı değil.
function ExceptionTriageSection() {
  const [cfg, setCfg] = useState<ExceptionTriageConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getExceptionTriage()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putExceptionTriage(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Kaydedildi — /inbox bir sonraki okumada yeni pencereleri kullanır.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  // Basamak ters dönerse kaydetmeden ÖNCE söyle: backend zaten 400
  // döner, ama operatör hatayı alanın yanında görmeli.
  const inverted = !!cfg && cfg.p2SameDayHours < cfg.p1FreshHours;

  return (
    <div>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Exception triyajı</h2>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55 }}>
        Bir exception grubunun /inbox&apos;taki önceliği iki şeyden çıkar: <b>şiddet</b> (hacim
        ve dakikadaki hız) ve <b>tazelik</b> (en son ne zaman görüldü). Şiddet zamanla
        silinmez, yalnız aciliyeti düşer — bu yüzden basamak: taze bir patlama P1,
        aynı gün içindeki P2, sonrası P3. Aşağıdaki pencereler o basamağın yerini
        belirler.
      </p>

      {!cfg ? (
        flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />
      ) : (
        <>
          <div style={{ display: 'grid', gap: 12 }}>
            <Field label="P1 tazelik penceresi (saat)">
              <input type="number" min={1} max={168} step={1}
                value={cfg.p1FreshHours}
                onChange={e => setCfg({ ...cfg, p1FreshHours: Number(e.target.value) })} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Son görülmesi bu kadar saat içinde olan bir patlama P1 kalır. Aynı
                pencere, patlama sayılmayan ama 100+ olaylık grupların P2 kapısı için
                de kullanılır. Varsayılan 4 saat &mdash; Problem tarafındaki
                &laquo;4 saattir açık critical &rarr; P1&raquo; kuralıyla simetrik.
              </div>
            </Field>

            <Field label="P2 aynı-gün penceresi (saat)">
              <input type="number" min={cfg.p1FreshHours} max={720} step={1}
                value={cfg.p2SameDayHours}
                onChange={e => setCfg({ ...cfg, p2SameDayHours: Number(e.target.value) })} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                P1 penceresi kapandıktan sonra patlama bu süre boyunca P2
                (&laquo;bugün&raquo;) kalır, sonrası P3. Varsayılan 24 saat.
                P1 penceresinden küçük olamaz.
                {inverted && (
                  <div style={{ color: 'var(--warn)', marginTop: 4 }}>
                    P1 penceresinden küçük: basamak ters döner, taze bir patlama
                    P1&apos;i atlayıp doğrudan P3&apos;e düşerdi.
                  </div>
                )}
              </div>
            </Field>

            <Field label="Sessiz grubu otomatik çözme (saat)">
              <input type="number" min={1} max={720} step={1}
                value={cfg.staleResolveHours}
                onChange={e => setCfg({ ...cfg, staleResolveHours: Number(e.target.value) })} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Bu kadar süredir yeni olay görmeyen açık/ack&apos;li bir grup
                kendiliğinden <b>resolved</b>&apos;a geçer. Varsayılan 24 saat.
                Süpürme yaklaşık 6 dakikada bir koşar.
              </div>
            </Field>
          </div>

          <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
            <Button variant="primary" onClick={save} disabled={busy || inverted}>
              {busy ? 'Kaydediliyor…' : 'Kaydet'}
            </Button>
            {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
          </div>
        </>
      )}
    </div>
  );
}
