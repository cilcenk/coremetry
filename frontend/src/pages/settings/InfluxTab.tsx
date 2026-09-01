// InfluxTab — Settings → "Influx kaynakları" (v0.10.222, Influx D1;
// docs/audit/influx-integration.md §10, operatör onayı 2026-09-01).
//
// ClustersTab'ın liste deseni (kaynak kartları + tek Kaydet) ve
// MetricsBackendTab'ın Test düğmesi (formdaki değerleri KAYDETMEDEN dener).
// Token kutusu YOK: tokenRef bir referans (`env:NAME` | `file:/path`),
// sunucu çözüp rozetler (tokenResolved). Sorgular kaynak başına liste;
// "TFAIL şablonu" düğmesi spec'in iki sorgusunu + attrMap'i doldurur.
// Poller D2'de: bu sekme bugün yalnız yapılandırma + deneme.
import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button, Field, TextareaField } from '@/components/ui';
import { api } from '@/lib/api';
import { fmtDateTime } from '@/lib/utils';
import { useSettingsLoad, SettingsLoadError, FlashBox } from './shared';
import {
  parseAttrMap, attrMapToText, parseList, listToText,
  thresholdsToForm, thresholdsToWire, TFAIL_TEMPLATE, type ThresholdsForm,
} from './influxForm';
import type {
  InfluxQueryConfig, InfluxSourceInput, InfluxSourceSnapshot, InfluxStatusPayload, InfluxTestResult,
} from '@/lib/types';

interface EditQuery {
  name: string;
  flux: string;
  enrichFlux: string;
  groupBy: string;   // "A, B"
  attrMap: string;   // satır başına TAG=attr
  thresholds: ThresholdsForm;
}

interface EditSource {
  id: string;
  name: string;
  url: string;
  org: string;
  tokenRef: string;
  tokenResolved: boolean;
  tokenError?: string;
  intervalSec: string;
  insecureSkipVerify: boolean;
  enabled: boolean;
  queries: EditQuery[];
}

function queryFromWire(q: InfluxQueryConfig): EditQuery {
  return {
    name: q.name, flux: q.flux, enrichFlux: q.enrichFlux ?? '',
    groupBy: listToText(q.groupBy), attrMap: attrMapToText(q.attrMap),
    thresholds: thresholdsToForm(q.thresholds),
  };
}

function queryToWire(q: EditQuery): InfluxQueryConfig {
  return {
    name: q.name.trim(), flux: q.flux, enrichFlux: q.enrichFlux.trim() || undefined,
    groupBy: parseList(q.groupBy), attrMap: parseAttrMap(q.attrMap),
    thresholds: thresholdsToWire(q.thresholds),
  };
}

function fromSnapshot(s: InfluxSourceSnapshot): EditSource {
  return {
    id: s.id ?? '', name: s.name, url: s.url, org: s.org, tokenRef: s.tokenRef ?? '',
    tokenResolved: s.tokenResolved, tokenError: s.tokenError,
    intervalSec: s.intervalSec ? String(s.intervalSec) : '',
    insecureSkipVerify: !!s.insecureSkipVerify, enabled: s.enabled,
    queries: (s.queries ?? []).map(queryFromWire),
  };
}

function toWire(s: EditSource): InfluxSourceInput {
  const iv = Number(s.intervalSec.trim());
  return {
    id: s.id || undefined,
    name: s.name.trim(), url: s.url.trim(), org: s.org.trim(),
    tokenRef: s.tokenRef.trim() || undefined,
    intervalSec: Number.isFinite(iv) && iv > 0 ? iv : undefined,
    insecureSkipVerify: s.insecureSkipVerify, enabled: s.enabled,
    queries: s.queries.map(queryToWire),
  };
}

const EMPTY_SOURCE: EditSource = {
  id: '', name: '', url: '', org: '', tokenRef: '', tokenResolved: false,
  intervalSec: '', insecureSkipVerify: false, enabled: true, queries: [],
};

export function InfluxTab() {
  const [rows, setRows] = useState<EditSource[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [probe, setProbe] = useState<Record<number, InfluxTestResult | { pending: true }>>({});
  // v0.10.223 (D2) — "veri geliyor mu": metric_points izi + işçi durumu.
  // Sekme açılışında bir kez + elle yenile; poll YOK (ayar sekmesi).
  const [status, setStatus] = useState<InfluxStatusPayload | null>(null);
  const [statusErr, setStatusErr] = useState<string | null>(null);
  const loadStatus = () => {
    setStatusErr(null);
    api.getInfluxStatus().then(setStatus).catch(e => setStatusErr(e instanceof Error ? e.message : 'durum alınamadı'));
  };
  useEffect(() => { loadStatus(); }, []);
  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.getInfluxSettings(),
    s => setRows((s.sources ?? []).map(fromSnapshot)),
  );

  const patch = (i: number, p: Partial<EditSource>) =>
    setRows(rs => rs.map((r, j) => (j === i ? { ...r, ...p } : r)));
  const patchQ = (i: number, k: number, p: Partial<EditQuery>) =>
    setRows(rs => rs.map((r, j) => j !== i ? r
      : { ...r, queries: r.queries.map((q, l) => (l === k ? { ...q, ...p } : q)) }));

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putInfluxSettings({ sources: rows.map(toWire) });
      setRows((next.sources ?? []).map(fromSnapshot));
      const on = (next.sources ?? []).filter(s => s.enabled).length;
      setMsg({ kind: 'ok', text: `Kaydedildi — ${on} kaynak etkin.` });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Kaydetme başarısız' });
    } finally {
      setBusy(false);
    }
  };

  // Formdaki değerlerle dener; kaydetmez (MetricsBackendTab sözleşmesi).
  const runTest = async (i: number) => {
    setProbe(p => ({ ...p, [i]: { pending: true } }));
    try {
      const res = await api.testInfluxSource(toWire(rows[i]));
      setProbe(p => ({ ...p, [i]: res }));
    } catch (err) {
      setProbe(p => ({ ...p, [i]: { ok: false, tokenResolved: false, queries: [], error: err instanceof Error ? err.message : 'Test başarısız' } }));
    }
  };

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
  if (!loaded) return <Spinner />;

  return (
    <div style={{ maxWidth: 820 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Influx kaynakları</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        InfluxDB 2.x'teki bir seriyi (ör. <code>TFAIL/ADET</code>) dış metrik kaynağı olarak
        bağlayın. Poll sorgusu <code>metric_points</code>'e <code>ext:&lt;sorgu adı&gt;</code>
        metriği olarak yazılır, anomali dedektörü ve kanıt toplama (trace/pod/log) üstüne
        kurulur. Token <b>saklanmaz</b>: yalnız referansı (<code>env:AD</code> ya da{' '}
        <code>file:/yol</code>) kaydedilir ve her kullanımda çözülür.
      </p>

      <form onSubmit={save}>
        {rows.length === 0 && (
          <div className="influx-q" style={{ marginTop: 0, marginBottom: 12, color: 'var(--text3)' }}>
            Henüz kaynak yok — aşağıdan ekleyin.
          </div>
        )}
        {rows.map((r, i) => {
          const pr = probe[i];
          const st = r.id ? status?.sources.find(x => x.id === r.id) : undefined;
          return (
            <div key={i} className={`influx-src${r.enabled ? '' : ' is-off'}`}>
              {st && (
                <div className="influx-status">
                  {st.lastPointAt
                    ? <>Son veri <b>{fmtDateTime(st.lastPointAt)}</b> · 1 saatte {st.points1h} nokta / {st.series1h} seri</>
                    : <span className="is-quiet">Son 1 saatte metric_points'e veri gelmedi.</span>}
                  {st.worker && (
                    <> · işçi: {st.worker.lastError
                      ? <span className="is-err">{st.worker.lastError}</span>
                      : <>son poll {st.worker.lastPollAt ? fmtDateTime(st.worker.lastPollAt) : '—'} · {st.worker.lastRows} satır → {st.worker.lastPoints} nokta{st.worker.lastDrops ? ` (${st.worker.lastDrops} düştü)` : ''}</>}
                    </>
                  )}
                </div>
              )}
              <div className="influx-src__head">
                <Field label="Kaynak adı (service_name olur)" value={r.name}
                  onChange={e => patch(i, { name: e.target.value })}
                  placeholder="ggfail" required />
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, paddingBottom: 6 }}>
                  <input type="checkbox" checked={r.enabled}
                    onChange={e => patch(i, { enabled: e.target.checked })} />
                  <span style={{ fontSize: 12 }}>Etkin</span>
                </label>
                {r.id && <span className="badge mono" title="Sunucu sahipli kimlik — yeniden adlandırma korur">{r.id}</span>}
                <Button type="button" variant="ghost" size="sm"
                  onClick={() => setRows(rs => rs.filter((_, j) => j !== i))}>
                  Kaldır
                </Button>
              </div>

              <div className="influx-row">
                <Field label="URL" value={r.url} onChange={e => patch(i, { url: e.target.value })}
                  placeholder="https://influx.example.com:8086" required={r.enabled} />
                <Field label="Org" value={r.org} onChange={e => patch(i, { org: e.target.value })}
                  placeholder="bank" required={r.enabled} className="is-narrow" />
                <Field label="Poll aralığı (sn)" value={r.intervalSec}
                  onChange={e => patch(i, { intervalSec: e.target.value })}
                  placeholder="30" inputMode="numeric" hint="10–3600; boş = 30" className="is-narrow" />
              </div>
              <div className="influx-row">
                <Field label="Token referansı" value={r.tokenRef}
                  onChange={e => patch(i, { tokenRef: e.target.value })}
                  placeholder="env:COREMETRY_INFLUX_TOKEN_GG  ·  file:/var/run/secrets/influx/token"
                  required={r.enabled}
                  error={r.tokenError}
                  hint={r.tokenResolved
                    ? 'Çözüldü ✓ — pod bu referansı okuyabiliyor.'
                    : 'Düz token kabul edilmez. env: Helm extraEnv + existingSecret; file: mount edilmiş Secret.'} />
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, alignSelf: 'flex-end', paddingBottom: 10 }}>
                  <input type="checkbox" checked={r.insecureSkipVerify}
                    onChange={e => patch(i, { insecureSkipVerify: e.target.checked })} />
                  <span style={{ fontSize: 12 }}>TLS doğrulamasını atla</span>
                </label>
              </div>

              {r.queries.map((q, k) => (
                <div key={k} className="influx-q">
                  <div className="influx-row">
                    <Field label="Sorgu adı (metrik: ext:<ad>)" value={q.name}
                      onChange={e => patchQ(i, k, { name: e.target.value })}
                      placeholder="tfail_adet" hint="a-z, 0-9, _" className="is-narrow" />
                    <Field label="Seri boyutları (groupBy tag'leri)" value={q.groupBy}
                      onChange={e => patchQ(i, k, { groupBy: e.target.value })}
                      placeholder="OPERATIONCODE, ERRORCODE"
                      hint="v1: yalnız OPERATIONCODE + ERRORCODE (KANALKOD/FUNCTIONCODE yüksek kardinalite)" />
                    <Button type="button" variant="ghost" size="sm" style={{ alignSelf: 'flex-end', marginBottom: 10 }}
                      onClick={() => patch(i, { queries: r.queries.filter((_, l) => l !== k) })}>
                      Sorguyu kaldır
                    </Button>
                  </div>
                  <TextareaField label="Poll sorgusu (Flux, SORGU 1)" rows={6} value={q.flux}
                    onChange={e => patchQ(i, k, { flux: e.target.value })}
                    hint="Her poll'da koşar; `range(start: -2m)` + `sum()` → gauge (son 2 dk hata sayısı)." />
                  <TextareaField label="Kanıt sorgusu (Flux, SORGU 2)" rows={8} value={q.enrichFlux}
                    onChange={e => patchQ(i, k, { enrichFlux: e.target.value })}
                    hint="Anomali açılınca {{from}} {{to}} {{op}} {{err}} doldurulur; TRACEID + INSTANCEID listesi." />
                  <TextareaField label="Tag → attribute eşlemesi (satır başına TAG=attr)" rows={6} value={q.attrMap}
                    onChange={e => patchQ(i, k, { attrMap: e.target.value })}
                    hint="OPERATIONCODE=operation · ERRORCODE=error.code · INSTANCEID=k8s.pod.name · TRACEID=trace_id" />
                  <div className="influx-row">
                    <Field label="Kritik z" value={q.thresholds.criticalZ} inputMode="decimal" className="is-narrow"
                      onChange={e => patchQ(i, k, { thresholds: { ...q.thresholds, criticalZ: e.target.value } })}
                      placeholder="global" />
                    <Field label="Dwell (kova)" value={q.thresholds.dwell} inputMode="numeric" className="is-narrow"
                      onChange={e => patchQ(i, k, { thresholds: { ...q.thresholds, dwell: e.target.value } })}
                      placeholder="global" />
                    <Field label="Min. mutlak fark" value={q.thresholds.minAbsDelta} inputMode="decimal" className="is-narrow"
                      onChange={e => patchQ(i, k, { thresholds: { ...q.thresholds, minAbsDelta: e.target.value } })}
                      placeholder="5" />
                    <Field label="Min. MAD" value={q.thresholds.minMAD} inputMode="decimal" className="is-narrow"
                      onChange={e => patchQ(i, k, { thresholds: { ...q.thresholds, minMAD: e.target.value } })}
                      placeholder="1" />
                  </div>
                </div>
              ))}

              <div style={{ display: 'flex', gap: 8, marginTop: 10, alignItems: 'center' }}>
                <Button type="button" variant="secondary" size="sm"
                  onClick={() => patch(i, { queries: [...r.queries, queryFromWire({ name: '', flux: '', groupBy: [] })] })}>
                  + Boş sorgu
                </Button>
                <Button type="button" variant="secondary" size="sm"
                  disabled={r.queries.some(q => q.name === TFAIL_TEMPLATE.name)}
                  onClick={() => patch(i, { queries: [...r.queries, queryFromWire(TFAIL_TEMPLATE)] })}>
                  + TFAIL şablonu
                </Button>
                <Button type="button" variant="accent" size="sm" disabled={busy || !!(pr && 'pending' in pr)}
                  onClick={() => runTest(i)}>
                  Bağlantıyı dene
                </Button>
              </div>

              {pr && !('pending' in pr) && 'ok' in pr && (
                <div className="influx-probe">
                  <FlashBox kind={pr.ok ? 'ok' : 'err'}>
                    {pr.ok ? 'Bağlantı ve sorgular çalıştı.' : (pr.error || 'Başarısız')}
                    {' '}· token {pr.tokenResolved ? 'çözüldü' : 'çözülemedi'}
                  </FlashBox>
                  {(pr.queries ?? []).map(p => (
                    <div key={p.name} style={{ marginTop: 8 }}>
                      <b>{p.name || '(adsız)'}</b>{' '}
                      {p.error
                        ? <span style={{ color: 'var(--err)' }}>{p.error}</span>
                        : <span style={{ color: 'var(--text2)' }}>{p.rows} satır · {p.latencyMs} ms · kolonlar: {p.columns.join(', ') || '—'}</span>}
                      {!p.error && (p.sample?.length ?? 0) > 0 && (
                        <div style={{ overflowX: 'auto', marginTop: 4 }}>
                          <table>
                            <thead><tr>{p.columns.map(c => <th key={c}>{c}</th>)}</tr></thead>
                            <tbody>
                              {p.sample!.map((row, ri) => (
                                <tr key={ri}>{p.columns.map(c => <td key={c} className="mono">{row[c] ?? ''}</td>)}</tr>
                              ))}
                            </tbody>
                          </table>
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          );
        })}

        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <Button type="button" variant="secondary" size="sm"
            onClick={() => setRows(rs => [...rs, { ...EMPTY_SOURCE, queries: [queryFromWire(TFAIL_TEMPLATE)] }])}>
            + Kaynak ekle
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={loadStatus}
            title={status?.workerOnThisPod ? 'Bu pod poll işçisini de çalıştırıyor' : 'Poll işçisi başka bir pod\'da (worker rolü)'}>
            Durumu yenile
          </Button>
          {statusErr && <span className="is-err">{statusErr}</span>}
          <Button type="submit" variant="primary" size="sm" loading={busy}>
            Kaydet
          </Button>
          {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
        </div>
      </form>
    </div>
  );
}
