// v0.10.143 — traces listesi K8s linkleri: doğru cluster (ikincil değer dahil),
// at = trace ms, range + servis korunur; cluster/namespace kolonu yoksa link YOK + gerekçe.
import { describe, it, expect } from 'vitest';
import { traceK8sHref, withK8sColumns, canAddK8sColumns } from './traceK8sLinks';
import type { EntityClusterInfo } from './types';

const clusters: EntityClusterInfo[] = [
  { id: 'c-a', name: 'prod-eu', spanClusterValue: 'prod-eu-west', spanClusterValues: ['prod-eu-west', 'eu-legacy'] },
];
const t = { startTime: 1_700_000_000_123_456_789, serviceName: 'api', extras: { 'k8s.namespace.name': 'pay', 'k8s.pod.name': 'api-1', 'k8s.node.name': 'w1', cluster: 'eu-legacy' } };
const p = (h: string) => new URLSearchParams(h.slice(h.indexOf('?') + 1));

describe('traceK8sHref', () => {
  it('pod link carries cluster (via secondary value), namespace, at (ms), range — and NO root-span service (values are trace-wide)', () => {
    const { href } = traceK8sHref('k8s.pod.name', t, clusters, '6h');
    expect(href?.startsWith('/pod?')).toBe(true);
    const q = p(href!);
    expect(q.get('cluster')).toBe('prod-eu'); expect(q.get('namespace')).toBe('pay'); expect(q.get('pod')).toBe('api-1');
    expect(q.get('at')).toBe('1700000000123'); expect(q.get('range')).toBe('6h'); expect(q.has('service')).toBe(false);
  });
  it('cluster value also read from k8s.cluster.name / openshift.cluster.name columns', () => {
    const t2 = { ...t, extras: { 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'openshift.cluster.name': 'prod-eu-west' } };
    expect(p(traceK8sHref('k8s.pod.name', t2, clusters).href!).get('cluster')).toBe('prod-eu');
  });
  it('node / namespace / cluster links resolve by cluster id', () => {
    expect(p(traceK8sHref('k8s.node.name', t, clusters).href!).get('id')).toBe('node:c-a/w1');
    expect(p(traceK8sHref('k8s.namespace.name', t, clusters).href!).get('id')).toBe('ns:c-a/pay');
    expect(traceK8sHref('cluster', t, clusters).href).toBe('/clusters?cluster=prod-eu');
  });
  it('no link when the cluster column is missing or unmapped, or the pod has no namespace column', () => {
    expect(traceK8sHref('k8s.pod.name', { ...t, extras: { 'k8s.pod.name': 'api-1' } }, clusters).href).toBeUndefined();
    expect(traceK8sHref('k8s.pod.name', { ...t, extras: { ...t.extras, cluster: 'lab' } }, clusters).note).toContain('"lab"');
    expect(traceK8sHref('k8s.pod.name', { ...t, extras: { 'k8s.pod.name': 'api-1', cluster: 'eu-legacy' } }, clusters).note).toContain('namespace');
    expect(traceK8sHref('k8s.pod.name', { ...t, extras: {} }, clusters)).toEqual({});
  });
  it('withK8sColumns adds cluster FIRST (the one key every link needs), respects the 8-column cap, skips existing', () => {
    expect(withK8sColumns(['http.route', 'k8s.pod.name'])).toEqual(['http.route', 'k8s.pod.name', 'cluster', 'k8s.namespace.name', 'k8s.node.name']);
    expect(withK8sColumns(['a', 'b', 'c', 'd', 'e', 'f', 'g'])).toEqual(['a', 'b', 'c', 'd', 'e', 'f', 'g', 'cluster']);
    expect(canAddK8sColumns(['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'])).toBe(false);
    expect(canAddK8sColumns(['cluster', 'k8s.namespace.name', 'k8s.pod.name', 'k8s.node.name'])).toBe(false);
    expect(canAddK8sColumns([])).toBe(true);
  });
});
