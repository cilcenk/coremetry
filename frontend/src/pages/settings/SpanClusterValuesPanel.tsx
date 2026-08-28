// SpanClusterValuesPanel — v0.10.141 (cluster değeri otomatik eşleme brief'i).
// Span verisindeki cluster değerleri (son 7 gün, entity_seen_5m; yoksa spans
// 1 saat) sayaç + ilk/son görülme ile; her değer bir Remote Cluster'a bağlı
// ya da EŞLEŞMEMİŞ. Eşleşmemiş değer bir kayda atanır (kalıcı; isteğe bağlı
// 24 saatlik geriye dönük span geçişi). Bir değer tek kayda: çakışmayı
// sunucu reddeder, bağlı kayıt gösterilir.
import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, Row, Badge } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtDateTime } from '@/lib/utils';

export function SpanClusterValuesPanel({ clusters, onAssigned }: {
  clusters: { id?: string; name: string }[];
  /** Atama sonrası ebeveyn formun satırı güncellenir — "Save all" eski listeyi gönderip atamayı sessizce geri almasın (inceleme). */
  onAssigned?: (clusterId: string, values: string[]) => void;
}) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ['thanos-span-clusters'], queryFn: () => api.thanosSpanClusters(), staleTime: 60_000 });
  const [pick, setPick] = useState<Record<string, string>>({});
  const [backfill, setBackfill] = useState(true);
  const [msg, setMsg] = useState<Record<string, string>>({});
  const assign = async (value: string) => {
    const clusterId = pick[value];
    if (!clusterId) return;
    setMsg(m => ({ ...m, [value]: '…' }));
    try {
      const res = await api.thanosAssignSpanCluster({ value, clusterId, backfill });
      if (!res.ok) {
        setMsg(m => ({ ...m, [value]: `✗ ${res.error ?? 'reddedildi'}` }));
        return;
      }
      setMsg(m => ({ ...m, [value]: `✓ ${res.clusterName} · backfill ${res.backfill}` }));
      if (res.clusterId && res.values) onAssigned?.(res.clusterId, res.values);
      void qc.invalidateQueries({ queryKey: ['thanos-span-clusters'] });
    } catch (err) {
      setMsg(m => ({ ...m, [value]: `✗ ${err instanceof Error ? err.message : 'assign failed'}` }));
    }
  };
  const rows = q.data?.rows ?? [];
  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="ov-card-h">
        <h3>Span cluster values</h3>
        <span className="ov-sub">
          {q.data ? `${rows.length} value${rows.length === 1 ? '' : 's'} · ${q.data.unmapped} unmapped · ${q.data.source}${q.data.source === 'spans-1h' ? ' (entity şeması yok — son 1 saat)' : ' (7d)'}` : ''}
        </span>
      </div>
      <div className="ov-card-b">
        {q.isPending ? <Spinner /> : q.error ? (
          <Empty icon="!" title="Span cluster değerleri yüklenemedi" compact>{String(q.error)}</Empty>
        ) : rows.length === 0 ? (
          <Empty icon="∅" title="Span verisinde cluster değeri yok" compact>Span'ler k8s.cluster.name / openshift.cluster.name / cluster taşımıyor.</Empty>
        ) : (
          <>
            <Row gap={2} wrap>
              <label className="field-hint" style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <input type="checkbox" checked={backfill} onChange={e => setBackfill(e.target.checked)} />
                atamada son 24 saati geriye dönük tara (pod/servis entity'leri)
              </label>
            </Row>
            <table>
              <thead><tr><th>value</th><th className="num">spans</th><th>first seen</th><th>last seen</th><th>bound to</th><th></th></tr></thead>
              <tbody>
                {rows.map(r => (
                  <tr key={r.value}>
                    <td className="mono">{r.value}</td>
                    <td className="num">{r.spans.toLocaleString()}</td>
                    <td className="mono">{fmtDateTime(new Date(r.firstSeen))}</td>
                    <td className="mono">{fmtDateTime(new Date(r.lastSeen))}</td>
                    <td>
                      {r.ownerName
                        ? <Badge tone="success" title={r.ownerId}>{r.ownerName}</Badge>
                        : <Badge tone="warning" title="Hiçbir Remote Cluster kaydı bu değeri taşımıyor">unmapped</Badge>}
                    </td>
                    <td>
                      {!r.ownerName && (
                        <Row gap={2}>
                          <select value={pick[r.value] ?? ''} onChange={e => setPick(p => ({ ...p, [r.value]: e.target.value }))}>
                            <option value="">— cluster —</option>
                            {clusters.filter(c => c.id).map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
                          </select>
                          <Button type="button" variant="primary" size="xs" disabled={!pick[r.value]} onClick={() => assign(r.value)}>Assign</Button>
                          {msg[r.value] && <span className="field-hint">{msg[r.value]}</span>}
                        </Row>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </div>
    </div>
  );
}
