// TracePodsStrip — v0.10.137 (DETAY SAYFALARI adım 3: trace pivotları).
// Trace'in geçtiği pod'lar (çok-pod görünürlüğü): span'lerin resource
// attr'larından ayrık (cluster, namespace, pod) + span/hata sayısı +
// servisler; her çip entityHref ile pod detayına (at = pod'un en erken
// span'i, range korunur). k8s bağlamı olmayan span sayısı ve eşlenmemiş
// cluster AÇIKÇA ilan edilir (link yok). Bayrak kapalı → null.
import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Row, Badge } from '@/components/ui';
import { useEntityEnabled } from '@/lib/queries';
import { tracePods, spanK8sNote, podChipLabel, podChipWhere } from '@/lib/spanK8s';
import type { SpanRow, TimeRange } from '@/lib/types';

const MAX_CHIPS = 16;

// unlinkedReason — SAF: linksiz pod'ların baskın nedeni, tek kısa ifade.
export function unlinkedReason(reasons: string[]): string {
  const n = (r: string) => reasons.filter(x => x === r).length;
  const top = (['unmapped-cluster', 'no-cluster', 'no-namespace'] as const)
    .map(r => [r, n(r)] as const).sort((a, b) => b[1] - a[1])[0];
  if (!top || top[1] === 0) return 'link yok';
  switch (top[0]) {
    case 'unmapped-cluster': return 'cluster değeri Remote Cluster kaydına eşlenmemiş';
    case 'no-cluster': return 'span cluster değeri taşımıyor';
    case 'no-namespace': return 'k8s.namespace.name yok';
  }
}

export function TracePodsStrip({ spans, range }: { spans: SpanRow[]; range: TimeRange }) {
  const { enabled, clusters } = useEntityEnabled();
  // Tarama yalnız bayrak açıkken (inceleme: kapı memo'dan önceydi, her render'da
  // tüm span'ler taranıyordu).
  const summary = useMemo(() => (enabled ? tracePods(spans, clusters, range) : null), [enabled, spans, clusters, range]);
  if (!enabled || !summary || spans.length === 0) return null;
  const { pods, noContext } = summary;
  if (pods.length === 0) {
    return (
      <Row gap={2} wrap>
        <span className="field-hint">Kubernetes: bu trace'in hiçbir span'i k8s.pod.name taşımıyor ({noContext} span) — pod pivotu yok.</span>
      </Row>
    );
  }
  const multiCluster = new Set(pods.map(p => p.ctx.clusterValue ?? '')).size > 1;
  // Toplu link-yok notu: sayı + baskın neden (tek kısa cümle).
  const unlinkedPods = pods.filter(p => !p.ctx.podHref);
  const unlinked = unlinkedPods.length;
  const unlinkedWhy = unlinkedReason(unlinkedPods.map(p => p.ctx.reason));
  const nodes = new Set(pods.map(p => p.ctx.node).filter(Boolean)).size;
  return (
    <Row gap={2} wrap>
      <span className="field-hint">pods ({pods.length}{nodes ? ` · ${nodes} node` : ''}):</span>
      {pods.slice(0, MAX_CHIPS).map(p => {
        const c = p.ctx;
        // v0.10.148 (operator-reported, prod "?/pod"): '?' yok — etiket bilinen
        // parçalardan, eksik olan tooltip'te açıkça (podChipWhere).
        const where = podChipWhere(c);
        const label = podChipLabel(c, multiCluster);
        const note = spanK8sNote(c);
        const title = `${where} · ${p.spans} span · ${p.errors} hata · ${p.services.join(', ')}${note ? ` · ${note}` : ''}`;
        // v0.10.150 (operator-reported, prod 26 pod): çip başına "· link yok"
        // KALDIRILDI — kalabalık. Linksiz çip sönük Badge, nedeni tooltip'te;
        // toplu bir not çiplerin sonunda (aşağıda).
        return c.podHref
          ? <Link key={p.key} to={c.podHref} className="sec" title={title}>{label}{p.errors > 0 ? ` · ${p.errors}✕` : ''}</Link>
          : <Badge key={p.key} tone="neutral" title={title}>{label}{p.errors > 0 ? ` · ${p.errors}✕` : ''}</Badge>;
      })}
      {pods.length > MAX_CHIPS && <span className="field-hint">+{pods.length - MAX_CHIPS}</span>}
      {unlinked > 0 && <span className="field-hint" title={unlinkedWhy}>· {unlinked} pod linksiz ({unlinkedWhy})</span>}
      {noContext > 0 && <span className="field-hint">· {noContext} span Kubernetes bağlamsız</span>}
    </Row>
  );
}
