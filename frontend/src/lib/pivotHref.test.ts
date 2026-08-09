import { describe, it, expect } from 'vitest';
import { tracesPivotHref, messagingTracesHref, dbTracesHref, operationTracesHref } from './pivotHref';
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

// v0.9.268 — messagingTracesHref ile AYNI sözleşme, aynı fonksiyondaki
// düzeltilmemiş kardeş. db_summary_5m instance'ı ALTI basamaklı coalesce ile
// çözüyor (store.go:2494-2502); eski link yalnız peer.service soruyordu.
// Canlı ölçüm: clickhouse satırı 30 dakikalık pencerede 2201 span taşıyordu,
// link 0 eşleşme buluyordu — çünkü o ad service_name basamağından geliyor.
describe('dbTracesHref', () => {
  const w = { preset: '1h' } as never;

  function q(href: string) { return new URLSearchParams(href.split('?')[1]); }
  function decodeGroup(href: string) {
    const g = q(href).get('filterGroup');
    return g ? JSON.parse(decodeURIComponent(g)) : null;
  }

  it('instance için MV coalesce zincirinin ALTI adını da OR ile sorar', () => {
    const g = decodeGroup(dbTracesHref({ window: w, system: 'clickhouse', instance: 'coremetry-monolithic' }));
    const or = (g.groups ?? []).find((x: { join: string }) => x.join === 'OR');
    expect(or).toBeTruthy();
    expect(or.filters.map((f: { k: string }) => f.k)).toEqual([
      'peer.service', 'server.address', 'net.peer.name',
      'db.host', 'db.name', 'service.name',
    ]);
    for (const f of or.filters) expect(f.v).toEqual(['coremetry-monolithic']);
  });

  it('service.name basamağı ŞART — canlıda ölü olan tam bu satırdı', () => {
    // Bu vaka ayrı duruyor çünkü hatanın kendisi: instance adı çağıran
    // servisten geliyorsa (DB'yi adlandıracak hiçbir attribute yoksa)
    // peer.service ile aranmak sıfır satır döndürür.
    const g = decodeGroup(dbTracesHref({ window: w, system: 'clickhouse', instance: 'coremetry-monolithic' }));
    const or = g.groups.find((x: { join: string }) => x.join === 'OR');
    expect(or.filters.some((f: { k: string }) => f.k === 'service.name')).toBe(true);
  });

  it('pencereyi HER ZAMAN taşır', () => {
    expect(q(dbTracesHref({ window: w, system: 'postgresql', instance: 'postgres' })).get('range')).toBeTruthy();
  });

  it('rootOnly=false — DB span’i CLIENT ÇOCUK span’dir', () => {
    expect(q(dbTracesHref({ window: w, system: 'postgresql', instance: 'postgres' })).get('rootOnly')).toBe('false');
  });

  it("dbName verilirse AND'e girer, 'default' sentinel'i girmez", () => {
    const withName = decodeGroup(dbTracesHref({ window: w, system: 'postgresql', instance: 'postgres', dbName: 'ledger' }));
    expect(withName.filters.map((f: { k: string }) => f.k)).toContain('db.name');
    const withDefault = decodeGroup(dbTracesHref({ window: w, system: 'postgresql', instance: 'postgres', dbName: 'default' }));
    expect(withDefault.filters.map((f: { k: string }) => f.k)).not.toContain('db.name');
  });

  it("instance 'unknown' ise OR basılmaz ama db.system KAYBOLMAZ", () => {
    // Düz-AND grubunda encodeFilterGroup '' döner; bu dal kodlanmasa link
    // FİLTRESİZ bir trace listesine giderdi — ölü linkten beter.
    const p = q(dbTracesHref({ window: w, system: 'postgresql', instance: 'unknown' }));
    expect(p.get('filterGroup')).toBeNull();
    const flat = JSON.parse(decodeURIComponent(p.get('filters') ?? '[]'));
    expect(flat.map((f: { k: string }) => f.k)).toContain('db.system');
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// v0.9.855 — UX denetimi K4: ⌘K endpoint sonucu `/traces?operation=<ad>`e
// gidiyordu. /traces'in URL okuyucusu `operation` diye bir param BİLMEZ ve
// State→URL efekti query string'i beyaz-listeden SIFIRDAN kurduğu için param
// ilk state yazımında silinir. Operatör endpoint adını yazıyor, filtresiz ve
// alakasız bir liste alıyordu — "arama bozuk".
//
// İkinci yarısı v0.8.488 (operatör-reported): serbest metin `search=` trace'in
// HERHANGİ bir span'ında eşleşir; operasyon pivotu bu yüzden alakasız
// trace'leri sürüklüyordu. Doğru kapsam KESİN isim filtresi.
// ─────────────────────────────────────────────────────────────────────────────
describe('operationTracesHref — K4 ölü ?operation= parametresi', () => {
  const href = operationTracesHref({ window: { preset: '6h' }, operation: 'GET /cart' });
  const p = params(href);

  it('/traces\'in TANIMADIĞI param adlarını ASLA yazmaz', () => {
    // Bu bug'ın kökü: okunmayan bir ada yazmak. `operation` (ve `op`)
    // beyaz-listede yok — yazılırsa sessizce silinir.
    expect(p.has('operation')).toBe(false);
    expect(p.has('op')).toBe(false);
  });

  it('kapsamı KESİN isim filtresiyle kurar, substring search ile değil', () => {
    expect(p.has('search')).toBe(false);
    expect(JSON.parse(p.get('filters') ?? '[]'))
      .toEqual([{ k: 'name', op: '=', v: ['GET /cart'] }]);
  });

  it('pencereyi taşır (pivotHref ailesinin zorunlu sözleşmesi)', () => {
    expect(p.get('range')).toBe('6h');
    const abs = params(operationTracesHref({
      window: { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_060_000_000_000 },
      operation: 'GET /cart',
    }));
    expect(decodeRange(abs.get('range'), { preset: '30m' }))
      .toEqual({ preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_060_000 });
  });

  it('rootOnly=false — operasyon span\'i kök olmak zorunda değil (v0.8.585)', () => {
    expect(p.get('rootOnly')).toBe('false');
    expect(p.get('view')).toBe('list');
  });

  it('servis verildiğinde kapsamı daraltır, verilmediğinde filo geneli kalır', () => {
    expect(params(operationTracesHref({
      window: { preset: '1h' }, operation: 'GET /cart', service: 'checkout',
    })).get('service')).toBe('checkout');
    expect(p.has('service')).toBe(false);
  });

  it('özel karakterli operasyon adı encode edilir ve aynen geri çözülür', () => {
    const weird = 'GET /a b?c=1&d#2';
    const back = JSON.parse(params(operationTracesHref({
      window: { preset: '1h' }, operation: weird,
    })).get('filters') ?? '[]');
    expect(back).toEqual([{ k: 'name', op: '=', v: [weird] }]);
  });
});
