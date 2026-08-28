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
import { tracePods, spanK8sNote } from '@/lib/spanK8s';
import type { SpanRow, TimeRange } from '@/lib/types';

const MAX_CHIPS = 16;

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
  const nodes = new Set(pods.map(p => p.ctx.node).filter(Boolean)).size;
  return (
    <Row gap={2} wrap>
      <span className="field-hint">pods ({pods.length}{nodes ? ` · ${nodes} node` : ''}):</span>
      {pods.slice(0, MAX_CHIPS).map(p => {
        const c = p.ctx;
        const where = `${c.clusterName ?? c.clusterValue ?? '?'} / ${c.namespace ?? '?'} / ${c.pod}`;
        const label = `${multiCluster ? `${c.clusterName ?? c.clusterValue ?? '?'} › ` : ''}${c.namespace ?? '?'}/${c.pod}`;
        const note = spanK8sNote(c);
        const title = `${where} · ${p.spans} span · ${p.errors} hata · ${p.services.join(', ')}${note ? ` · ${note}` : ''}`;
        return c.podHref
          ? <Link key={p.key} to={c.podHref} className="sec" title={title}>{label}{p.errors > 0 ? ` · ${p.errors}✕` : ''}</Link>
          : <Badge key={p.key} tone="neutral" title={title}>{label} · link yok</Badge>;
      })}
      {pods.length > MAX_CHIPS && <span className="field-hint">+{pods.length - MAX_CHIPS}</span>}
      {noContext > 0 && <span className="field-hint">· {noContext} span Kubernetes bağlamsız</span>}
    </Row>
  );
}
