// EntityDetail.tsx — v0.10.135 (DETAY SAYFALARI adım 1; adım 5 node/namespace
// görünümleriyle büyütür). /entity?id=&at=&range= — pod dışı entity'lerin
// (node / namespace / workload / container) hedef sayfası: kimlik +
// geçerlilik + ebeveyn zinciri + etiketler + çocuk sayıları + bu entity
// altındaki servisler ve pod'lar (entity_seen_5m). Ölü entity 404 DEĞİL:
// "artık mevcut değil, son görülme X" + tarihçe. Bayrak kapalı → açık ilan.
import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { PageShell } from '@/components/ui/PageShell';
import { Stack, Row, Badge, Button } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { timeRangeToNs, fmtDateTime } from '@/lib/utils';
import { useEntityEnabled, useEntity, useEntityServices } from '@/lib/queries';
import { entityHref, entityLiveness } from '@/lib/entityHref';
import { serviceHref } from '@/lib/serviceHref';
import type { EntityDetailResponse, EntityServicesResponse, TimeRange } from '@/lib/types';

export default function EntityDetail() {
  const [sp] = useSearchParams();
  const id = sp.get('id') ?? '';
  const at = Number(sp.get('at') ?? 0) || 0;
  const { range, setRange } = usePageZoomRange('1h');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const { enabled, loading } = useEntityEnabled();
  const entQ = useEntity(id, at || undefined, enabled && !!id);
  const svcQ = useEntityServices(id, { from, to }, enabled && !!id && !!entQ.data);
  const name = entQ.data?.entity.name ?? (id.includes('/') ? id.slice(id.lastIndexOf('/') + 1) : id);
  const type = entQ.data?.entity.type ?? (id.includes(':') ? id.slice(0, id.indexOf(':')) : 'entity');
  return (
    <>
      <Topbar title={`${type} · ${name}`} range={range} onRangeChange={setRange} />
      <PageShell>
        {!id && <Empty icon="—" title="Entity belirtilmedi (id parametresi gerekli)." />}
        {!!id && !enabled && !loading && (
          <Empty icon="—" title="Entity katmanı kapalı.">Settings → K8s entity katmanı'ndan açılır; bu sayfa o kayıtları gösterir.</Empty>
        )}
        {!!id && enabled && (entQ.isPending
          ? <Spinner />
          : (entQ.error || !entQ.data)
            ? <Empty icon="—" title="Kayıt bulunamadı."><span className="mono">{id}</span></Empty>
            : <Body data={entQ.data} svc={svcQ.data} svcError={svcQ.error ? String(svcQ.error) : undefined} at={at} pageRange={range} />)}
      </PageShell>
    </>
  );
}

function Body({ data, svc, svcError, at, pageRange }: { data: EntityDetailResponse; svc?: EntityServicesResponse; svcError?: string; at: number; pageRange: TimeRange }) {
  const { entity, parents, children, lifetimes, cluster, atMatch } = data;
  const [showLabels, setShowLabels] = useState(false);
  const live = entityLiveness(entity);
  const hrefOpts = { range: pageRange, at: at || undefined, clusterName: cluster?.name };
  const chain = [...parents].reverse().filter(p => p.type !== 'cluster');
  const labels = Object.entries(entity.labels ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const kids = Object.entries(children ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const rows = svc?.rows ?? [];
  return (
    <Stack gap={4}>
      <Row gap={2} wrap>
        {cluster && (
          <Link to={entityHref({ type: 'cluster', id: `cluster:${cluster.id}`, name: cluster.name, clusterId: cluster.id }, hrefOpts)} className="sec" title={cluster.id}>{cluster.name}</Link>
        )}
        {chain.map(p => (
          <Row key={p.id} gap={2}>
            <span className="field-hint">›</span>
            <Link to={entityHref(p, hrefOpts)} className="sec" title={p.id}>{p.type === 'workload' ? `${p.labels?.kind ?? 'workload'}/${p.name}` : p.name}</Link>
          </Row>
        ))}
        <span className="field-hint">›</span>
        <Badge tone="info" title={entity.id}>{entity.type === 'workload' ? `${entity.labels?.kind ?? 'workload'}/${entity.name}` : entity.name}</Badge>
        {live === 'live' && <Badge tone="success">live</Badge>}
        {live === 'stale' && <Badge tone="warning" title="Son senkronda görülmedi; ömür henüz kapanmadı">stale</Badge>}
        {live === 'gone' && <Badge tone="danger">artık mevcut değil</Badge>}
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
      </Row>
      {kids.length > 0 && (
        <Row gap={2} wrap>
          <span className="field-hint">children:</span>
          {kids.map(([t, n]) => <Badge key={t} tone="neutral">{t} × {n}</Badge>)}
        </Row>
      )}
      {labels.length > 0 && (
        <Row gap={2} wrap>
          <span className="field-hint">labels:</span>
          {(showLabels ? labels : labels.slice(0, 12)).map(([k, v]) => <Badge key={k} tone="neutral" title={`${k}=${v}`}>{k}={v}</Badge>)}
          {labels.length > 12 && (
            <Button variant="secondary" size="xs" onClick={() => setShowLabels(v => !v)}>{showLabels ? 'daha az' : `+${labels.length - 12}`}</Button>
          )}
        </Row>
      )}
      <h3>Services under this {entity.type}</h3>
      {!svc && !svcError && <Spinner />}
      {svcError && <Empty icon="!" title="Servisler yüklenemedi." compact>{svcError}</Empty>}
      {svc && svc.services.length === 0 && <Empty icon="—" title="Bu pencerede bu entity altından span geçmedi." compact />}
      {svc && svc.services.length > 0 && (
        <table>
          <thead><tr><th>service</th><th>pods</th><th>spans</th><th>errors</th><th>avg ms</th></tr></thead>
          <tbody>
            {svc.services.map(s => (
              <tr key={s.service}>
                <td><Link to={serviceHref(s.service, { range: pageRange })} className="sec">{s.service}</Link></td>
                <td className="mono">{s.pods}</td>
                <td className="mono">{s.spans}</td>
                <td className="mono">{s.errors}</td>
                <td className="mono">{s.avgMs.toFixed(1)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {rows.length > 0 && (
        <>
          <h3>Pods × services ({rows.length})</h3>
          <table>
            <thead><tr><th>pod</th><th>namespace</th><th>service</th><th>spans</th><th>errors</th><th>avg ms</th><th>last seen</th></tr></thead>
            <tbody>
              {rows.slice(0, 200).map(r => (
                <tr key={`${r.namespace}/${r.pod}/${r.service}`}>
                  <td>
                    <Link to={entityHref({ type: 'pod', id: `pod:${entity.clusterId}/${r.namespace}/${r.pod}`, name: r.pod, namespace: r.namespace, clusterId: entity.clusterId }, hrefOpts)} className="sec"
                      title={`${cluster?.name ?? entity.clusterId} / ${r.namespace} / ${r.pod}`}>{r.pod}</Link>
                  </td>
                  <td>{r.namespace}</td>
                  <td><Link to={serviceHref(r.service, { range: pageRange })} className="sec">{r.service}</Link></td>
                  <td className="mono">{r.spans}</td>
                  <td className="mono">{r.errors}</td>
                  <td className="mono">{r.avgMs.toFixed(1)}</td>
                  <td className="mono">{fmtDateTime(new Date(r.lastSeen))}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length > 200 && <span className="field-hint">ilk 200 satır gösteriliyor</span>}
        </>
      )}
      {lifetimes.length > 1 && (
        <>
          <h3>Lifetimes ({lifetimes.length})</h3>
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
        </>
      )}
    </Stack>
  );
}
