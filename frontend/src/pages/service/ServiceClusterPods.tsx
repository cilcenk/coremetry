import { Fragment, useMemo, useState } from 'react';
import { DataTableHead, DataTableColgroup, type DataTable } from '@/components/DataTable';
import { PodJmxInline } from './PodJmxInline';
import { fmtCores, fmtBps, podPhaseBadge, restartColor } from '@/pages/clusters/thresholds';
import { fmtBytes } from '@/lib/utils';
import type { ClusterPodRow } from '@/lib/types';

// ServiceClusterPods (v0.9.155, operatör onaylı mock A) — Infrastructure
// sekmesinin pod listesi artık cluster'lara göre AÇILIR GRUP: her cluster bir
// kart (başlıkta pod sayısı + Σcpu/Σmem + ↻restarts + sağlık; tıkla-kapat),
// gövdesinde o cluster'ın pod tablosu. Bir pod satırına tıkla → YERİNDE
// JVM/JBoss JMX paneli açılır (PodJmxInline); "Tam detay → /pod" tam sayfaya
// gider. Sıralama/resize korunur: tek useDataTable (dt) globaldir, sortedRows
// cluster'a göre gruplanır; her kart AYNI DataTableHead'i taşır → herhangi bir
// başlığa tıkla tüm cluster'ları sıralar (dt paylaşımlı). Cluster kolonu
// kaldırıldı (artık grup başlığı). Aynı anda tek pod açık (openKey).
export function ServiceClusterPods({ dt, effNs, effDeploy, cFrom, cTo, colCount, onOpenPod }: {
  dt: DataTable<ClusterPodRow>;
  effNs: string;
  effDeploy: string;
  cFrom: number;
  cTo: number;
  colCount: number;
  onOpenPod: (r: ClusterPodRow) => void;
}) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [openKey, setOpenKey] = useState<string | null>(null);

  // sortedRows'u cluster'a göre grupla (grup içi sıra sortu izler).
  const groups = useMemo(() => {
    const m = new Map<string, ClusterPodRow[]>();
    for (const r of dt.sortedRows) {
      const arr = m.get(r.cluster);
      if (arr) arr.push(r); else m.set(r.cluster, [r]);
    }
    return [...m.entries()];
  }, [dt.sortedRows]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      {groups.map(([cluster, pods]) => {
        const cpuSum = pods.reduce((a, r) => a + r.cpuCores, 0);
        const memSum = pods.reduce((a, r) => a + r.memBytes, 0);
        const restarts = pods.reduce((a, r) => a + (r.restarts ?? 0), 0);
        const notRunning = pods.filter(p => p.phase && p.phase !== 'Running').length;
        const health = notRunning > 0 ? 'err' : restarts > 5 ? 'warn' : 'ok';
        const hcolor = health === 'ok' ? 'var(--ok)' : health === 'warn' ? 'var(--warn)' : 'var(--err)';
        const isCol = !!collapsed[cluster];
        return (
          <div key={cluster} style={{ border: '1px solid var(--border)', borderRadius: 9, overflow: 'hidden' }}>
            {/* cluster başlığı — tıkla-kapat */}
            <div onClick={() => setCollapsed(s => ({ ...s, [cluster]: !s[cluster] }))}
              style={{
                display: 'flex', alignItems: 'center', gap: 10, padding: '10px 13px',
                cursor: 'pointer', background: 'var(--panel2, var(--bg))',
              }}>
              <span style={{ color: 'var(--text3)', display: 'inline-block', width: 12,
                transform: isCol ? 'rotate(-90deg)' : 'none', transition: 'transform .15s' }}>▾</span>
              <span className="mono" style={{ fontWeight: 600, fontSize: 13 }}>{cluster}</span>
              <span className={`badge ${health === 'ok' ? 'b-ok' : 'b-err'}`}>{pods.length} pods</span>
              <div style={{ marginLeft: 'auto', display: 'flex', gap: 16, alignItems: 'center', fontSize: 12, color: 'var(--text3)' }}>
                <span>CPU <b className="mono" style={{ color: 'var(--text)' }}>{fmtCores(cpuSum)}</b></span>
                <span>Mem <b className="mono" style={{ color: 'var(--text)' }}>{fmtBytes(memSum)}</b></span>
                <span style={{ color: restartColor(restarts) }}>↻ {restarts}</span>
                <span style={{ width: 8, height: 8, borderRadius: '50%', background: hcolor }} title={`cluster sağlığı: ${health}`} />
              </div>
            </div>
            {/* gövde — cluster'ın pod tablosu */}
            {!isCol && (
              <div className="table-wrap">
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={dt} />
                  <DataTableHead dt={dt} />
                  <tbody>
                    {pods.map(r => {
                      const key = `${r.cluster}|${r.namespace}|${r.pod}`;
                      const open = openKey === key;
                      return (
                        <Fragment key={key}>
                          <tr onClick={() => setOpenKey(open ? null : key)}
                            title="Metrikleri göster · JVM · GC · datasource"
                            style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                            <td className="mono" style={{ fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.pod}>
                              <span style={{ color: 'var(--text3)', marginRight: 5 }}>{open ? '▾' : '▸'}</span>{r.pod}
                            </td>
                            <td>{r.phase
                              ? <span className={`badge ${podPhaseBadge(r.phase)}`}>{r.phase}</span>
                              : <span style={{ color: 'var(--text3)' }}>—</span>}</td>
                            <td className="num mono">{fmtCores(r.cpuCores)}</td>
                            <td className="num mono">{fmtBytes(r.memBytes)}</td>
                            <td className="num mono">{(r.netInBps ?? 0) > 0 ? fmtBps(r.netInBps!) : '—'}</td>
                            <td className="num mono">{(r.netOutBps ?? 0) > 0 ? fmtBps(r.netOutBps!) : '—'}</td>
                            <td className="num mono" style={{ color: restartColor(r.restarts ?? 0) }}>{r.restarts ?? 0}</td>
                          </tr>
                          {open && (
                            <tr>
                              <td colSpan={colCount} style={{ padding: 0, background: 'var(--bg)' }}>
                                {/* ns = pod'un KENDİ namespace'i (r.namespace);
                                    JMX selector namespace+pod ikisini de eşler,
                                    çok-namespace serviste effNs yanlış olurdu. */}
                                <PodJmxInline cluster={r.cluster} ns={r.namespace || effNs} deploy={effDeploy}
                                  pod={r.pod} cFrom={cFrom} cTo={cTo} onFull={() => onOpenPod(r)} />
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
