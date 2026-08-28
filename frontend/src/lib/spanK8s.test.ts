// v0.10.137 — adım 3 sözleşmeleri: k8s bağlamsız span → link yok + açık ilan;
// eşlenmemiş cluster → link yok + değer; eşlenmiş → at = span zamanı (ms),
// cluster id linkte; aynı pod adı iki cluster'da iki ayrı link; tracePods
// ayrık pod sayımı + hata sayısı + servisler.
import { describe, it, expect } from 'vitest';
import { spanK8sContext, spanK8sNote, tracePods } from './spanK8s';
import type { EntityClusterInfo } from './types';

const clusters: EntityClusterInfo[] = [
  { id: 'c-a', name: 'prod-eu', spanClusterValue: 'prod-eu-west' },
  { id: 'c-b', name: 'prod-us', spanClusterValue: 'prod-us-east' },
];
const span = (ra: Record<string, string>, startNs = 1_700_000_000_123_456_789, extra: Partial<{ serviceName: string; statusCode: string }> = {}) =>
  ({ resourceAttributes: ra, startTime: startNs, serviceName: extra.serviceName ?? 'api', statusCode: extra.statusCode ?? 'ok' });

function params(href: string) { return new URLSearchParams(href.slice(href.indexOf('?') + 1)); }

describe('spanK8sContext', () => {
  it('no k8s.pod.name → no-k8s, no links, explicit note', () => {
    const ctx = spanK8sContext(span({ 'service.name': 'api', 'host.name': 'vm-1' }), clusters);
    expect(ctx.reason).toBe('no-k8s');
    expect(ctx.podHref).toBeUndefined();
    expect(spanK8sNote(ctx)).toMatch(/Kubernetes bağlamı yok/);
  });
  it('unmapped cluster → no links, value announced', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'k8s.cluster.name': 'lab' }), clusters);
    expect(ctx.reason).toBe('unmapped-cluster');
    expect(ctx.podHref).toBeUndefined();
    expect(spanK8sNote(ctx)).toContain('"lab"');
  });
  it('pod without any cluster value → no-cluster', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1' }), clusters);
    expect(ctx.reason).toBe('no-cluster');
    expect(ctx.podHref).toBeUndefined();
  });
  it('mapped cluster → pod/node links carry cluster, namespace, at (ms of span start) and range', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'k8s.node.name': 'w1', 'openshift.cluster.name': 'prod-eu-west' }), clusters, '6h');
    expect(ctx.reason).toBe('ok');
    const p = params(ctx.podHref!);
    expect(p.get('cluster')).toBe('prod-eu');
    expect(p.get('namespace')).toBe('pay');
    expect(p.get('pod')).toBe('api-1');
    expect(p.get('at')).toBe('1700000000123');
    expect(p.get('range')).toBe('6h');
    expect(params(ctx.nodeHref!).get('id')).toBe('node:c-a/w1');
    expect(params(ctx.namespaceHref!).get('id')).toBe('ns:c-a/pay');
    expect(ctx.clusterHref).toBe('/clusters?cluster=prod-eu&range=6h');
  });
  it('same pod name in two clusters → two different pod links', () => {
    const a = spanK8sContext(span({ 'k8s.pod.name': 'kafka-0', 'k8s.namespace.name': 'infra', 'k8s.cluster.name': 'prod-eu-west' }), clusters);
    const b = spanK8sContext(span({ 'k8s.pod.name': 'kafka-0', 'k8s.namespace.name': 'infra', 'k8s.cluster.name': 'prod-us-east' }), clusters);
    expect(a.podHref).not.toBe(b.podHref);
    expect(params(a.podHref!).get('cluster')).toBe('prod-eu');
    expect(params(b.podHref!).get('cluster')).toBe('prod-us');
  });
  it('matches ONLY spanClusterValue — a Remote Cluster name is not a span value (inceleme: yanlış cluster bağlama)', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.cluster.name': 'prod-us' }), clusters);
    expect(ctx.reason).toBe('unmapped-cluster');
    expect(ctx.podHref).toBeUndefined();
  });
});

describe('tracePods', () => {
  it('distinct pods with span/error counts and services; no-context spans counted separately', () => {
    const base = 1_700_000_000_000_000_000; // ns; ofsetler ms cinsinden
    const spans = [
      span({ 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'k8s.cluster.name': 'prod-eu-west' }, base + 3e6, { serviceName: 'api' }),
      span({ 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'k8s.cluster.name': 'prod-eu-west' }, base + 1e6, { serviceName: 'api', statusCode: 'error' }),
      span({ 'k8s.pod.name': 'db-0', 'k8s.namespace.name': 'pay', 'k8s.cluster.name': 'prod-eu-west' }, base + 2e6, { serviceName: 'db' }),
      span({ 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'k8s.cluster.name': 'prod-us-east' }, base + 2.5e6, { serviceName: 'api' }),
      span({ 'service.name': 'legacy' }, base + 0.5e6, { serviceName: 'legacy' }),
    ];
    const { pods, noContext } = tracePods(spans, clusters);
    expect(noContext).toBe(1);
    expect(pods.map(p => p.key)).toEqual(['prod-eu-west/pay/api-1', 'prod-eu-west/pay/db-0', 'prod-us-east/pay/api-1']);
    expect(pods[0].spans).toBe(2);
    expect(pods[0].errors).toBe(1);
    expect(pods[0].services).toEqual(['api']);
    // at = pod'un EN ERKEN span'i (ms): base + 1 ms
    expect(params(pods[0].ctx.podHref!).get('at')).toBe('1700000000001');
    expect(pods[2].ctx.clusterId).toBe('c-b');
  });
});
