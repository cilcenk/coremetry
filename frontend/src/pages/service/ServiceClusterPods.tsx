import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
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
//
// v0.9.384 (redesign açık-dilimi, operatör onayı "Yap tamam") — yan yana
// pod kıyası: satırlardaki "+kıyas" iki pod'a kadar seçer (?pods=a,b
// URL'de, paylaşılabilir), seçilen İKİ pod'un JMX panelleri grupların
// altında SPLIT açılır. Asıl senaryo rolling deploy: eski/yeni imaj
// pod'unun heap/GC eğrisi yan yana. Tek-pod satır tıkı (inline akordeon)
// AYNEN kaldı — kas hafızası köprüsü. Yeni sorgu tipi yok: iki panel,
// tek tek zaten yapılabilen okumaların yan yana hali.
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
  const [params, setParams] = useSearchParams();
  const cmpPods = useMemo(
    () => (params.get('pods') ?? '').split(',').map(x => x.trim()).filter(Boolean).slice(0, 2),
    [params]);
  const setCmp = (list: string[]) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (list.length > 0) next.set('pods', list.join(',')); else next.delete('pods');
    return next;
  }, { replace: true });
  const toggleCmp = (pod: string) => {
    if (cmpPods.includes(pod)) setCmp(cmpPods.filter(x => x !== pod));
    else if (cmpPods.length < 2) setCmp([...cmpPods, pod]);
    else setCmp([cmpPods[0], pod]); // üçüncü seçim ikincinin yerine geçer
  };
  // URL'deki adları görünen satırlara çöz — pod ölmüş/pencereden düşmüşse
  // panel yerine dürüst not (sessiz yok-sayma değil).
  const cmpRows = useMemo(
    () => cmpPods.map(name => ({ name, row: dt.sortedRows.find(r => r.pod === name) })),
    [cmpPods, dt.sortedRows]);

  // v0.9.533 — ?jpod= derin linki (Problems → "pod'a bak"). Eskiden
  // kaldırılan bağımsız JMX bölümünün pod daraltmasıydı; yeniden
  // amaçlandı: ilgili pod satırını otomatik aç + görünüme kaydır.
  // TEK SEFERLİK ve TÜKETİLİR (ref bekçisi + replace:true, yabancı
  // paramlar korunur — ev kuralı): pod envanterde yoksa (rollout'ta
  // öldü) param yine temizlenir, sonsuz bekleyen daraltma kalmaz.
  const jpodConsumed = useRef(false);
  useEffect(() => {
    if (jpodConsumed.current) return;
    const jpod = (params.get('jpod') ?? '').trim();
    if (!jpod) { jpodConsumed.current = true; return; }
    if (dt.sortedRows.length === 0) return; // envanter henüz yüklenmedi
    jpodConsumed.current = true;
    const r = dt.sortedRows.find(x => x.pod === jpod);
    if (r) {
      setOpenKey(`${r.cluster}|${r.namespace}|${r.pod}`);
      setCollapsed(s => ({ ...s, [r.cluster]: false }));
      setTimeout(() => {
        document.getElementById(`pod-row-${jpod}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }, 80);
    }
    setParams(prev => {
      const next = new URLSearchParams(prev);
      next.delete('jpod');
      return next;
    }, { replace: true });
  }, [params, dt.sortedRows, setParams]);

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
        const restarts = pods.reduce((a, r) => a + (r.restartsUnknown ? 0 : r.restarts ?? 0), 0);
        const restartsPartial = pods.some(r => r.restartsUnknown);
        const notRunning = pods.filter(p => p.phase && p.phase !== 'Running').length;
        const health = notRunning > 0 ? 'err' : restarts > 5 ? 'warn' : 'ok';
        const hcolor = health === 'ok' ? 'var(--ok)' : health === 'warn' ? 'var(--warn)' : 'var(--err)';
        const isCol = !!collapsed[cluster];
        return (
          // v0.9.400 (operatör, prod ekran görüntüsü): kartın kendi zemini
          // yoktu — redhat/light temada sayfanın gri kanvası (--bg0)
          // tablonun içinden görünüyordu. .card konvansiyonu: --bg1
          // (açık temalarda BEYAZ, koyu temada yükseltilmiş yüzey).
          <div key={cluster} style={{ border: '1px solid var(--border)', borderRadius: 9, overflow: 'hidden', background: 'var(--bg1)' }}>
            {/* cluster başlığı — tıkla-kapat */}
            <div onClick={() => setCollapsed(s => ({ ...s, [cluster]: !s[cluster] }))}
              style={{
                display: 'flex', alignItems: 'center', gap: 10, padding: '10px 13px',
                // --panel2 hiç tanımlı değildi (sessiz fallback) — başlık
                // thead konvansiyonuyla --bg2 (hafif ton farkı).
                cursor: 'pointer', background: 'var(--bg2)',
              }}>
              <span style={{ color: 'var(--text3)', display: 'inline-block', width: 12,
                transform: isCol ? 'rotate(-90deg)' : 'none', transition: 'transform .15s' }}>▾</span>
              <span className="mono" style={{ fontWeight: 600, fontSize: 13 }}>{cluster}</span>
              <span className={`badge ${health === 'ok' ? 'b-ok' : health === 'warn' ? 'b-warn' : 'b-err'}`}>{pods.length} pods</span>
              <div style={{ marginLeft: 'auto', display: 'flex', gap: 16, alignItems: 'center', fontSize: 12, color: 'var(--text3)' }}>
                <span>CPU <b className="mono" style={{ color: 'var(--text)' }}>{fmtCores(cpuSum)}</b></span>
                <span>Mem <b className="mono" style={{ color: 'var(--text)' }}>{fmtBytes(memSum)}</b></span>
                <span style={{ color: restartColor(restarts) }}
                  title={restartsPartial ? 'Bazı pod\'ların restart serisi yok — toplam alt sınırdır.' : undefined}>
                  ↻ {restarts}{restartsPartial ? '+' : ''}</span>
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
                            id={`pod-row-${r.pod}`}
                            title="Metrikleri göster · JVM · GC · datasource"
                            style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                            <td className="mono" style={{ fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.pod}>
                              <span style={{ color: 'var(--text3)', marginRight: 5 }}>{open ? '▾' : '▸'}</span>{r.pod}
                              <button type="button"
                                onClick={(e) => { e.stopPropagation(); toggleCmp(r.pod); }}
                                title={cmpPods.includes(r.pod)
                                  ? 'Kıyastan çıkar'
                                  : 'Yan yana kıyasa ekle (en fazla 2 pod; ?pods= URL\'de)'}
                                style={{
                                  all: 'unset', cursor: 'pointer', marginLeft: 8, fontSize: 10.5,
                                  padding: '0 6px', borderRadius: 4,
                                  border: `1px solid ${cmpPods.includes(r.pod) ? 'var(--accent)' : 'var(--border)'}`,
                                  color: cmpPods.includes(r.pod) ? 'var(--accent)' : 'var(--text3)',
                                }}>
                                {cmpPods.includes(r.pod) ? '✓ kıyasta' : '+ kıyas'}
                              </button>
                            </td>
                            <td>{r.phase
                              ? <span className={`badge ${podPhaseBadge(r.phase)}`}>{r.phase}</span>
                              : <span style={{ color: 'var(--text3)' }}>—</span>}</td>
                            <td className="num mono">{fmtCores(r.cpuCores)}</td>
                            <td className="num mono">{fmtBytes(r.memBytes)}</td>
                            <td className="num mono">{(r.netInBps ?? 0) > 0 ? fmtBps(r.netInBps!) : '—'}</td>
                            <td className="num mono">{(r.netOutBps ?? 0) > 0 ? fmtBps(r.netOutBps!) : '—'}</td>
                            <td className="num mono"
                              title={r.restartsUnknown ? 'Restart serisi yok (KSM eksik ya da seri tavanı) — 0 değil, bilinmiyor.' : undefined}
                              style={{ color: r.restartsUnknown ? 'var(--text3)' : restartColor(r.restarts ?? 0) }}>
                              {r.restartsUnknown ? '—' : r.restarts ?? 0}</td>
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

      {/* v0.9.384 — yan yana kıyas: iki seçili pod'un JMX panelleri split.
          Tek pod seçiliyken ipucu satırı; çözülemeyen ad dürüst not. */}
      {cmpPods.length === 1 && (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>
          <span className="mono">{cmpPods[0]}</span> kıyasta — ikinci pod'u seç, paneller yan yana açılsın.
        </div>
      )}
      {cmpPods.length === 2 && (
        <div style={{ border: '1px solid var(--accent)', borderRadius: 9, padding: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, fontSize: 12.5, fontWeight: 600 }}>
            Pod kıyası
            <span style={{ fontWeight: 400, color: 'var(--text3)' }}>aynı pencere · aynı metrikler</span>
            <span onClick={() => setCmp([])}
              style={{ marginLeft: 'auto', cursor: 'pointer', color: 'var(--text3)' }}>✕ kapat</span>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 12 }}>
            {cmpRows.map(({ name, row }) => (
              <div key={name} style={{ border: '1px solid var(--border)', borderRadius: 7, overflow: 'hidden' }}>
                <div className="mono" style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '6px 10px', fontSize: 11.5, fontWeight: 600, borderBottom: '1px solid var(--border)' }}>
                  {name}
                  {row?.phase && <span className={`badge ${podPhaseBadge(row.phase)}`}>{row.phase}</span>}
                  <span onClick={() => toggleCmp(name)}
                    style={{ marginLeft: 'auto', cursor: 'pointer', color: 'var(--text3)', fontWeight: 400 }}>✕</span>
                </div>
                {row ? (
                  <PodJmxInline cluster={row.cluster} ns={row.namespace || effNs} deploy={effDeploy}
                    pod={row.pod} cFrom={cFrom} cTo={cTo} onFull={() => onOpenPod(row)} />
                ) : (
                  <div style={{ padding: 12, fontSize: 12, color: 'var(--text3)' }}>
                    Bu pod şu anki listede yok — ölmüş ya da pencereden düşmüş olabilir
                    (?pods= linki bayat). Kıyastan çıkarabilirsin.
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
