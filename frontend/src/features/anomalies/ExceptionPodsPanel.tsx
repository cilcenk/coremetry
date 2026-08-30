// ExceptionPodsPanel — v0.10.138 (DETAY SAYFALARI adım 4). Hata grubunun
// pod/node dağılımı: hangi pod (cluster / namespace / node) kaç oluşum, pay,
// son görülme; pod'a / node'a pivot (entityHref: at = o pod'daki son oluşum,
// range = grubun tarama penceresi, servis bağlamı). k8s bağlamsız oluşumlar,
// tavan ve eşlenmemiş cluster'lar AÇIK ilan; eşlenmemişte link yok.
// Bayrak kapalı → null.
//
// v0.10.173 (operatör, prod: "taşma olmasın") — sekiz kolonlu tablo kartı
// yatay kaydırıyordu (node adları + namespace + cluster + tarih). Şimdi
// table-layout:fixed + colgroup: cluster › namespace pod hücresinin alt
// satırına indi (bilgi kaybı yok, title'da tam yol), pod/node hücreleri
// ellipsis + title (CLAUDE.md tuzağı: fixed + nowrap + küçük genişlik
// sessizce kırpar → min/max + ellipsis + title), sayısal kolonlar sabit dar.
// Kart genişliği ne olursa olsun yatay kaydırma yok.
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { Row, Badge } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { useEntityEnabled } from '@/lib/queries';
import { entityHref } from '@/lib/entityHref';
import { fmtDateTime } from '@/lib/utils';
import { windowRangeParam } from '@/lib/urlState';

export function ExceptionPodsPanel({ fingerprint, service, groupOccurrences }: { fingerprint: string; service: string; groupOccurrences: number }) {
  const { enabled } = useEntityEnabled();
  const q = useQuery({
    queryKey: ['exc-pods', fingerprint],
    queryFn: ({ signal }) => api.exceptionGroupPods(fingerprint, signal),
    staleTime: 30_000,
    enabled: enabled && !!fingerprint,
  });
  if (!enabled) return null;
  const d = q.data;
  const rows = d?.rows ?? [];
  const withContext = rows.reduce((a, r) => a + r.occurrences, 0);
  // Pay paydası sunucunun taradığı GRUBA AİT oluşum toplamı (total) — pod
  // tavanı dolunca dönen satırların toplamı yalan söylerdi (inceleme).
  const denom = Math.max(d?.total ?? 0, 1);
  const range = d ? { fromNs: Date.parse(d.from) * 1e6, toNs: Date.parse(d.to) * 1e6 } : undefined;
  // /traces pivotu: grubun penceresi (range) + hata-only (hasError) — /traces'ın
  // OKUDUĞU parametreler; bilinmeyen parametre sessizce düşerdi (inceleme).
  const rangeParam = range ? windowRangeParam(range) : '';
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="ov-card-h">
        <h3>Pods · nodes</h3>
        <span className="ov-sub">{d ? `${rows.length}${d.truncated ? '+' : ''} pod · ${withContext.toLocaleString()} / ${d.total.toLocaleString()} scanned occurrences with pod context${d.sampled ? ` · sampled (newest ${d.scanned.toLocaleString()} rows of ${groupOccurrences.toLocaleString()})` : ''}` : ''}</span>
      </div>
      <div className="ov-card-b">
        {q.isPending ? <Spinner /> : q.error ? (
          <div style={{ color: 'var(--text3)', fontSize: 12 }}>Dağılım yüklenemedi — {String(q.error)}</div>
        ) : d?.schemaMissing ? (
          <div style={{ color: 'var(--text3)', fontSize: 12 }}>Entity şeması (0011) bu ClickHouse'a uygulanmamış — k8s kolonları yok; dağılım hesaplanamaz.</div>
        ) : rows.length === 0 ? (
          <div style={{ color: 'var(--text3)', fontSize: 12 }}>
            {d && d.noContext > 0
              ? `${d.noContext.toLocaleString()} oluşum Kubernetes bağlamsız — span'ler k8s.pod.name taşımıyor ya da entity şeması (0011) öncesi ingest edildi; pod pivotu yok.`
              : 'Tarama penceresinde oluşum bulunamadı (grup penceresi ile spans retention örtüşmüyor olabilir).'}
          </div>
        ) : (
          <>
            <table className="exc-pods-t">
              <colgroup>
                <col className="exc-pods-c-pod" /><col className="exc-pods-c-node" /><col className="exc-pods-c-n" /><col className="exc-pods-c-s" /><col className="exc-pods-c-t" /><col className="exc-pods-c-a" />
              </colgroup>
              <thead>
                <tr><th>pod · cluster › namespace</th><th>node</th><th className="num">occurrences</th><th className="num">share</th><th>last seen</th><th></th></tr>
              </thead>
              <tbody>
                {rows.map(r => {
                  const cid = r.clusterId ?? '';
                  const atMs = Date.parse(r.lastSeen) || undefined;
                  const podHref = cid && !r.hostOnly ? entityHref({ type: 'pod', id: `pod:${cid}/${r.namespace}/${r.pod}`, name: r.pod, namespace: r.namespace, clusterId: cid }, { range, at: atMs, clusterName: r.clusterName, service }) : null;
                  const nodeHref = cid && r.node ? entityHref({ type: 'node', id: `node:${cid}/${r.node}`, name: r.node, clusterId: cid }, { range, at: atMs, clusterName: r.clusterName }) : null;
                  const filters = encodeURIComponent(JSON.stringify([{ k: 'k8s.pod.name', op: '=', v: [r.pod] }]));
                  const tracesHref = `/traces?service=${encodeURIComponent(service)}&filters=${filters}&cluster=${encodeURIComponent(r.cluster)}&hasError=true${rangeParam ? `&range=${encodeURIComponent(rangeParam)}` : ''}`;
                  const share = (100 * r.occurrences) / denom;
                  return (
                    <tr key={`${r.cluster}/${r.namespace}/${r.pod}`}>
                      <td className="mono exc-pods-cell" title={`${r.clusterName ?? r.cluster} › ${r.namespace} › ${r.pod}${cid ? '' : ' (cluster eşlenmemiş)'}`}>
                        <div className="exc-pods-pod">
                          {podHref
                            ? <Link to={podHref} className="sec">{r.pod}</Link>
                            : <span title={r.hostOnly ? 'k8s.namespace.name yok — pod adı host.name yedeğinden; Kubernetes pod\'u değil, link yok' : 'Cluster eşlenmemiş — entity kaydı yok, link yok'}>{r.pod}</span>}
                          {r.hostOnly && <Badge tone="neutral" style={{ marginLeft: 6 }} title="host.name yedeği (k8s bağlamı yok)">host</Badge>}
                        </div>
                        <div className="exc-pods-sub">{r.clusterName ?? r.cluster} › {r.namespace}</div>
                      </td>
                      <td className="mono exc-pods-cell" title={r.node ?? ''}>{nodeHref ? <Link to={nodeHref} className="sec">{r.node}</Link> : (r.node ?? '')}</td>
                      <td className="num">{r.occurrences.toLocaleString()}</td>
                      <td className="num">{share.toFixed(1)}%</td>
                      <td className="mono exc-pods-cell" title={fmtDateTime(new Date(r.lastSeen))}>{fmtDateTime(new Date(r.lastSeen))}</td>
                      <td><Link to={tracesHref} className="sec" title="Bu pod'un hatalı trace'leri">Traces</Link></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            <Row gap={2} wrap>
              {d && d.noContext > 0 && <span className="field-hint">{d.noContext.toLocaleString()} oluşum Kubernetes bağlamsız (k8s.pod.name yok ya da 0011 öncesi) — pivot yok</span>}
              {d?.truncated && <Badge tone="warning" title="Daha fazla pod var; yalnız en yoğun 50 gösteriliyor">ilk 50 pod</Badge>}
              {d?.unmappedClusters && d.unmappedClusters.length > 0 && (
                <Badge tone="warning" title={`Remote Cluster kaydı olmayan cluster değerleri: ${d.unmappedClusters.join(', ')}`}>unmapped: {d.unmappedClusters.join(', ')}</Badge>
              )}
            </Row>
          </>
        )}
      </div>
    </div>
  );
}
