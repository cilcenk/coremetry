// EntityDetail.tsx — v0.10.135 (DETAY SAYFALARI adım 1; adım 5 node/namespace
// görünümleriyle büyütür). /entity?id=&at=&range= — pod dışı entity'lerin
// (node / namespace / workload / container) hedef sayfası: kimlik +
// geçerlilik + ebeveyn zinciri + etiketler + çocuk sayıları + bu entity
// altındaki servisler ve pod'lar (entity_seen_5m). Ölü entity 404 DEĞİL:
// "artık mevcut değil, son görülme X" + tarihçe. Bayrak kapalı → açık ilan.
import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Stat } from '@/features/dependencies/panels/shared';
import { namedSeriesToSeries } from '@/pages/clusters/trendSeries';
import { clampThanosWindow, THANOS_MAX_WINDOW_LABEL } from '@/lib/thanosWindow';
import { fmtBytes } from '@/lib/utils';
import { fmtCores } from '@/pages/clusters/thresholds';
import { ThanosTrendPanel } from '@/pages/clusters/TrendPanel';
import { Topbar } from '@/components/Topbar';
import { PageShell } from '@/components/ui/PageShell';
import { Stack, Row, Badge, Button } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { timeRangeToNs, fmtDateTime } from '@/lib/utils';
import { useEntityEnabled, useEntity, useEntityServices, useEntityLatency } from '@/lib/queries';
import { entityHref, entityLiveness } from '@/lib/entityHref';
import { serviceHref } from '@/lib/serviceHref';
import type { EntityDetailResponse, EntityRecord, EntityServicesResponse, TimeRange } from '@/lib/types';

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
            : <Body data={entQ.data} svc={svcQ.data} svcError={svcQ.error ? String(svcQ.error) : undefined} at={at} pageRange={range} from={from} to={to} />)}
      </PageShell>
    </>
  );
}

function Body({ data, svc, svcError, at, pageRange, from, to }: { data: EntityDetailResponse; svc?: EntityServicesResponse; svcError?: string; at: number; pageRange: TimeRange; from: number; to: number }) {
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
      {/* v0.10.139 (adım 5) — node / namespace: kapasite + kullanım (Thanos),
          CPU/Mem trend, giriş-span latency özeti, etki (pod/servis sayısı). */}
      {entity.type === 'node' && cluster && <NodePanel entity={entity} clusterName={cluster.name} from={from} to={to} svc={svc} />}
      {entity.type === 'namespace' && cluster && <NamespacePanel entity={entity} clusterName={cluster.name} from={from} to={to} svc={svc} />}
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

function msLabel(v?: number): string {
  if (v === undefined || v <= 0) return '—';
  return `${v < 10 ? v.toFixed(1) : v.toFixed(0)} ms`;
}

// Etki + latency KPI şeridi: pod/servis sayısı entity_seen satırlarından,
// p50/p95/p99 + hata ham spans'ten (/api/entity/latency).
function ImpactStrip({ id, from, to, svc }: { id: string; from: number; to: number; svc?: EntityServicesResponse }) {
  const latQ = useEntityLatency(id, { from, to });
  const l = latQ.data?.latency;
  const rows = svc?.rows ?? [];
  const spans = rows.reduce((a, r) => a + r.spans, 0);
  const errors = rows.reduce((a, r) => a + r.errors, 0);
  // Sunucu tavanları (pod ≤ 500, satır ≤ 2000): dolunca toplam KISMİ — ilan (inceleme).
  const capped = !!svc && (svc.pods.length >= 500 || rows.length >= 2000);
  const lat = latQ.data;
  return (
    <Row gap={3} wrap>
      <Stat label="pods" value={svc ? `${svc.pods.length}${capped ? '+' : ''}` : '—'} sub={capped ? 'tavan: ilk 500 pod' : undefined} tone={capped ? 'warn' : undefined} />
      <Stat label="services" value={svc ? String(svc.services.length) : '—'} />
      <Stat label="spans" value={svc ? `${spans.toLocaleString()}${capped ? '+' : ''}` : '—'} sub={capped ? 'kısmi (satır tavanı)' : 'entity_seen_5m'} />
      <Stat label="error %" value={svc && spans ? `${((100 * errors) / spans).toFixed(2)}%` : '—'} tone={spans && errors / spans >= 0.05 ? 'err' : undefined} />
      <Stat label="entry p50" value={latQ.isError ? 'hata' : msLabel(l?.p50Ms)} />
      <Stat label="entry p95" value={latQ.isError ? 'hata' : msLabel(l?.p95Ms)} />
      <Stat label="entry p99" value={latQ.isError ? 'hata' : msLabel(l?.p99Ms)} sub={l ? `${l.entrySpans.toLocaleString()} entry spans${lat?.clamped ? ' · son 24 s' : ''}` : undefined} />
    </Row>
  );
}

function NodePanel({ entity, clusterName, from, to, svc }: { entity: EntityRecord; clusterName: string; from: number; to: number; svc?: EntityServicesResponse }) {
  const { cFrom, cTo, clamped } = useMemo(() => clampThanosWindow(from, to), [from, to]);
  const nodesQ = useQuery({
    queryKey: ['entity-node-rows', clusterName],
    queryFn: () => api.clusterNodes(clusterName),
    staleTime: 60_000, enabled: !!clusterName,
  });
  // Tek node sorgusu (node= parametresi, topk yok — inceleme: cluster-genel
  // topk(8) 9. node'u boş bırakıyordu). Seçici node-exporter instance host'u:
  // entity kaydının internal_ip etiketi (kube_node_info) ya da relabel'lı
  // kurulumlarda node adı.
  const ip = entity.labels?.internal_ip ?? '';
  const nodeSel = ip || entity.name;
  const cpuQ = useQuery({
    queryKey: ['entity-node-trend', clusterName, 'cpu', nodeSel, cFrom, cTo],
    queryFn: () => api.clusterResourceTrend(clusterName, 'cpu', true, cFrom, cTo, nodeSel),
    staleTime: 60_000, enabled: !!clusterName,
  });
  const memQ = useQuery({
    queryKey: ['entity-node-trend', clusterName, 'mem', nodeSel, cFrom, cTo],
    queryFn: () => api.clusterResourceTrend(clusterName, 'mem', true, cFrom, cTo, nodeSel),
    staleTime: 60_000, enabled: !!clusterName,
  });
  const row = nodesQ.data?.nodes?.find(n => n.node === entity.name);
  // Sunucu tek node'a kapsadı: dönen seri(ler) bu node'undur (ad instance host).
  const pick = (series: { name: string; points: { bucket: number; value: number }[] }[] | null | undefined) =>
    namedSeriesToSeries(series ?? [], entity.name);
  const cpuSeries = useMemo(() => pick(cpuQ.data?.series), [cpuQ.data, entity.name]); // eslint-disable-line react-hooks/exhaustive-deps
  const memSeries = useMemo(() => pick(memQ.data?.series), [memQ.data, entity.name]); // eslint-disable-line react-hooks/exhaustive-deps
  const xRange = useMemo(() => ({ from: cFrom / 1e9, to: cTo / 1e9 }), [cFrom, cTo]);
  return (
    <Stack gap={3}>
      <Row gap={3} wrap>
        <Stat label="role" value={row?.role ?? '—'} />
        <Stat label="CPU" value={row ? fmtCores(row.cpuCores) : nodesQ.isPending ? '…' : '—'} sub={row?.cpuPct != null ? `${row.cpuPct.toFixed(0)}% of capacity` : undefined} tone={row?.cpuPct != null && row.cpuPct >= 90 ? 'err' : undefined} />
        <Stat label="Mem" value={row ? fmtBytes(row.memBytes) : nodesQ.isPending ? '…' : '—'} sub={row?.memPct != null ? `${row.memPct.toFixed(0)}% of capacity` : undefined} tone={row?.memPct != null && row.memPct >= 90 ? 'err' : undefined} />
        {row?.netInBps != null && <Stat label="net in" value={`${fmtBytes(row.netInBps)}/s`} />}
        {row?.netOutBps != null && <Stat label="net out" value={`${fmtBytes(row.netOutBps)}/s`} />}
        {!row && !nodesQ.isPending && <span className="field-hint">Thanos'ta bu node için kapasite serisi yok{nodesQ.isError ? ' (sorgu hatası)' : ''}.</span>}
        {clamped && <span className="field-hint">trend: son {THANOS_MAX_WINDOW_LABEL}</span>}
      </Row>
      <ImpactStrip id={entity.id} from={from} to={to} svc={svc} />
      <div className="ov-grid ov-charts-3 ov-mb">
        <div>
          <div className="field-hint">CPU (cores)</div>
          {cpuQ.isPending ? <Spinner /> : cpuSeries.length === 0
            ? <Empty icon="—" title="Bu node için CPU serisi yok." compact>{cpuQ.isError ? 'Thanos sorgusu hata verdi.' : `node-exporter instance'ı ${nodeSel} ile eşleşmedi (kube_node_info internal_ip / node adı).`}</Empty>
            : <MultiLineChart series={cpuSeries} height={180} unit="cores" xRange={xRange} syncKey={`entity-node:${entity.id}`} />}
        </div>
        <div>
          <div className="field-hint">Memory (bytes)</div>
          {memQ.isPending ? <Spinner /> : memSeries.length === 0
            ? <Empty icon="—" title="Bu node için bellek serisi yok." compact>{memQ.isError ? 'Thanos sorgusu hata verdi.' : `node-exporter instance'ı ${nodeSel} ile eşleşmedi (kube_node_info internal_ip / node adı).`}</Empty>
            : <MultiLineChart series={memSeries} height={180} unit="bytes" xRange={xRange} syncKey={`entity-node:${entity.id}`} />}
        </div>
      </div>
    </Stack>
  );
}

function NamespacePanel({ entity, clusterName, from, to, svc }: { entity: EntityRecord; clusterName: string; from: number; to: number; svc?: EntityServicesResponse }) {
  const { cFrom, cTo, clamped } = useMemo(() => clampThanosWindow(from, to), [from, to]);
  const nsQ = useQuery({
    queryKey: ['entity-ns-rows', clusterName],
    queryFn: () => api.clusterNamespaces(clusterName),
    staleTime: 60_000, enabled: !!clusterName,
  });
  const row = nsQ.data?.namespaces?.find(n => n.namespace === entity.name);
  return (
    <Stack gap={3}>
      <Row gap={3} wrap>
        <Stat label="pods (k8s)" value={row?.pods != null ? String(row.pods) : nsQ.isPending ? '…' : '—'} />
        <Stat label="CPU" value={row ? fmtCores(row.cpuCores) : '—'} />
        <Stat label="Mem" value={row ? fmtBytes(row.memBytes) : '—'} />
        <Stat label="restarts" value={row?.restarts != null ? String(row.restarts) : '—'} tone={row?.restarts ? 'warn' : undefined} />
        <Stat label="failing pods" value={row?.failing != null ? String(row.failing) : '—'} tone={row?.failing ? 'err' : undefined} />
        {!row && !nsQ.isPending && <span className="field-hint">Thanos'ta bu namespace için seri yok{nsQ.isError ? ' (sorgu hatası)' : ''}.</span>}
        {clamped && <span className="field-hint">trend: son {THANOS_MAX_WINDOW_LABEL}</span>}
      </Row>
      <ImpactStrip id={entity.id} from={from} to={to} svc={svc} />
      <ThanosTrendPanel cluster={clusterName} namespace={entity.name} fromNs={cFrom} toNs={cTo} />
    </Stack>
  );
}
