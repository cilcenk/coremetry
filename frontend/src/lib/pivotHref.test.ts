import { describe, it, expect } from 'vitest';
import { tracesPivotHref, messagingTracesHref } from './pivotHref';
import { decodeRange } from './urlState';

// v0.9.213 — the cross-signal pivot into /traces dropped its time window in
// four separate places. /traces then answered over the operator's sticky
// range and rendered an empty list, which reads as "no such traces" instead
// of "wrong window". These tests pin the one property that made the bug
// possible: the window is never optional and always survives the round trip.
const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

describe('tracesPivotHref', () => {
  it('always emits a range — preset window', () => {
    const p = params(tracesPivotHref({ window: { preset: '6h' }, service: 'checkout' }));
    expect(p.get('range')).toBe('6h');
    expect(p.get('service')).toBe('checkout');
  });

  it('always emits a range — absolute ns window', () => {
    const href = tracesPivotHref({
      window: { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_900_000_000_000 },
      service: 'checkout',
    });
    const range = params(href).get('range');
    expect(range).toBe('custom:1700000000000-1700000900000');
    expect(decodeRange(range, { preset: '30m' })).toEqual({
      preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_900_000,
    });
  });

  it('never narrows the requested window when ns→ms rounds', () => {
    // from floors, to ceils: the encoded window must CONTAIN the request.
    const fromNs = 1_700_000_000_000_999_999;
    const toNs = 1_700_000_900_000_000_001;
    const r = decodeRange(params(tracesPivotHref({ window: { fromNs, toNs } })).get('range'), { preset: '30m' });
    expect(r.preset).toBe('custom');
    expect(r.fromMs! * 1e6).toBeLessThanOrEqual(fromNs);
    expect(r.toMs! * 1e6).toBeGreaterThanOrEqual(toNs);
  });

  it('defaults rootOnly to false — error spans and hops are mid-trace', () => {
    expect(params(tracesPivotHref({ window: { preset: '1h' } })).get('rootOnly')).toBe('false');
    expect(params(tracesPivotHref({ window: { preset: '1h' }, rootOnly: true })).get('rootOnly')).toBe('true');
  });

  it('multi-service co-occurrence wins over single service', () => {
    const p = params(tracesPivotHref({
      window: { preset: '1h' }, service: 'ignored', services: ['gateway', 'checkout'],
    }));
    expect(p.get('services')).toBe('gateway,checkout');
    expect(p.get('service')).toBeNull();
  });

  it('carries hasError, search, filters and view when asked', () => {
    const p = params(tracesPivotHref({
      window: { preset: '1h' }, service: 'db-svc',
      hasError: true, search: '/api/cart', filters: '[{"k":"db.statement","op":"LIKE","v":["SELECT"]}]',
      view: 'list',
    }));
    expect(p.get('hasError')).toBe('true');
    expect(p.get('search')).toBe('/api/cart');
    expect(p.get('filters')).toBe('[{"k":"db.statement","op":"LIKE","v":["SELECT"]}]');
    expect(p.get('view')).toBe('list');
  });

  it('omits absent optionals rather than emitting empty params', () => {
    const p = params(tracesPivotHref({ window: { preset: '1h' }, service: 'checkout' }));
    expect(p.get('hasError')).toBeNull();
    expect(p.get('search')).toBeNull();
    expect(p.get('filters')).toBeNull();
    expect(p.get('view')).toBeNull();
  });

  it('URL-encodes service names and search text', () => {
    const href = tracesPivotHref({
      window: { preset: '1h' }, service: 'checkout/v2 svc', search: 'GET /a b?c=1',
    });
    // Round-trips through URLSearchParams — no raw spaces or & in the href.
    expect(href).not.toMatch(/service=checkout\/v2 svc/);
    const p = params(href);
    expect(p.get('service')).toBe('checkout/v2 svc');
    expect(p.get('search')).toBe('GET /a b?c=1');
  });
});

// v0.9.256 — operatör: "messaging kısmında tracelere erişemiyorum."
//
// Link ölüydü. Yalnız `messaging.destination.name` filtreliyordu, ama
// messaging MV'si destination'ı ÜÇ kademeli coalesce ile üretiyor
// (messaging.destination.name → messaging.destination → peer_service).
// Canlı prod verisinde son 1 saatte `.name` SIFIR satır, eski
// `messaging.destination` 1280 satır; 17 topic'in hepsinde has_name_attr=0.
// Yani link yapısal olarak hiçbir zaman sonuç dönemezdi.
//
// Sözleşme: MV bir alanı N ad altında arıyorsa, o satırdan çıkan pivot da
// N adın hepsini sormak zorunda. Tek ada daralmak, var olamayacak satırlara
// işaret eden bir link üretir.
describe('messagingTracesHref', () => {
  const w = { preset: '1h' } as never;

  function decodeGroup(href: string) {
    const g = new URLSearchParams(href.split('?')[1]).get('filterGroup');
    return g ? JSON.parse(decodeURIComponent(g)) : null;
  }

  it('destination için MV coalesce zincirinin ÜÇ adını da OR ile sorar', () => {
    const g = decodeGroup(messagingTracesHref({ window: w, system: 'kafka', destination: 'transfer.posted' }));
    const or = (g.groups ?? []).find((x: { join: string }) => x.join === 'OR');
    expect(or).toBeTruthy();
    expect(or.filters.map((f: { k: string }) => f.k)).toEqual([
      'messaging.destination.name',
      'messaging.destination',
      'peer.service',
    ]);
    for (const f of or.filters) expect(f.v).toEqual(['transfer.posted']);
  });

  it('pencereyi HER ZAMAN taşır — ikinci bağımsız boş-sonuç kaynağı buydu', () => {
    const href = messagingTracesHref({ window: w, system: 'kafka', destination: 't' });
    expect(new URLSearchParams(href.split('?')[1]).get('range')).toBeTruthy();
  });

  it('rootOnly=false — mesajlaşma span’i ÇOCUK span’dir', () => {
    const href = messagingTracesHref({ window: w, system: 'kafka', destination: 't' });
    expect(new URLSearchParams(href.split('?')[1]).get('rootOnly')).toBe('false');
  });

  // encodeFilterGroup düz-AND grubu için '' döner (urlState back-compat).
  // Bu dal kodlanmasaydı `messaging.system` de düşerdi ve link FİLTRESİZ bir
  // trace listesine giderdi — ölü linkten beter, YANLIŞ link.
  it("destination 'unknown' ise OR grubu basılmaz ama messaging.system KAYBOLMAZ", () => {
    const q = new URLSearchParams(
      messagingTracesHref({ window: w, system: 'kafka', destination: 'unknown' }).split('?')[1]);
    expect(q.get('filterGroup')).toBeNull();
    const flat = JSON.parse(decodeURIComponent(q.get('filters') ?? '[]'));
    expect(flat.map((f: { k: string }) => f.k)).toContain('messaging.system');
  });

  it('rol ve operasyon verilince AND dalına girer', () => {
    const g = decodeGroup(messagingTracesHref({
      window: w, system: 'kafka', destination: 't', role: 'consumer', operation: 't process',
    }));
    const ks = g.filters.map((f: { k: string }) => f.k);
    expect(ks).toContain('kind');
    expect(ks).toContain('name');
  });

  it('filterGroup varken filters ASLA basılmaz (/traces sözleşmesi)', () => {
    const q = new URLSearchParams(messagingTracesHref({ window: w, system: 'kafka', destination: 't' }).split('?')[1]);
    expect(q.get('filterGroup')).toBeTruthy();
    expect(q.get('filters')).toBeNull();
  });

  // v0.9.257 — drawer'ın "Failed traces" eylemi. hasError'ı eklemek
  // destination OR grubunu ya da pencereyi DÜŞÜRMEMELİ: üçü birden
  // gitmezse link ya konu-dışı hatalara ya da boş listeye açılır, ki
  // v0.9.256'nın kapattığı sınıfın aynısıdır.
  it('hasError, destination OR grubunu ve pencereyi KORUYARAK eklenir', () => {
    const href = messagingTracesHref({
      window: w, system: 'kafka', destination: 'transfer.posted', hasError: true,
    });
    const q = new URLSearchParams(href.split('?')[1]);
    expect(q.get('hasError')).toBe('true');
    expect(q.get('range')).toBeTruthy();
    expect(q.get('rootOnly')).toBe('false');
    const or = (decodeGroup(href).groups ?? []).find((x: { join: string }) => x.join === 'OR');
    expect(or.filters.map((f: { k: string }) => f.k)).toEqual([
      'messaging.destination.name',
      'messaging.destination',
      'peer.service',
    ]);
  });
});
