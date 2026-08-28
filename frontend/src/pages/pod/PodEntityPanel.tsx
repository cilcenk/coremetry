// PodEntityPanel — v0.10.135 (DETAY SAYFALARI adım 1; v0.10.131 şeridinin
// büyümüş hali). Bayrak açıkken /pod sayfasının entity bloğu:
//  - kimlik zinciri: cluster › node › namespace › workload › pod — hepsi link,
//    DOĞRU cluster'a, range + at bağlamıyla (entityHref)
//  - geçerlilik: live / stale / ARTIK YOK (son görülme + tarihçe); ?at=
//    verilmiş ve o an geçerli kayıt yoksa bunu SÖYLER (atMatch=false)
//  - etiketler, konteyner durumları (Thanos KSM), bu pod'daki servisler,
//    kardeş pod'lar (aynı workload)
// Bayrak kapalı / kayıt yok → null (sayfa v0.10.130 öncesi gibi çizilir).
// Pod adı asla tek başına değil: cluster + namespace hep title'da/yanında.
import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Stack, Row, Badge, Button } from '@/components/ui';
import { useEntityEnabled, useEntity, useEntityServices, useEntityContainers } from '@/lib/queries';
import { fmtDateTime } from '@/lib/utils';
import { entityHref, entityLiveness } from '@/lib/entityHref';
import { serviceHref } from '@/lib/serviceHref';
import type { EntityContainerStatus, TimeRange } from '@/lib/types';

function short(id: string): string {
  const i = id.lastIndexOf('/');
  return i >= 0 ? id.slice(i + 1) : id.replace(/^[a-z]+:/, '');
}

function containerTone(c: EntityContainerStatus): 'neutral' | 'success' | 'danger' | 'warning' {
  if (!c.readyKnown) return 'neutral';
  if (!c.ready) return 'danger';
  return c.restarts > 0 || c.lastTermReason ? 'warning' : 'success';
}

function containerText(c: EntityContainerStatus): string {
  const parts = [c.name];
  if (c.readyKnown) parts.push(c.ready ? 'ready' : 'not ready');
  if (c.restarts > 0) parts.push(`${c.restarts} restarts`);
  if (c.waitingReason) parts.push(c.waitingReason);
  if (c.lastTermReason) parts.push(`last: ${c.lastTermReason}`);
  return parts.join(' · ');
}

export function PodEntityPanel({ clusterRef, namespace, pod, range, at, pageRange }: {
  clusterRef: string; namespace: string; pod: string;
  /** ns — servis sağlığı penceresi (entity_seen_5m) */
  range: { from: number; to: number };
  /** ms — tarihsel bağlam (?at=) */
  at?: number;
  /** linklere taşınan pencere */
  pageRange: TimeRange;
}) {
  const { enabled, clusters } = useEntityEnabled();
  const cid = useMemo(() => clusters.find(c => c.id === clusterRef || c.name === clusterRef)?.id ?? '', [clusters, clusterRef]);
  const id = cid && namespace && pod ? `pod:${cid}/${namespace}/${pod}` : '';
  const entQ = useEntity(id, at || undefined, enabled && !!id);
  const svcQ = useEntityServices(id, range, enabled && !!id);
  const live = entQ.data ? entityLiveness(entQ.data.entity) : 'gone';
  const ctrQ = useEntityContainers(id, enabled && !!id && !!entQ.data && live !== 'gone');
  const [showLabels, setShowLabels] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  if (!enabled || !id || entQ.isPending || entQ.error || !entQ.data) return null;
  const { entity, parents, node, cluster, siblings, containers, lifetimes, atMatch } = entQ.data;
  const hrefOpts = { range: pageRange, at: at || undefined, clusterName: cluster?.name };
  const chain = [...parents].reverse().filter(p => p.type !== 'cluster');
  const labels = Object.entries(entity.labels ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const nodeRec = node ? { type: 'node' as const, id: node, name: short(node), clusterId: entity.clusterId } : null;
  // Kardeşler = aynı parent altındakiler. Parent workload ise "aynı iş yükü",
  // span-kaynaklı pod'da parent namespace'tir → "aynı namespace'teki pod'lar";
  // ikisini tek etiketle anmak yanlış iddia olurdu.
  const nearest = parents[0];
  const siblingLabel = nearest?.type === 'workload'
    ? `sibling pods · ${nearest.labels?.kind ?? 'workload'}/${nearest.name}`
    : nearest?.type === 'namespace' ? `other pods in namespace ${nearest.name}` : 'sibling pods';
  const ctrs = ctrQ.data?.containers ?? [];
  return (
    <Stack gap={3}>
      <Row gap={2} wrap>
        {cluster && (
          <Link to={entityHref({ type: 'cluster', id: `cluster:${cluster.id}`, name: cluster.name, clusterId: cluster.id }, hrefOpts)} className="sec" title={cluster.id}>
            {cluster.name}
          </Link>
        )}
        {nodeRec && <><span className="field-hint">›</span><Link to={entityHref(nodeRec, hrefOpts)} className="sec" title={node}>{nodeRec.name}</Link></>}
        {chain.map(p => (
          <Row key={p.id} gap={2}>
            <span className="field-hint">›</span>
            <Link to={entityHref(p, hrefOpts)} className="sec" title={p.id}>
              {p.type === 'workload' ? `${p.labels?.kind ?? 'workload'}/${p.name}` : p.name}
            </Link>
          </Row>
        ))}
        <span className="field-hint">›</span>
        <Badge tone="info" title={entity.id}>{entity.name}</Badge>
        {live === 'live' && <Badge tone="success">live</Badge>}
        {live === 'stale' && <Badge tone="warning" title="Son senkronda görülmedi; ömür henüz kapanmadı">stale</Badge>}
        {live === 'gone' && <Badge tone="danger">artık mevcut değil</Badge>}
        {entity.nameStable && <Badge tone="neutral" title="No pod uid — lifetime split relies on podGap">name-stable</Badge>}
      </Row>
      <Row gap={2} wrap>
        <span className="field-hint">
          {live === 'gone'
            ? `son görülme ${fmtDateTime(new Date(entity.lastSeen))} · ömür ${fmtDateTime(new Date(entity.validFrom))} → ${entity.validTo ? fmtDateTime(new Date(entity.validTo)) : '—'}`
            : `ömür ${fmtDateTime(new Date(entity.validFrom))} → (açık) · son görülme ${fmtDateTime(new Date(entity.lastSeen))}`}
          {' · '}kaynak {entity.source}{entity.uid ? ` · uid ${entity.uid}` : ''}
        </span>
        {!!at && atMatch === false && (
          <Badge tone="warning" title={`İstenen an: ${fmtDateTime(new Date(at))}`}>o anda geçerli kayıt yok — en yakın ömür gösteriliyor</Badge>
        )}
        {lifetimes.length > 1 && (
          <Button variant="secondary" size="xs" onClick={() => setShowHistory(v => !v)}>
            {showHistory ? 'tarihçeyi gizle' : `tarihçe (${lifetimes.length} ömür)`}
          </Button>
        )}
      </Row>
      {showHistory && (
        <table>
          <thead><tr><th>valid from</th><th>valid to</th><th>source</th><th>uid</th></tr></thead>
          <tbody>
            {lifetimes.map(l => (
              <tr key={`${l.validFrom}|${l.uid ?? ''}`}>
                <td className="mono">{fmtDateTime(new Date(l.validFrom))}</td>
                <td className="mono">{l.validTo ? fmtDateTime(new Date(l.validTo)) : '(açık)'}</td>
                <td>{l.source}</td>
                <td className="mono">{l.uid ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {labels.length > 0 && (
        <Row gap={2} wrap>
          <span className="field-hint">labels:</span>
          {(showLabels ? labels : labels.slice(0, 8)).map(([k, v]) => <Badge key={k} tone="neutral" title={`${k}=${v}`}>{k}={v}</Badge>)}
          {labels.length > 8 && (
            <Button variant="secondary" size="xs" onClick={() => setShowLabels(v => !v)}>{showLabels ? 'daha az' : `+${labels.length - 8}`}</Button>
          )}
        </Row>
      )}
      {live !== 'gone' && (
        <Row gap={2} wrap>
          <span className="field-hint">containers:</span>
          {ctrQ.isPending && <span className="field-hint">…</span>}
          {ctrQ.data?.error && <Badge tone="warning" title={ctrQ.data.error}>Thanos: durum alınamadı</Badge>}
          {ctrQ.data && !ctrQ.data.error && ctrs.length === 0 && (
            <span className="field-hint">KSM serisi yok{containers && containers.length > 0 ? ` · ${containers.map(c => c.name).join(', ')}` : ''}</span>
          )}
          {ctrs.map(c => (
            <Badge key={c.name} tone={containerTone(c)} title={c.waitingReason || c.lastTermReason ? [c.waitingReason && `waiting: ${c.waitingReason}`, c.lastTermReason && `last terminated: ${c.lastTermReason}`].filter(Boolean).join(' · ') : undefined}>
              {containerText(c)}
            </Badge>
          ))}
        </Row>
      )}
      {svcQ.data && svcQ.data.services.length > 0 && (
        <Row gap={2} wrap>
          <span className="field-hint">services on this pod:</span>
          {svcQ.data.services.map(s => (
            <Link key={s.service} to={serviceHref(s.service, { range: pageRange })} className="sec"
              title={`${s.spans} spans · ${s.errors} errors · avg ${s.avgMs.toFixed(1)} ms`}>
              {s.service}
            </Link>
          ))}
        </Row>
      )}
      {siblings && siblings.length > 0 && (
        <Row gap={2} wrap>
          <span className="field-hint">{siblingLabel} ({siblings.length}{siblings.length >= 50 ? '+' : ''}):</span>
          {siblings.slice(0, 12).map(p => (
            <Link key={p.id} to={entityHref(p, hrefOpts)} className="sec" title={`${cluster?.name ?? p.clusterId} / ${p.namespace ?? ''} / ${p.name}`}>
              {p.name}
            </Link>
          ))}
          {siblings.length > 12 && <span className="field-hint">+{siblings.length - 12}</span>}
        </Row>
      )}
    </Stack>
  );
}
