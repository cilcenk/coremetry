// traceK8sLinks — v0.10.143 (DETAY SAYFALARI adım 6: traces listesi).
// Bir trace satırının Kubernetes kolonları (k8s.namespace.name / k8s.pod.name /
// k8s.node.name / cluster) entity sayfalarına link olur: doğru cluster
// (span cluster değeri → Remote Cluster, spanClusterValues), at = trace
// başlangıcı (ms), range korunur, pod linki servis bağlamını taşır. Cluster
// çözülemezse link YOK (düz metin + gerekçe). Saf; vitest'li.
import { entityHref } from './entityHref';
import type { EntityClusterInfo, TimeRange, TraceRow } from './types';

// Sıra ÖNEMLİ: `cluster` önce — link için tek zorunlu anahtar; 8 kolon tavanı
// kuyruğu düşürür (inceleme). Değer terfi `cluster` kolonundan (coalesce zinciri).
export const TRACE_K8S_COLS = ['cluster', 'k8s.namespace.name', 'k8s.pod.name', 'k8s.node.name'] as const;
/** Operatörün kendi kolon listesinde cluster değeri bu adlarla da yaşayabilir. */
const CLUSTER_KEYS = ['cluster', 'k8s.cluster.name', 'openshift.cluster.name'] as const;
export type TraceK8sCol = typeof TRACE_K8S_COLS[number];

export function isTraceK8sCol(id: string): id is TraceK8sCol {
  return (TRACE_K8S_COLS as readonly string[]).includes(id);
}

export interface TraceK8sLink { href?: string; note?: string }

/** clusters: /api/entities/clusters (spanClusterValues ile eşleşme). */
export function traceK8sHref(col: TraceK8sCol, t: Pick<TraceRow, 'extras' | 'startTime'>, clusters: EntityClusterInfo[], range?: TimeRange | string | null): TraceK8sLink {
  const ex = t.extras ?? {};
  const value = ex[col] ?? '';
  if (!value) return {};
  const clusterValue = CLUSTER_KEYS.map(k => ex[k] ?? '').find(Boolean) ?? '';
  const c = clusterValue ? clusters.find(x => x.spanClusterValue === clusterValue || (x.spanClusterValues ?? []).includes(clusterValue)) : undefined;
  if (!c) {
    return { note: clusterValue ? `cluster değeri "${clusterValue}" bir Remote Cluster kaydına eşlenmemiş — link yok` : 'cluster kolonu yok — hangi cluster bilinmiyor, link yok' };
  }
  const atMs = Math.floor((t.startTime || 0) / 1e6);
  const ns = ex['k8s.namespace.name'] ?? '';
  const opts = { range, at: atMs, clusterName: c.name };
  switch (col) {
    case 'cluster':
      return { href: entityHref({ type: 'cluster', id: `cluster:${c.id}`, name: c.name, clusterId: c.id }, { range }) };
    case 'k8s.namespace.name':
      return { href: entityHref({ type: 'namespace', id: `ns:${c.id}/${value}`, name: value, clusterId: c.id }, opts) };
    case 'k8s.node.name':
      return { href: entityHref({ type: 'node', id: `node:${c.id}/${value}`, name: value, clusterId: c.id }, opts) };
    case 'k8s.pod.name':
      if (!ns) return { note: 'k8s.namespace.name kolonu yok — pod adı tek başına cluster içinde belirsiz, link yok' };
      // Servis bağlamı BİLİNÇLİ yok: pod/namespace değerleri trace-geneli
      // any() örnekleri, serviceName ise kök span'in servisi — ikisi aynı
      // pod'a ait olmayabilir (inceleme). /pod entity yoluyla kendi kapsamını kurar.
      return { href: entityHref({ type: 'pod', id: `pod:${c.id}/${ns}/${value}`, name: value, namespace: ns, clusterId: c.id }, opts) };
  }
}

/** Kubernetes kolon seti: 8 kolon tavanına uyacak biçimde eklenir (mevcutlar atlanır; cluster önce). */
export function withK8sColumns(extraCols: string[], cap = 8): string[] {
  const out = [...extraCols];
  for (const k of TRACE_K8S_COLS) {
    if (out.length >= cap) break;
    if (!out.includes(k)) out.push(k);
  }
  return out;
}

/** Düğme yalnız gerçekten bir şey ekleyecekse görünür (tavan dolu / hepsi ekli → gizli). */
export function canAddK8sColumns(extraCols: string[], cap = 8): boolean {
  return withK8sColumns(extraCols, cap).length > extraCols.length;
}
