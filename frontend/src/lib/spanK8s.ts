// spanK8s — v0.10.137 (DETAY SAYFALARI adım 3: trace pivotları). Bir span'in
// Kubernetes bağlamını (resource attrs → resolveResource, backend coalesce
// zinciriyle aynı) entity linklerine çevirir. Dört sonuç:
//   'ok'               pod linki (+ node/namespace/cluster), at = span başlangıcı
//   'no-k8s'           k8s.pod.name yok → link YOK, "bu span'de Kubernetes bağlamı yok"
//   'no-cluster'       pod var ama cluster değeri yok → link YOK (hangi cluster? bilinmiyor)
//   'unmapped-cluster' cluster değeri hiçbir Remote Cluster'a eşlenmemiş → link YOK, değer ilan
// Aynı pod adı iki cluster'da iki ayrı kayıt: cluster id linke girer. Saf; vitest'li.
import { resolveResource } from './otel/semconv';
import { entityHref } from './entityHref';
import type { EntityClusterInfo, SpanRow, TimeRange } from './types';

// v0.10.148 — 'no-namespace': pod adı var, cluster eşlenmiş ama
// k8s.namespace.name yok (Tempo-fallback / yalnız pod adı çıkaran
// k8sattributes). Pod adı tek başına cluster içinde belirsiz → pod linki YOK
// (traces kolonu traceK8sLinks ile aynı kural); node/cluster linkleri kalır.
// Eskiden `pod:<cid>//<pod>` diye bozuk bir link üretiliyordu.
export type SpanK8sReason = 'ok' | 'no-k8s' | 'no-cluster' | 'unmapped-cluster' | 'no-namespace';

export interface SpanK8sContext {
  reason: SpanK8sReason;
  namespace?: string;
  pod?: string;
  node?: string;
  /** span'deki ham cluster değeri (k8s.cluster.name / openshift.cluster.name / cluster) */
  clusterValue?: string;
  clusterId?: string;
  clusterName?: string;
  /** span başlangıcı, ms — hedef sayfa o an geçerli kaydı çözer */
  atMs: number;
  podHref?: string;
  nodeHref?: string;
  namespaceHref?: string;
  clusterHref?: string;
}

type SpanLike = Pick<SpanRow, 'resourceAttributes' | 'startTime'> & { serviceName?: string };

export function spanK8sContext(span: SpanLike, clusters: EntityClusterInfo[], range?: TimeRange | string | null): SpanK8sContext {
  const r = resolveResource(span.resourceAttributes);
  const atMs = Math.floor((span.startTime || 0) / 1e6);
  const base: SpanK8sContext = { reason: 'no-k8s', atMs, namespace: r.k8s.namespace, pod: r.k8s.pod, node: r.k8s.node, clusterValue: r.cluster };
  if (!r.k8s.pod) return base;
  if (!r.cluster) return { ...base, reason: 'no-cluster' };
  // Yalnız spanClusterValue (sunucu SpanClusterKey ile aynı: değer boşsa ad
  // zaten oraya yazılır). Ad yedeği, değeri farklı bir kaydı yanlış cluster'a
  // bağlayabilirdi (inceleme).
  const c = clusters.find(x => x.spanClusterValue === r.cluster || (x.spanClusterValues ?? []).includes(r.cluster!));
  if (!c) return { ...base, reason: 'unmapped-cluster' };
  const ns = r.k8s.namespace ?? '';
  const opts = { range, at: atMs, clusterName: c.name, service: span.serviceName || undefined };
  const linked = {
    ...base,
    clusterId: c.id,
    clusterName: c.name,
    nodeHref: r.k8s.node ? entityHref({ type: 'node', id: `node:${c.id}/${r.k8s.node}`, name: r.k8s.node, clusterId: c.id }, opts) : undefined,
    clusterHref: entityHref({ type: 'cluster', id: `cluster:${c.id}`, name: c.name, clusterId: c.id }, { range }),
  };
  if (!ns) return { ...linked, reason: 'no-namespace' };
  return {
    ...linked,
    reason: 'ok',
    podHref: entityHref({ type: 'pod', id: `pod:${c.id}/${ns}/${r.k8s.pod}`, name: r.k8s.pod, namespace: ns, clusterId: c.id }, opts),
    namespaceHref: entityHref({ type: 'namespace', id: `ns:${c.id}/${ns}`, name: ns, clusterId: c.id }, opts),
  };
}

// podChipLabel / podChipWhere — v0.10.148 (operator-reported, prod:
// "?/bsa-…" çipleri). Etiket YALNIZ bilinen parçalardan kurulur; bilinmeyen
// parça için '?' basılmaz — eksik ne ise tooltip (where) AÇIKÇA söyler.
export function podChipLabel(ctx: Pick<SpanK8sContext, 'clusterName' | 'clusterValue' | 'namespace' | 'pod'>, multiCluster: boolean): string {
  const cluster = ctx.clusterName ?? ctx.clusterValue;
  const head = multiCluster && cluster ? `${cluster} › ` : '';
  return `${head}${ctx.namespace ? `${ctx.namespace}/` : ''}${ctx.pod ?? ''}`;
}

export function podChipWhere(ctx: Pick<SpanK8sContext, 'clusterName' | 'clusterValue' | 'namespace' | 'pod' | 'reason'>): string {
  const cluster = ctx.clusterName
    ?? (ctx.clusterValue ? `${ctx.clusterValue} (Remote Cluster kaydına eşlenmemiş)` : 'bilinmiyor (span cluster değeri taşımıyor)');
  const ns = ctx.namespace ?? 'yok (k8s.namespace.name taşımıyor)';
  return `cluster: ${cluster} · namespace: ${ns} · pod: ${ctx.pod ?? ''}`;
}

/** Operatöre gösterilecek açık ilan — link olmayan üç durumun gerekçesi. */
export function spanK8sNote(ctx: SpanK8sContext): string | null {
  switch (ctx.reason) {
    case 'no-k8s': return 'Bu span\'de Kubernetes bağlamı yok (k8s.pod.name taşımıyor).';
    case 'no-cluster': return `Pod ${ctx.pod} — span cluster değeri taşımıyor; hangi cluster bilinmiyor, link yok.`;
    case 'unmapped-cluster': return `Pod ${ctx.pod} — cluster değeri "${ctx.clusterValue}" bir Remote Cluster kaydına eşlenmemiş; link yok.`;
    case 'no-namespace': return `Pod ${ctx.pod} — span k8s.namespace.name taşımıyor; pod adı tek başına cluster içinde belirsiz, pod linki yok.`;
    default: return null;
  }
}

export interface TracePodSummary {
  key: string;
  ctx: SpanK8sContext;
  spans: number;
  errors: number;
  services: string[];
}

type TraceSpanLike = SpanLike & Pick<SpanRow, 'serviceName' | 'statusCode'>;

/** tracePods — trace'in span'lerinden AYRIK pod'lar (çok-pod görünürlüğü); at = trace'in en erken span'i. */
export function tracePods(spans: TraceSpanLike[], clusters: EntityClusterInfo[], range?: TimeRange | string | null): { pods: TracePodSummary[]; noContext: number } {
  const byKey = new Map<string, TracePodSummary & { svc: Set<string> }>();
  let noContext = 0;
  const sorted = [...spans].sort((a, b) => a.startTime - b.startTime);
  for (const s of sorted) {
    // Ucuz anahtar önce; entity linkleri yalnız pod'un İLK (en erken) span'inde
    // kurulur — span başına dört href üretmek 5k span'lik trace'te boşa işti (inceleme).
    const r = resolveResource(s.resourceAttributes);
    if (!r.k8s.pod) { noContext++; continue; }
    const key = `${r.cluster ?? ''}/${r.k8s.namespace ?? ''}/${r.k8s.pod}`;
    let e = byKey.get(key);
    if (!e) {
      e = { key, ctx: spanK8sContext(s, clusters, range), spans: 0, errors: 0, services: [], svc: new Set() };
      byKey.set(key, e);
    }
    e.spans++;
    if (s.statusCode === 'error') e.errors++;
    if (s.serviceName) e.svc.add(s.serviceName);
  }
  const pods = [...byKey.values()].map(({ svc, ...rest }) => ({ ...rest, services: [...svc].sort() }));
  pods.sort((a, b) => b.spans - a.spans || a.key.localeCompare(b.key));
  return { pods, noContext };
}

// k8sAttrHref — v0.10.150 (operator-reported: "trace attribute'larından
// odaklara tıklanabilsin"). Span detayındaki resource attribute satırının
// DEĞERİ, çözülmüş bağlamdaki entity linkine gider; çözülemeyen (link yok)
// durumda undefined → satır düz metin kalır (neden Kubernetes bölümünde).
// Saf; SpanDetail.Row'daki spanAttrHref (servis/endpoint) çözücüsünün ikizi.
export function k8sAttrHref(key: string, ctx: SpanK8sContext | null | undefined): string | undefined {
  if (!ctx) return undefined;
  switch (key) {
    case 'k8s.pod.name': return ctx.podHref;
    case 'k8s.namespace.name': return ctx.namespaceHref;
    case 'k8s.node.name': return ctx.nodeHref;
    case 'k8s.cluster.name':
    case 'openshift.cluster.name':
    case 'cluster': return ctx.clusterHref;
    default: return undefined;
  }
}
