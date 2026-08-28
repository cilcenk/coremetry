import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Stack, Row } from '@/components/ui';
import { Badge } from '@/components/ui/Badge';
import { useEntityEnabled, useEntity, useEntityServices } from '@/lib/queries';
import { fmtDateTime } from '@/lib/utils';

// PodEntityStrip — v0.10.131 (design §5 /pod): cluster › node › namespace ›
// workload › pod şeridi + "services on this pod" (entity katmanı). Bayrak
// kapalıysa ya da cluster kaydı çözülemiyorsa hiçbir şey çizmez — /pod
// sayfasının bugünkü Thanos yolu değişmez.

function short(id: string): string {
  const i = id.lastIndexOf('/');
  return i >= 0 ? id.slice(i + 1) : id.replace(/^[a-z]+:/, '');
}

export function PodEntityStrip({ clusterRef, namespace, pod, range }: {
  clusterRef: string; namespace: string; pod: string; range: { from: number; to: number };
}) {
  const { enabled, clusters } = useEntityEnabled();
  // ?cluster= Thanos ADI ya da id taşır → Remote Cluster id.
  const cid = useMemo(() => clusters.find(c => c.id === clusterRef || c.name === clusterRef)?.id ?? '', [clusters, clusterRef]);
  const id = cid && namespace && pod ? `pod:${cid}/${namespace}/${pod}` : '';
  const entQ = useEntity(id, undefined, enabled && !!id);
  const svcQ = useEntityServices(id, range, enabled && !!id);
  if (!enabled || !id || entQ.isPending || entQ.error || !entQ.data) return null;
  const { entity, parents, node, cluster } = entQ.data;
  const chain = [...parents].reverse(); // cluster › … › parent
  return (
    <Stack gap={2} style={{ marginBottom: 12 }}>
      <Row gap={2} wrap>
        {cluster && <Badge tone="neutral" title={cluster.id}>{cluster.name}</Badge>}
        {node && <><span className="field-hint">›</span><Badge tone="neutral" title={node}>{short(node)}</Badge></>}
        {chain.filter(p => p.type !== 'cluster').map(p => (
          <span key={p.id} className="row" style={{ gap: 6 }}>
            <span className="field-hint">›</span>
            <Badge tone="neutral" title={p.id}>{p.type === 'workload' ? `${p.labels?.kind ?? 'workload'}/${p.name}` : p.name}</Badge>
          </span>
        ))}
        <span className="field-hint">›</span>
        <Badge tone="info" title={entity.id}>{entity.name}</Badge>
        {entity.nameStable && <Badge tone="neutral" title="No pod uid — lifetime split relies on podGap">name-stable</Badge>}
        <span className="field-hint">
          lifetime since {fmtDateTime(new Date(entity.validFrom))}{entity.validTo ? ` → ${fmtDateTime(new Date(entity.validTo))}` : ' (open)'} · source {entity.source}
        </span>
      </Row>
      {svcQ.data && svcQ.data.services.length > 0 && (
        <Row gap={2} wrap>
          <span className="field-hint">services on this pod:</span>
          {svcQ.data.services.map(s => (
            <Link key={s.service} to={`/services/${encodeURIComponent(s.service)}`} className="sec" title={`${s.spans} spans · ${s.errors} errors · avg ${s.avgMs.toFixed(1)} ms`}>
              {s.service}
            </Link>
          ))}
        </Row>
      )}
    </Stack>
  );
}
