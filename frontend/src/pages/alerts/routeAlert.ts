import type { RuleTarget } from '@/lib/types';

// routeAlert.ts — v0.10.705: RouteAlertModal'ın saf yardımcıları (ad şablonu,
// varsayılan eşik). Bileşen dosyasından ayrı: react-refresh yalnız bileşen
// export'u ister; testler de bileşeni yüklemeden bunları pinler.
export function defaultRouteRuleName(t: Pick<RuleTarget, 'service' | 'route'>, metric: string): string {
  const what = metric === 'http_route_error_rate' ? 'Route error rate'
    : metric === 'http_route_rate' ? 'Route request rate'
    : metric === 'http_route_p99_ms' ? 'Route p99' : 'Route p95';
  const scope = [t.service ?? '', t.route ?? ''].filter(Boolean).join(' ');
  return `${what}: ${scope || 'route'}`;
}

export function defaultRouteThreshold(metric: string): number {
  return metric === 'http_route_error_rate' ? 5 : metric === 'http_route_rate' ? 1 : 1000;
}
