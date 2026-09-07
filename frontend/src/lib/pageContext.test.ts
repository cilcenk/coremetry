import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pageContext, pageIdFor, ROUTE_PAGES, NO_CONTEXT_PAGES } from './pageContext';
import { serviceFromRoute } from './chatContext';
import { encodeFilters, encodeFilterGroup } from './urlState';
import { encodeFiltersParam } from './logFilters';

// v0.10.538 — Faz 3.1: sayfa bağlamı saf serileştirici. İki kapı:
//  (1) ROTA KAPSAMA iki yönlü — App.tsx'teki her rota tabloda, tablodaki her
//      rota App.tsx'te (serviceFromRoute'un iki sürüm ıskaladığı sınıf).
//  (2) Eski çözümleyiciyle PARİTE — pageContext().service, serviceFromRoute ile
//      her rotada aynı; yeni serileştirici sohbete farklı servis veremez.

const appSrc = readFileSync(resolve(__dirname, '..', 'App.tsx'), 'utf8');
const appRoutes = [...new Set([...appSrc.matchAll(/path="([^"]+)"/g)].map(m => m[1]))].filter(p => p !== '*');

describe('rota kapsama', () => {
  it('App.tsx rotalarının hepsi tabloda', () => {
    const missing = appRoutes.filter(p => !(p in ROUTE_PAGES));
    expect(missing).toEqual([]);
  });
  it('tablodaki rotalar App.tsx\'te (bayat giriş yok)', () => {
    const stale = Object.keys(ROUTE_PAGES).filter(p => !appRoutes.includes(p));
    expect(stale).toEqual([]);
  });
  it('param\'lı rotalar prefix ile çözülür; bilinmeyen path unknown', () => {
    expect(pageIdFor('/settings/ai')).toBe('settings');
    expect(pageIdFor('/admin/clickhouse')).toBe('admin');
    expect(pageIdFor('/system/stats')).toBe('system');
    expect(pageIdFor('/traces/')).toBe('traces');
    expect(pageIdFor('/nope')).toBe('unknown');
  });
});

describe('serviceFromRoute paritesi', () => {
  const cases: Array<[string, string]> = [
    ['/traces', '?service=api&cluster=c1'], ['/logs', '?service=api'], ['/endpoints', '?service=api'],
    ['/endpoint', '?service=api&route=/x'], ['/inbox', '?service=api'], ['/metrics', '?service=api'],
    ['/explore', '?service=api'], ['/clusters', '?service=api'], ['/profiling', '?service=api'],
    ['/service', '?name=api'], ['/service', '?service=api'], ['/service/backtrace', '?name=api'],
    ['/pod', '?service=api&pod=p'], ['/service-map', '?focus=api'], ['/problems', '?service=api'],
    ['/trace', '?id=abc'], ['/settings/ai', '?service=api'],
  ];
  for (const [path, search] of cases) {
    it(`${path}${search}`, () => {
      const legacy = serviceFromRoute(path, search);
      const got = pageContext(path, search).service ?? '';
      // Eski çözümleyici bazı rotaları bilmiyor (''); yeni serileştirici daha
      // geniş olabilir ama ASLA farklı bir servis üretemez.
      if (legacy) expect(got).toBe(legacy);
    });
  }
});

describe('pageContext alanları', () => {
  it('trace: id + span + range', () => {
    expect(pageContext('/trace', '?id=abc123&span=s1&range=30m&env=prod')).toEqual({
      page: 'trace', path: '/trace', env: 'prod', timeRange: { preset: '30m' }, traceId: 'abc123', spanId: 's1',
    });
  });
  it('traces: kapsam + FilterExpr + skaler çipler + arama; custom range', () => {
    const filters = encodeFilters([{ k: 'http.route', op: '=', v: ['/pay'] }]);
    const group = encodeFilterGroup({ join: 'AND', filters: [{ k: 'k8s.pod.name', op: 'LIKE', v: ['api-%'] }], groups: [{ join: 'OR', filters: [{ k: 'x', op: 'IN', v: ['1', '2'] }] }] });
    const ctx = pageContext('/traces', `?service=api&cluster=c1&filters=${encodeURIComponent(filters)}&filterGroup=${encodeURIComponent(group)}&hasError=1&rootOnly=true&search=UPDATE&range=custom:1000-2000&traceId=t1`);
    expect(ctx.page).toBe('traces');
    expect(ctx.service).toBe('api'); expect(ctx.cluster).toBe('c1'); expect(ctx.search).toBe('UPDATE'); expect(ctx.traceId).toBe('t1');
    expect(ctx.timeRange).toEqual({ preset: 'custom', fromMs: 1000, toMs: 2000 });
    expect(ctx.activeFilters).toEqual([
      { k: 'http.route', op: '=', v: ['/pay'] },
      { k: 'k8s.pod.name', op: 'LIKE', v: ['api-%'] },
      { k: 'x', op: 'IN', v: ['1', '2'] },
      { k: 'status', op: '=', v: ['error'] },
      { k: 'root', op: '=', v: ['true'] },
    ]);
  });
  it('logs: LogFilter tuple kodeği tek şekle iner; devre dışı süzgeç düşer; severity tabanı', () => {
    const raw = encodeFiltersParam([
      { key: 'service.name', value: 'api', negated: false, disabled: false },
      { key: 'level', value: 'debug', negated: true, disabled: false },
      { key: 'trace_id', value: '', negated: false, disabled: false, exists: true },
      { key: 'host', value: 'h1', negated: false, disabled: true },
    ]);
    const ctx = pageContext('/logs', `?service=api&filters=${encodeURIComponent(raw)}&q=timeout&severity=17&traceId=t9&spanId=s9`);
    expect(ctx.activeFilters).toEqual([
      { k: 'service.name', op: '=', v: ['api'] },
      { k: 'level', op: '!=', v: ['debug'] },
      { k: 'trace_id', op: 'EXISTS', v: [] },
      { k: 'severity', op: '>=', v: ['17'] },
    ]);
    expect(ctx.search).toBe('timeout'); expect(ctx.traceId).toBe('t9'); expect(ctx.spanId).toBe('s9');
  });
  it('service: name + op; service-map: focus', () => {
    expect(pageContext('/service', '?name=api&op=GET%20%2Fx&tab=ops')).toMatchObject({ page: 'service', service: 'api', operation: 'GET /x' });
    expect(pageContext('/service-map', '?focus=api&hops=2')).toMatchObject({ page: 'service-map', service: 'api' });
  });
  it('clusters: ns alias + deployment=workload; pod: pod + deploy; entity: opak kimlik ayrıştırılır', () => {
    expect(pageContext('/clusters', '?cluster=c1&ns=payments&deployment=api&q=x')).toMatchObject({ cluster: 'c1', namespace: 'payments', workload: 'api', search: 'x' });
    expect(pageContext('/pod', '?cluster=c1&namespace=n&pod=api-1&deploy=api&service=svc')).toMatchObject({ cluster: 'c1', namespace: 'n', pod: 'api-1', workload: 'api', service: 'svc' });
    expect(pageContext('/entity', '?id=workload:c1/ns1/api&range=1h')).toMatchObject({ cluster: 'c1', namespace: 'ns1', workload: 'api', timeRange: { preset: '1h' } });
    expect(pageContext('/entity', '?id=pod:c1/ns1/api-7')).toMatchObject({ pod: 'api-7' });
    expect(pageContext('/entity', '?id=garbage')).toEqual({ page: 'entity', path: '/entity' });
  });
  it('problem çekmecesi: problems/inbox/anomalies/exceptions', () => {
    expect(pageContext('/problems', '?problem=p1&service=api&exc=fp9')).toMatchObject({ problemId: 'p1', service: 'api', exceptionId: 'fp9' });
    expect(pageContext('/inbox', '?problem=p2&q=x')).toMatchObject({ problemId: 'p2', search: 'x' });
    expect(pageContext('/traces', '?problem=p3').problemId).toBeUndefined();
  });
  it('dashboard: ?cluster= bir değişken olabilir → kapsam olarak OKUNMAZ', () => {
    expect(pageContext('/dashboard', '?id=d1&cluster=x&service=y&range=6h')).toEqual({ page: 'dashboard', path: '/dashboard', timeRange: { preset: '6h' } });
  });
  it('bağlamsız sayfalar yalnız page + path', () => {
    for (const p of NO_CONTEXT_PAGES) {
      const path = Object.entries(ROUTE_PAGES).find(([, id]) => id === p)![0].replace('/:tab', '/x').replace('/:section', '/x');
      expect(pageContext(path, '?service=api&range=1h&cluster=c')).toEqual({ page: p, path });
    }
  });
  it('boş/ilgisiz param alan üretmez; range yoksa timeRange yok', () => {
    expect(pageContext('/traces', '?service=&foo=bar')).toEqual({ page: 'traces', path: '/traces' });
  });
});
