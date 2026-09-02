// Rollouts — v0.10.201 (ROLLOUTS Faz 4; docs/audits/rollouts-audit.md §4, §9, §14.1).
// «Deployment Report» yerine canlı, olay-tabanlı akış: workload_rollouts
// satırları (reconciler yazar — satır = gözlenmiş giriş olayı) + agregat
// sekmesi (deploy sıklığı, rollback oranı, süre — DORA zemini).
//
// Canlılık: Plan A (audit §3) — `event: rollout` yalnız INVALIDATION sinyali
// (eventInvalidations.ts → keys.rollouts.listAll); satırlar snapshot
// refetch'iyle gelir, 15 s poll SSE kaybının emniyeti; pencere refreshTick'le
// yürür (Dashboard emsali — mount'ta donmuş `to` canlı sekmeyi öldürür).
// Tablo SUNUCU sıralı (started_at DESC + LIMIT): istemci sıralaması kesik
// sayfada yalan söyler → serverSort (resize-only, Services emsali).
// URL = kaynak: ?tab, ?status, ?cluster, ?namespace (debounce), ?range.
// Bayrak kapalı → 404 {disabled:true} SENTİNELİ (hata değil; entities emsali)
// → Empty + admin için tek tık Enable (mevcut ayarları EZMEDEN: read-modify-write).
// Koşu durumu (runs) admin ucu: viewer'da hiç sorulmaz (403 döngüsü olmasın).
// Satır çekmecesi + health verdict + tekil `/api/rollout` istemcisi Faz 4b.
import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { PageShell } from '@/components/ui/PageShell';
import { Spinner, Empty } from '@/components/Spinner';
import { Badge, Button, StatTile } from '@/components/ui';
import { useDataTable, DataTableColgroup, DataTableHead, ResetLayoutButton } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs } from '@/lib/utils';
import { fmtNum, fmtDateTime, fmtDurShort } from '@/lib/utils';
import { api } from '@/lib/api';
import { useAuth } from '@/components/AuthProvider';
import { keys, useRollouts, useRolloutStats, useRolloutRuns, useEntityClusters } from '@/lib/queries';
import { RolloutDrawer } from '@/components/RolloutDrawer';
import { tracesPivotHref } from '@/lib/pivotHref';
import { entityHref } from '@/lib/entityHref';
import { rolloutKey, statusTone, statusLabel, statusTitle, rolloutDurationSec, shortRevision, imageDiff, encodeRolloutParam, decodeRolloutParam, rolloutTracesFilters, rolloutChangeKind, changeKindLabel, changeKindTitle, changeKindTone } from '@/lib/rolloutRow';
import type { WorkloadRollout } from '@/lib/types';

const STATUSES = ['', 'in_progress', 'completed', 'rolled_back', 'superseded', 'stalled'] as const;

// Sunucu sıralı sayfa: sortValue YOK (kesik 200 satırı sıralamak yanıltır —
// dataTable.ts serverSort sözleşmesi; Services emsali).
// v0.10.205 — operatör bildirimi: prod'da (uzun workload/imaj adları) fit
// kolonları 48px tabanına kadar ezdi, hücreler ve BAŞLIKLAR ortadan
// kırpıldı ("KA…", "SP…"). minWidth tabanı = fit'in ezemeyeceği okunabilir
// genişlik; toplam kabı aşarsa .table-wrap zaten overflow-x:auto kaydırır
// (v0.9.1078). Başlık kırpılması da biter: taban her başlığın tam adını
// taşıyacak kadar geniş.
const COLS: DataTableColumn<WorkloadRollout>[] = [
  { id: 'status', label: 'Durum', width: 120, minWidth: 96 },
  { id: 'workload', label: 'Workload', width: 300, minWidth: 200 },
  { id: 'kind', label: 'Tür', width: 100, minWidth: 64 },
  { id: 'change', label: 'Değişiklik', width: 130, minWidth: 100 }, // v0.10.234 — imaj değişti mi (Deployment) / aynı mı (config)
  { id: 'revision', label: 'Revizyon', width: 150, minWidth: 120 },
  { id: 'image', label: 'İmaj (eski → yeni)', width: 300, minWidth: 170 },
  { id: 'started', label: 'Başladı', width: 170, minWidth: 140, numeric: true },
  { id: 'dur', label: 'Süre', width: 90, minWidth: 64, numeric: true },
  { id: 'spans', label: 'Span', width: 90, minWidth: 64, numeric: true },
  { id: 'by', label: 'Kaynak', width: 90, minWidth: 76 },
  { id: 'note', label: 'Not', width: 260, minWidth: 120 },
  { id: 'links', label: '', width: 120, minWidth: 96 },
];

const ROW_CV = { contentVisibility: 'auto', containIntrinsicSize: '0 32px' } as const;

export default function RolloutsPage() {
  const [range, setRange] = useUrlRange('24h');
  const [sp, setSp] = useSearchParams();
  const tab = sp.get('tab') === 'stats' ? 'stats' : 'live';
  const status = sp.get('status') ?? '';
  const cluster = sp.get('cluster') ?? '';
  const ns = sp.get('namespace') ?? '';
  const setParam = (k: string, v: string) => setSp(prev => { const n = new URLSearchParams(prev); if (v) n.set(k, v); else n.delete(k); return n; }, { replace: true });
  // Pencere canlı yürür (Dashboard refreshTick emsali): donmuş `to` yeni
  // rollout'u sonsuza dek dışarıda bırakırdı. custom (elle) pencere sabit.
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (range.preset === 'custom') return;
    const id = setInterval(() => { if (!document.hidden) setTick(t => t + 1); }, 60_000);
    return () => clearInterval(id);
  }, [range.preset]);
  const { from, to } = useMemo(() => timeRangeToNs(range), [range, tick]);
  // Namespace debounce: her tuş bir FINAL sorgusu olmasın
  const [nsDraft, setNsDraft] = useState(ns);
  useEffect(() => { setNsDraft(ns); }, [ns]);
  useEffect(() => {
    if (nsDraft === ns) return;
    const id = setTimeout(() => setParam('namespace', nsDraft), 350);
    return () => clearTimeout(id);
  }, [nsDraft]); // eslint-disable-line react-hooks/exhaustive-deps
  const auth = useAuth();
  const isAdmin = auth.user?.role === 'admin';
  const qc = useQueryClient();
  const clustersQ = useEntityClusters();
  const clusters = clustersQ.data?.clusters ?? [];
  const clusterName = (id: string) => clusters.find(c => c.id === id)?.name ?? id;
  const listQ = useRollouts({ from, to, cluster: cluster || undefined, namespace: ns || undefined, status: status || undefined, limit: 200 }, tab === 'live');
  const statsQ = useRolloutStats({ from, to, cluster: cluster || undefined, namespace: ns || undefined, topN: 10 }, tab === 'stats');
  const runsQ = useRolloutRuns(isAdmin); // admin ucu: viewer'da 403 döngüsü açma
  const rows = useMemo(() => listQ.data?.rollouts ?? [], [listQ.data]);
  const dt = useDataTable<WorkloadRollout>({ storageKey: 'rollouts-live', columns: COLS, rows, serverSort: true });
  const [enabling, setEnabling] = useState(false);
  const [enableErr, setEnableErr] = useState('');
  const err = (tab === 'live' ? listQ.error : statsQ.error) as Error | null;
  const disabled = tab === 'live' ? listQ.data?.disabled === true : statsQ.data?.disabled === true;
  const lastRun = runsQ.data?.runs?.[0];
  const drawerId = decodeRolloutParam(sp.get('rollout'));
  const enable = async () => {
    setEnabling(true);
    setEnableErr('');
    try {
      // Mevcut vidaları EZME: read-modify-write (tek tık Enable ayar sıfırlamasın)
      const cur = await api.rolloutSettings();
      await api.putRolloutSettings({ ...cur.settings, enabled: true });
      await qc.invalidateQueries({ queryKey: keys.rollouts.all });
    } catch (e) {
      setEnableErr(e instanceof Error ? e.message : String(e));
    } finally {
      setEnabling(false);
    }
  };
  return (
    <>
      <Topbar title="Rollouts" range={range} onRangeChange={setRange} />
      <PageShell>
        <div className="tab-strip" style={{ marginBottom: 10 }}>
          <button type="button" className={tab === 'live' ? 'active' : ''} onClick={() => setParam('tab', '')}>Canlı</button>
          <button type="button" className={tab === 'stats' ? 'active' : ''} onClick={() => setParam('tab', 'stats')}>Toplu</button>
        </div>
        <div className="controls" style={{ marginBottom: 12, gap: 10, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text3)' }}>
            Cluster
            <select value={cluster} onChange={e => setParam('cluster', e.target.value)}>
              <option value="">tümü</option>
              {clusters.map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
          </label>
          <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text3)' }}>
            Durum
            <select value={status} onChange={e => setParam('status', e.target.value)}>
              {STATUSES.map(s => <option key={s} value={s}>{s ? statusLabel(s) : 'tümü'}</option>)}
            </select>
          </label>
          <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text3)' }}>
            Namespace
            <input value={nsDraft} placeholder="tümü" onChange={e => setNsDraft(e.target.value)} style={{ width: 160 }} />
          </label>
          {isAdmin && (
            <span className="field-hint" style={{ marginLeft: 'auto' }}>
              {lastRun
                ? <>reconciler son koşu {fmtDateTime(new Date(lastRun.startedAt))} · <span className={`badge ${lastRun.status === 'ok' ? 'b-ok' : lastRun.status === 'partial' || lastRun.status === 'skipped' ? 'b-warn' : 'b-err'}`}>{lastRun.status}</span></>
                : runsQ.isPending ? null : 'reconciler henüz koşmadı'}
            </span>
          )}
        </div>
        {disabled ? (
          <Empty icon="—" title="Rollouts kapalı"
            action={isAdmin ? <Button variant="primary" size="sm" loading={enabling} onClick={() => void enable()}>Etkinleştir</Button> : undefined}>
            Olay tablosu yazılmıyor. {isAdmin ? (enableErr ? `Açılamadı: ${enableErr}` : 'Mevcut ayarlar korunarak açılır.') : 'Bir admin Settings → Rollouts → Enable ile açar.'}
          </Empty>
        ) : tab === 'live' ? (
          listQ.isPending ? <Spinner /> : err ? <Empty icon="!" title="Rollout listesi yüklenemedi">{err.message}</Empty> : rows.length === 0 ? (
            <Empty icon="∅" title="Bu pencerede rollout yok">{listQ.data?.note ?? 'Reconciler aktif kümeye giren yeni revizyon görmedi (giriş: ≥2 karar kovası + gözlenmiş yokluk).'}</Empty>
          ) : (
            <>
              {listQ.data?.capped && <div className="pod-cap">Liste sunucuda kesildi (limit 200) — pencereyi daralt.</div>}
              <div className="table-wrap">
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={dt} />
                  <DataTableHead dt={dt} />
                  <tbody>
                    {dt.sortedRows.map(r => {
                      const cname = clusterName(r.clusterId);
                      const tracesHref = tracesPivotHref({
                        window: range, cluster: cname, rootOnly: false,
                        filters: JSON.stringify(rolloutTracesFilters(r)),
                      });
                      const wlHref = entityHref({ type: 'workload', id: `wl:${r.clusterId}/${r.namespace}/${r.kind || 'Deployment'}/${r.workload}`, name: r.workload, namespace: r.namespace, clusterId: r.clusterId }, { range });
                      return (
                        <tr key={rolloutKey(r)} style={rows.length > 100 ? { ...ROW_CV, cursor: 'pointer' } : { cursor: 'pointer' }}
                          onClick={e => { if ((e.target as HTMLElement).closest('a, button')) return; setParam('rollout', encodeRolloutParam(r)); }}>
                          <td><Badge tone={statusTone(r.status)} title={[statusTitle(r.status), r.completedAt ? `tamamlandı ${fmtDateTime(new Date(r.completedAt))}` : ''].filter(Boolean).join(' · ') || undefined}>{statusLabel(r.status)}</Badge></td>
                          <td title={`${cname} / ${r.namespace} / ${r.workload}`} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                            <Link to={wlHref} className="sec">{r.workload}</Link>
                            <span className="field-hint"> · {r.namespace} · {cname}</span>
                          </td>
                          <td className="field-hint">{r.kind || '—'}</td>
                          <td>{(() => { const k = rolloutChangeKind(r); return <Badge tone={changeKindTone(k)} title={changeKindTitle(k)}>{changeKindLabel(k)}</Badge>; })()}</td>
                          <td className="mono" title={r.revision} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{shortRevision(r.revision, r.workload)}{r.prevRevision ? <span className="field-hint"> ← {shortRevision(r.prevRevision, r.workload)}</span> : null}</td>
                          <td className="mono" title={r.image || undefined} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{imageDiff(r)}</td>
                          <td className="mono">{fmtDateTime(new Date(r.startedAt))}</td>
                          <td className="num mono">{fmtDurShort(rolloutDurationSec(r, Date.now()))}</td>
                          <td className="num mono">{fmtNum(r.spanCount)}</td>
                          <td className="field-hint">{r.detectedBy}</td>
                          <td className="field-hint" title={r.note || undefined} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.note || ''}</td>
                          <td><Link to={tracesHref} className="sec">Traces →</Link></td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              <ResetLayoutButton dt={dt} />
            </>
          )
        ) : (
          statsQ.isPending ? <Spinner /> : err ? <Empty icon="!" title="İstatistik yüklenemedi">{err.message}</Empty> : !statsQ.data || statsQ.data.total === 0 ? (
            <Empty icon="∅" title="Bu pencerede rollout yok" />
          ) : (
            <StatsPanel st={statsQ.data} clusterName={clusterName} />
          )
        )}
        {drawerId && <RolloutDrawer id={drawerId} onClose={() => setParam('rollout', '')} />}
      </PageShell>
    </>
  );
}

function StatsPanel({ st, clusterName }: { st: NonNullable<ReturnType<typeof useRolloutStats>['data']>; clusterName: (id: string) => string }) {
  return (
    <>
      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginBottom: 14 }}>
        <StatTile label="Rollout"><span className="mono">{fmtNum(st.total)}</span> <span className="field-hint">{st.perDay.toFixed(1)} / gün</span></StatTile>
        <StatTile label="Tamamlanan"><span className="mono">{fmtNum(st.completed)}</span></StatTile>
        <StatTile label="Geri alınan" tone={st.rolledBack > 0 ? 'err' : undefined}><span className="mono">{fmtNum(st.rolledBack)}</span> <span className="field-hint">oran %{(st.rollbackRate * 100).toFixed(1)}</span></StatTile>
        <StatTile label="Devralınan"><span className="mono">{fmtNum(st.superseded)}</span></StatTile>
        <StatTile label="Sürüyor"><span className="mono">{fmtNum(st.inProgress)}</span></StatTile>
        <StatTile label="Takılı" tone={st.stalled > 0 ? 'warn' : undefined}><span className="mono">{fmtNum(st.stalled)}</span></StatTile>
        <StatTile label="Ort. süre"><span className="mono">{fmtDurShort(Math.round(st.meanDurationSec))}</span> <span className="field-hint">p95 {fmtDurShort(Math.round(st.p95DurationSec))}</span></StatTile>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: 14 }}>
        <div>
          <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>En çok rollback alan workload'lar</h3>
          <table style={{ width: '100%' }}>
            <thead><tr><th>workload</th><th style={{ textAlign: 'right' }}>rollback</th></tr></thead>
            <tbody>
              {st.topRollback.length === 0 && <tr><td colSpan={2} className="field-hint">yok</td></tr>}
              {st.topRollback.map(w => <tr key={`${w.clusterId}/${w.namespace}/${w.workload}`}><td>{w.workload} <span className="field-hint">· {w.namespace} · {clusterName(w.clusterId)}</span></td><td className="num mono">{w.n}</td></tr>)}
            </tbody>
          </table>
        </div>
        <div>
          <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>En çok deploy alan workload'lar</h3>
          <table style={{ width: '100%' }}>
            <thead><tr><th>workload</th><th style={{ textAlign: 'right' }}>rollout</th></tr></thead>
            <tbody>
              {st.topDeploy.length === 0 && <tr><td colSpan={2} className="field-hint">yok</td></tr>}
              {st.topDeploy.map(w => <tr key={`${w.clusterId}/${w.namespace}/${w.workload}`}><td>{w.workload} <span className="field-hint">· {w.namespace} · {clusterName(w.clusterId)}</span></td><td className="num mono">{w.n}</td></tr>)}
            </tbody>
          </table>
        </div>
        <div>
          <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>Gün kırılımı</h3>
          <table style={{ width: '100%' }}>
            <thead><tr><th>gün</th><th style={{ textAlign: 'right' }}>rollout</th><th style={{ textAlign: 'right' }}>geri alınan</th></tr></thead>
            <tbody>
              {st.byDay.length === 0 && <tr><td colSpan={3} className="field-hint">yok</td></tr>}
              {st.byDay.map(d => <tr key={d.day}><td className="mono">{d.day}</td><td className="num mono">{d.total}</td><td className="num mono" style={d.rolledBack > 0 ? { color: 'var(--err)' } : undefined}>{d.rolledBack}</td></tr>)}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}
