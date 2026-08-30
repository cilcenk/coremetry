// v0.10.137 — adım 3 sözleşmeleri: k8s bağlamsız span → link yok + açık ilan;
// eşlenmemiş cluster → link yok + değer; eşlenmiş → at = span zamanı (ms),
// cluster id linkte; aynı pod adı iki cluster'da iki ayrı link
// ayrık pod sayımı + hata sayısı + servisler.
import { describe, it, expect } from 'vitest';
import { spanK8sContext, spanK8sNote } from './spanK8s';
import type { EntityClusterInfo } from './types';

const clusters: EntityClusterInfo[] = [
  { id: 'c-a', name: 'prod-eu', spanClusterValue: 'prod-eu-west', spanClusterValues: ['prod-eu-west', 'eu-legacy'] },
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
  it('a secondary span cluster value (v0.10.139 multi-value record) maps to the same cluster', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'k8s.cluster.name': 'eu-legacy' }), clusters);
    expect(ctx.reason).toBe('ok');
    expect(ctx.clusterId).toBe('c-a');
  });
  it('matches ONLY spanClusterValue — a Remote Cluster name is not a span value (inceleme: yanlış cluster bağlama)', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.cluster.name': 'prod-us' }), clusters);
    expect(ctx.reason).toBe('unmapped-cluster');
    expect(ctx.podHref).toBeUndefined();
  });
});

// v0.10.148 — operator-reported (prod, Tempo fallback trace): pod çipleri
// "?/bsa-…" çıkıyordu. (1) '?' yok: etiket bilinen parçalardan, eksik olan
// tooltip'te açıkça; (2) namespace'siz pod ENTITY linki almaz (eskiden
// `pod:<cid>//<pod>` bozuk link); v0.10.190'dan beri ham /pod linki alır.
import { podChipLabel, podChipWhere } from './spanK8s';
import { readFileSync } from 'node:fs';
import { resolve as resolvePath } from 'node:path';

describe('podChipLabel / podChipWhere (v0.10.148)', () => {
  it.each([
    [{ clusterName: 'prod-eu', namespace: 'pay', pod: 'api-1' }, true, 'prod-eu › pay/api-1'],
    [{ clusterName: 'prod-eu', namespace: 'pay', pod: 'api-1' }, false, 'pay/api-1'],
    [{ clusterValue: 'lab', namespace: 'pay', pod: 'api-1' }, true, 'lab › pay/api-1'],
    [{ pod: 'api-1' }, true, 'api-1'],
    [{ pod: 'api-1' }, false, 'api-1'],
    [{ clusterName: 'prod-eu', pod: 'api-1' }, false, 'api-1'],
  ] as const)('label %j multi=%s → %s (never "?")', (ctx, multi, want) => {
    const got = podChipLabel(ctx, multi);
    expect(got).toBe(want);
    expect(got).not.toContain('?');
  });

  it('where names each missing part explicitly', () => {
    expect(podChipWhere({ pod: 'api-1', reason: 'no-cluster' })).toBe('cluster: bilinmiyor (span cluster değeri taşımıyor) · namespace: yok (k8s.namespace.name taşımıyor) · pod: api-1');
    expect(podChipWhere({ clusterValue: 'lab', namespace: 'pay', pod: 'api-1', reason: 'unmapped-cluster' })).toContain('lab (Remote Cluster kaydına eşlenmemiş)');
    expect(podChipWhere({ clusterName: 'prod-eu', namespace: 'pay', pod: 'api-1', reason: 'ok' })).toBe('cluster: prod-eu · namespace: pay · pod: api-1');
  });

  it('mapped cluster but no namespace → no-namespace: pod link is the RAW /pod page (namespace resolved there via Thanos, v0.10.190), no entity id, cluster/node links stay', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.cluster.name': 'prod-eu-west', 'k8s.node.name': 'w1' }), clusters);
    expect(ctx.reason).toBe('no-namespace');
    expect(ctx.podHref).toBeDefined();
    expect(ctx.podHref!.startsWith('/pod?')).toBe(true);
    const pp = params(ctx.podHref!);
    expect(pp.get('pod')).toBe('api-1');
    expect(pp.get('cluster')).toBe('prod-eu'); // Remote Cluster ADI (sayfa Thanos'a bununla gider)
    expect(pp.get('namespace')).toBeNull();     // namespace bilinmiyor → param YAZILMAZ, sayfa çözer
    expect(pp.get('at')).toBeTruthy();
    expect(ctx.podHref).not.toContain('id=');   // entity id üretilmez (pod:cid//pod bozuk olurdu)
    // İnceleme (190): range NESNE gelince pencere linke YAZILIR ('ok' dalıyla aynı)
    const withRange = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.cluster.name': 'prod-eu-west' }), clusters, { preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_003_600_000 });
    expect(params(withRange.podHref!).get('range')).toBeTruthy();
    expect(ctx.namespaceHref).toBeUndefined();
    expect(ctx.clusterHref).toContain('cluster=prod-eu');
    expect(ctx.nodeHref).toBeDefined();
    expect(ctx.nodeHref).toContain('w1');
    expect(spanK8sNote(ctx)).toMatch(/k8s\.namespace\.name/);
    expect(spanK8sNote(ctx)).toMatch(/Thanos/);
  });

});

// v0.10.150 — resource attribute satırları entity odağına (operator-reported:
// "trace attribute'larından tıklanabilsin"); şeritte çip başına "link yok"
// yok, toplu not var.
import { k8sAttrHref } from './spanK8s';

describe('k8sAttrHref (v0.10.150)', () => {
  const ok = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.namespace.name': 'pay', 'k8s.node.name': 'w1', 'k8s.cluster.name': 'prod-eu-west' }), clusters);
  it.each([
    ['k8s.pod.name', 'pod=api-1'],
    ['k8s.namespace.name', 'ns%3Ac-a%2Fpay'],
    ['k8s.node.name', 'w1'],
    ['k8s.cluster.name', 'cluster=prod-eu'],
    ['openshift.cluster.name', 'cluster=prod-eu'],
    ['cluster', 'cluster=prod-eu'],
  ])('%s → entity href', (key, needle) => {
    const href = k8sAttrHref(key, ok);
    expect(href).toBeDefined();
    expect(decodeURIComponent(href!)).toContain(decodeURIComponent(needle));
  });
  it('non-k8s keys and null ctx → undefined', () => {
    expect(k8sAttrHref('http.method', ok)).toBeUndefined();
    expect(k8sAttrHref('k8s.pod.name', null)).toBeUndefined();
  });
  it('no-namespace ctx: pod → raw /pod link (v0.10.190), namespace undefined, cluster/node still resolve', () => {
    const ctx = spanK8sContext(span({ 'k8s.pod.name': 'api-1', 'k8s.node.name': 'w1', 'k8s.cluster.name': 'prod-eu-west' }), clusters);
    expect(k8sAttrHref('k8s.pod.name', ctx)).toMatch(/^\/pod\?/);
    expect(k8sAttrHref('k8s.namespace.name', ctx)).toBeUndefined();
    expect(k8sAttrHref('k8s.node.name', ctx)).toBeDefined();
    expect(k8sAttrHref('cluster', ctx)).toBeDefined();
  });
});

// v0.10.163 — şerit kaldırıldı; span detayındaki k8s linkleri pod'a giden TEK yol, kapı burada kalır.
describe('SpanDetail k8s attribute links (v0.10.150, v0.10.163 tek yol)', () => {
  it('SpanDetail rows resolve k8s attribute links through the provider (source scan)', () => {
    const src = readFileSync(resolvePath(__dirname, '../components/SpanDetail.tsx'), 'utf8').replace(/^\s*\/\/.*$/gm, '');
    expect(src).toMatch(/k8sAttrHref\(k, k8s\)/);
    expect(src).toMatch(/<SpanK8sCtx\.Provider value=\{k8sCtx\}>/);
  });
});
