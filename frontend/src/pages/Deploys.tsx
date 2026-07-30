import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs, tsLong } from '@/lib/utils';
import { api } from '@/lib/api';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { buildDeployTimeline, type TimelineRow } from '@/lib/deploysTimeline';

// /deploys (v0.9.435, operatör istegi) — filo Deploys/Rollouts geçmişi:
// imaj/sürüm değişimleri + pod-churn (rollout/restart, pod adları
// değişince) TEK zaman çizelgesinde. İki kanıtlı kaynak birleşir:
// GetRecentDeploys (sürüm ilk-görülmeleri) + GetServiceRollouts
// (pod-churn, deploy/restart ayrımı v0.8.405). Rollout taraması aday
// kümesiyle sınırlı (pencerede deploy gören servisler) ve bu KAPSAM
// ŞERİTLE İFŞA edilir; belirli servisin tam geçmişi için picker.
// URL source-of-truth: ?range, ?service, ?kind.

const KIND_BADGE: Record<TimelineRow['kind'], { label: string; cls: string }> = {
  deploy:  { label: '🚀 deploy',  cls: 'b-info' },
  rollout: { label: '🔁 rollout', cls: 'b-warn' },
  restart: { label: '♻ restart',  cls: 'b-gray' },
};

const COLS: DataTableColumn<TimelineRow>[] = [
  { id: 'time',    label: 'Zaman',        sortValue: r => r.timeNs, naturalDir: 'desc', numeric: true, width: 170 },
  { id: 'kind',    label: 'Tür',          sortValue: r => r.kind, width: 110 },
  { id: 'service', label: 'Servis',       sortValue: r => r.service, width: 240 },
  { id: 'version', label: 'Sürüm',        sortValue: r => r.version, width: 260 },
  // podsChurn sayısal anahtar — biçimli string leksikografik yanlış
  // sıralıyordu (verify bulgusu); deploy-satırı churn'süz → -1 dibe.
  { id: 'pods',    label: 'Pod değişimi', sortValue: r => r.podsChurn ?? -1, numeric: true, naturalDir: 'desc', width: 160 },
  { id: 'volume',  label: 'Hacim',        sortValue: r => r.spanCount ?? 0, naturalDir: 'desc', numeric: true, width: 110 },
  // v0.9.436 — etki: hata-oranı deltası (yüzde puanı) sıralama anahtarı;
  // hesaplanmamış satırlar (-999) dibe.
  { id: 'impact',  label: 'Etki (±10dk)', sortValue: r => r.errDeltaPp ?? -999, numeric: true, naturalDir: 'desc', width: 170 },
];

// İşaretli delta rozeti: pozitif = kötüleşme (kırmızı), negatif = iyileşme
// (yeşil), |δ| küçükse nötr.
function DeltaBadge({ v, unit, threshold }: { v: number; unit: string; threshold: number }) {
  const cls = v > threshold ? 'b-err' : v < -threshold ? 'b-ok' : 'b-gray';
  return <span className={`badge ${cls}`} style={{ fontSize: 10 }}>{v > 0 ? '+' : ''}{v.toFixed(1)}{unit}</span>;
}

export default function DeploysPage() {
  const [range, setRange] = useUrlRange('24h');
  const [sp, setSp] = useSearchParams();
  const service = sp.get('service') ?? '';
  // Commit-on-pick (Traces deseni): her tuş vuruşu fetch + cache
  // anahtarı üretmesin — draft lokal, ?service yalnız seçimde yazılır.
  const [draft, setDraft] = useState(service);
  useEffect(() => { setDraft(service); }, [service]);
  const kindFilter = sp.get('kind') ?? '';
  const setParam = (k: string, v: string) => setSp(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set(k, v); else next.delete(k);
    return next;
  }, { replace: true });

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['deploys-history', from, to, service],
    queryFn: () => api.deploysHistory(from, to, service || undefined),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });

  // Saf kurucu (lib/deploysTimeline, vitest'li): sürümlü tek olayın
  // deploy+churn çifti TEK satırda birleşir — çift satır yanılgısı yok.
  const rows = useMemo<TimelineRow[]>(
    () => q.data ? buildDeployTimeline(q.data.deploys ?? [], q.data.rollouts ?? [], kindFilter) : [],
    [q.data, kindFilter]);

  const dt = useDataTable({ storageKey: 'deploys-history', columns: COLS, rows });

  return (
    <>
      <Topbar title="Deploys" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 8, marginBottom: 12, alignItems: 'center', flexWrap: 'wrap' }}>
          <ServicePicker value={draft} onChange={setDraft}
            onEnter={v => setParam('service', v ?? draft)}
            placeholder="Servis (tam churn taraması için seç)…" width={280} />
          {service && (
            <button className="sec" style={{ fontSize: 11, padding: '3px 8px' }}
              onClick={() => { setDraft(''); setParam('service', ''); }}>✕ {service}</button>
          )}
          <div className="segmented">
            {(['', 'deploy', 'rollout', 'restart'] as const).map(k => (
              <button key={k || 'all'} className={kindFilter === k ? 'active' : ''}
                onClick={() => setParam('kind', k)}>
                {k === '' ? 'Hepsi' : KIND_BADGE[k].label}
              </button>
            ))}
          </div>
          {q.data && (
            <span style={{ fontSize: 11, color: 'var(--text3)' }}
              title="Rollout/restart taraması pencerede deploy görülen servislerle sınırlıdır — kapsamı bu sayı söyler. Belirli bir servisin TAM churn geçmişi için soldan servisi seç.">
              churn taraması: {q.data.scannedServices} servis
              {q.data.candidateCapped && ' · aday kümesi kısıtlı'}
              {q.data.rolloutWindowClamped && ' · churn son ~6.5 günle sınırlı'}
              {q.data.deploysTruncated && ' · deploy listesi kırpıldı (pencereyi daralt)'}
              {(q.data.rolloutScanErrors ?? 0) > 0 && ` · ${q.data.rolloutScanErrors} servis taranamadı`}
            </span>
          )}
        </div>

        {q.isPending && <Spinner />}
        {q.isError && <Empty icon="✗" title="Deploy geçmişi yüklenemedi" />}
        {q.data && rows.length === 0 && (
          <Empty icon="🚀" title="Bu pencerede deploy/rollout yok">
            Sürüm ilk-görülmeleri service.version / image-tag attribute'larından,
            pod-churn ise aktif pod kümesi değişimlerinden türetilir.
          </Empty>
        )}
        {q.data && rows.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map((r, i) => (
                  <tr key={`${r.kind}:${r.service}:${r.timeNs}:${i}`}
                    style={rows.length > 100 ? { contentVisibility: 'auto', containIntrinsicSize: '33px' } : undefined}>
                    <td className="mono" style={{ fontSize: 11.5 }}>{tsLong(r.timeNs)}</td>
                    <td><span className={`badge ${KIND_BADGE[r.kind].cls}`}>{KIND_BADGE[r.kind].label}</span></td>
                    <td>
                      <Link to={`/service?name=${encodeURIComponent(r.service)}${r.kind !== 'deploy' ? '&tab=pods' : ''}`}
                        className="mono" style={{ color: 'var(--accent2)', textDecoration: 'none', fontSize: 12 }}>
                        {r.service}
                      </Link>
                    </td>
                    <td className="mono" style={{ fontSize: 11.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                      title={r.version}>{r.version}</td>
                    <td className="mono" style={{ fontSize: 11.5 }} title={r.podsTitle}>{r.podsDelta}</td>
                    <td className="num mono" style={{ fontSize: 11.5, color: 'var(--text3)' }}>
                      {r.spanCount !== undefined ? r.spanCount.toLocaleString() : '—'}
                    </td>
                    <td title={r.impactTitle} style={{ fontSize: 11 }}>
                      {r.errDeltaPp !== undefined ? (
                        <span style={{ display: 'inline-flex', gap: 4 }}>
                          <DeltaBadge v={r.errDeltaPp} unit="pp hata" threshold={0.5} />
                          {r.p99DeltaPct !== undefined && <DeltaBadge v={r.p99DeltaPct} unit="% p99" threshold={10} />}
                        </span>
                      ) : <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
