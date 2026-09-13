import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { defaultRouteRuleName, defaultRouteThreshold } from './routeAlert';
import { HTTP_ROUTE_METRICS, targetMetrics, httpRouteUnit, isHttpRouteMetric } from './constants';

// v0.10.705 — http_route hedefli kural: ad şablonu, varsayılan eşik, metrik
// ailesi, birim; Alerts/Endpoints/EndpointDetail kaynak pinleri.
const root = resolve(__dirname, '..');
const read = (p: string) => readFileSync(resolve(root, p), 'utf8');

describe('RouteAlertModal', () => {
  it('varsayılan ad ve eşik', () => {
    expect(defaultRouteRuleName({ service: 'shop-payment', route: '/api/pay' }, 'http_route_p95_ms')).toBe('Route p95: shop-payment /api/pay');
    expect(defaultRouteRuleName({ service: 'a', route: '/x' }, 'http_route_error_rate')).toBe('Route error rate: a /x');
    expect(defaultRouteRuleName({}, 'http_route_rate')).toBe('Route request rate: route');
    expect(defaultRouteThreshold('http_route_p99_ms')).toBe(1000);
    expect(defaultRouteThreshold('http_route_error_rate')).toBe(5);
    expect(defaultRouteThreshold('http_route_rate')).toBe(1);
  });
  it('metrik ailesi ve birim', () => {
    expect(targetMetrics('http_route')).toBe(HTTP_ROUTE_METRICS);
    expect(HTTP_ROUTE_METRICS[0].v).toBe('http_route_p95_ms');
    expect(httpRouteUnit('http_route_error_rate')).toBe('%');
    expect(httpRouteUnit('http_route_rate')).toBe('/s');
    expect(httpRouteUnit('http_route_p95_ms')).toBe('ms');
    expect(isHttpRouteMetric('http_route_p95_ms')).toBe(true);
    expect(isHttpRouteMetric('p95_ms')).toBe(false);
  });
  it('Alerts: tür etiketi, salt-okunur kapsam, koşul hücresi', () => {
    const alerts = read('Alerts.tsx');
    expect(alerts).toContain("r.target?.kind === 'http_route' ? 'HTTP ROUTE'");
    expect(alerts).toContain("draft.target?.kind === 'http_route'");
    expect(alerts).toContain('isRoute ? `${r.target!.service} ${r.target!.route}`');
  });
  it('Endpoints satırı ve detay: yazma rolüne ⚠ + modal', () => {
    const eps = read('Endpoints.tsx');
    expect(eps).toContain("canEditRules = user?.role === 'admin' || user?.role === 'editor'");
    expect(eps).toContain('<RouteAlertModal open onClose={() => setAlertRow(null)}');
    expect(eps).toContain("entry === 'http'"); // RPC satırında route yok
    const det = read('EndpointDetail.tsx');
    expect(det).toContain('<RouteAlertModal open onClose={() => setAlertOpen(false)}');
    expect(det).toContain('⚠ Alarm oluştur');
  });
});
