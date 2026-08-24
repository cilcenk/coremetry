import { describe, it, expect } from 'vitest';
import type { TimeRange } from '@/lib/types';
import { tracesLink, exploreLink } from '@/pages/endpoints/links';

// v0.9.307 (brief N6b) — a pivot must carry the SCOPE it was launched
// from.
//
// v0.9.306 fixed this inside the /endpoints drawer: with env=uat the
// table showed uat numbers while the drawer aggregated every env. The
// links out of the page had the same defect one step further along — a
// row read under env=uat opened an UNFILTERED trace list, so the pivot
// silently widened the question it came from. Same class, different
// exit.
//
// These exist because the failure is invisible: the link works, the page
// loads, the numbers are simply about a different population than the
// row that was clicked.
//
// ⚠ v0.9.1372 — BU DOSYA 500+ SÜRÜM BOYUNCA HİÇBİR ŞEYİ KORUMADI.
// v0.9.307'de yazıldığında iki kurucu `Endpoints.tsx`in İÇİNDEydi, yani
// import edilemiyordu; test onları KOPYALADI ("these mirror the two
// builders in Endpoints.tsx"). v0.9.839 kurucuları `endpoints/links.ts`e
// çıkardı ama test kopyayı sınamaya devam etti. Sonuç: testler yeşil,
// gerçek modül ise sınanmıyor — bugün `search=` → `http.route=`
// değişikliğini yaptığımda bu dosya kılını kıpırdatmadı. Ayna testi,
// aynaya bakmayı bırakan koda karşı sessizdir. Artık GERÇEK modülü
// import ediyor.

const range: TimeRange = { preset: '1h' } as TimeRange;
const T = (service: string, path: string, env?: string, cluster?: string) =>
  tracesLink({ service, path }, range, env, cluster);
const E = (service: string, path: string, agg: string, env?: string, cluster?: string) =>
  exploreLink({ service, path }, range, agg, env, cluster);

describe('endpoints → traces pivot', () => {
  it('carries the env the row was read under', () => {
    expect(T('checkout', '/api/orders', 'uat')).toContain('env=uat');
  });

  it('carries the cluster', () => {
    expect(T('checkout', '/api/orders', '', 'eu-west')).toContain('cluster=eu-west');
  });

  it('omits an unset scope rather than sending an empty filter', () => {
    // `env=` explicitly means "all envs" to useUrlEnv, which is NOT the
    // same as "inherit" — writing it blank would pin the target page to
    // all-envs even when the operator had picked one upstream.
    const link = T('checkout', '/api/orders');
    expect(link).not.toContain('env=');
    expect(link).not.toContain('cluster=');
  });

  it('encodes a route containing slashes and braces', () => {
    const p = new URL(T('checkout', '/api/users/{id}/orders', 'uat'), 'http://x').searchParams;
    expect(p.get('filters')).toContain('/api/users/{id}/orders');
  });

  // v0.9.1372 — operatör isteği: KESİN rota eşleşmesi, serbest metin değil.
  it('http.route = <path> yapısal filtresi gönderir, search= DEĞİL', () => {
    const p = new URL(T('checkout', '/api/orders'), 'http://x').searchParams;
    expect(p.get('search')).toBeNull();
    const filters = p.get('filters') ?? '';
    expect(filters).toContain('http.route');
    expect(filters).toContain('/api/orders');
  });

  it('bir öneki olan rotayı GETİRMEZ — serbest metnin sızdırdığı yer', () => {
    // Ayırt edici vaka. `search=/api/pay` hem `/api/pay`i hem
    // `/api/payment-retry`yi tutuyordu: pivot, başlatıldığı satırdan
    // BAŞKA bir soru soruyordu ve bunu sessizce yapıyordu. Yapısal
    // filtre `op: '='` taşır; testin gördüğü şey işte o.
    const filters = new URL(T('checkout', '/api/pay'), 'http://x').searchParams.get('filters') ?? '';
    const decoded = JSON.parse(decodeURIComponent(filters)) as Array<{ k: string; op: string; v: string[] }>;
    expect(decoded).toHaveLength(1);
    expect(decoded[0].op).toBe('=');
    expect(decoded[0].v).toEqual(['/api/pay']);
  });

  it('rootOnly=auto gönderir — root seçili açılır, boşsa sessizce düşer', () => {
    const p = new URL(T('checkout', '/api/orders'), 'http://x').searchParams;
    expect(p.get('rootOnly')).toBe('auto');
    // 'true' OLMAMALI: o, operatörün kendi seçimini ifade eder ve
    // /traces onu sessizce düşürmez (rootOnlyFallback sözleşmesi).
    expect(p.get('rootOnly')).not.toBe('true');
  });

  it('servis filtresini ve liste görünümünü korur', () => {
    const p = new URL(T('checkout', '/api/orders'), 'http://x').searchParams;
    expect(p.get('service')).toBe('checkout');
    expect(p.get('view')).toBe('list');
  });
});

describe('endpoints → explore pivot', () => {
  it('filters on service.name AND http.route, not just the service', () => {
    // Dropping the route would open the WHOLE service's latency — a
    // plausible chart about the wrong thing.
    const filters = decodeURIComponent(new URL(E('checkout', '/api/orders', 'p99'), 'http://x').searchParams.get('filters') ?? '');
    expect(filters).toContain('service.name');
    expect(filters).toContain('http.route');
    expect(filters).toContain('/api/orders');
  });

  it('carries the scope', () => {
    expect(E('checkout', '/api/orders', 'p99', 'uat')).toContain('env=uat');
  });

  it('drops empty scope params so the URL stays clean', () => {
    const link = E('checkout', '/api/orders', 'p99');
    expect(link).not.toContain('env=');
    expect(link).not.toContain('cluster=');
  });

  it('requests the metric result mode Explore decodes', () => {
    const p = new URL(E('checkout', '/api/orders', 'p99'), 'http://x').searchParams;
    expect(p.get('result')).toBe('metric');
    expect(p.get('field')).toBe('duration_ms');
    expect(p.get('agg')).toBe('p99');
  });
});

describe('iki pivot aynı satırdan aynı evrene gider', () => {
  it('ikisi de http.route üzerinden aynı rotayı taşır', () => {
    // v0.9.1372 öncesi ıraksama: explore yapısal `http.route` filtresi
    // kurarken traces serbest metin arıyordu. Aynı satırdan çıkan iki
    // buton farklı popülasyon gösteriyordu ve hiçbiri hata vermiyordu.
    const path = '/api/users/{id}';
    const t = new URL(T('checkout', path), 'http://x').searchParams.get('filters') ?? '';
    const e = new URL(E('checkout', path, 'p99'), 'http://x').searchParams.get('filters') ?? '';
    for (const f of [t, e]) {
      expect(decodeURIComponent(f)).toContain('http.route');
      expect(decodeURIComponent(f)).toContain(path);
    }
  });
});
