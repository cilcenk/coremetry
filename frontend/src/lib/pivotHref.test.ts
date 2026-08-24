import { describe, it, expect } from 'vitest';
import { tracesPivotHref, messagingTracesHref, dbTracesHref, operationTracesHref,
  statementTracesHref, STATEMENT_LIKE_PREFIX_LEN } from './pivotHref';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { decodeRange } from './urlState';

// v0.9.213 — the cross-signal pivot into /traces dropped its time window in
// four separate places. /traces then answered over the operator's sticky
// range and rendered an empty list, which reads as "no such traces" instead
// of "wrong window". These tests pin the one property that made the bug
// possible: the window is never optional and always survives the round trip.
const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

// ⚠ BİRİM / ULP TUZAĞI — v0.9.1354, önceli v0.9.1331 (slowTracesHref).
// Unix-nanosaniye damgaları ~1,7e18; Number.MAX_SAFE_INTEGER'ı (9,007e15)
// ~190× aşıyor ve o büyüklükte float64'ün adım aralığı 256 ns. Yani `ns + 1`
// LİTERAL OLARAK aynı sayıdır ve ±1 ns'lik bir iddia hiçbir şey kanıtlamaz.
// Bu dosyadaki her sub-milisaniye offset YARIM MS'tir — hem ULP'nin hem de
// (aşağıda anlatılan) bölme hassasiyetinin çok üstünde.
const FROM_NS = 1_700_000_000_000_000_000; // 2023-11-14T22:13:20Z
const TO_NS   = 1_700_000_900_000_000_000; // +15 dk

describe('tracesPivotHref', () => {
  it('always emits a range — preset window', () => {
    const p = params(tracesPivotHref({ window: { preset: '6h' }, service: 'checkout' }));
    expect(p.get('range')).toBe('6h');
    expect(p.get('service')).toBe('checkout');
  });

  it('always emits a range — absolute ns window', () => {
    const href = tracesPivotHref({
      window: { fromNs: FROM_NS, toNs: TO_NS },
      service: 'checkout',
    });
    const range = params(href).get('range');
    expect(range).toBe('custom:1700000000000-1700000900000');
    expect(decodeRange(range, { preset: '30m' })).toEqual({
      preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_900_000,
    });
  });

  // v0.9.1354 — bu test 1354'e kadar HİÇBİR ŞEY kanıtlamıyordu ve iki ucu
  // da AYRI AYRI kördü. Eski hâli `from = …_000_999_999`, `to = …_000_000_001`
  // yazıyordu:
  //
  //   • `to` ucu: +1 ns, 1,7e18'de 256 ns'lik ULP'nin altında kaldığı için
  //     LİTERAL olarak TO_NS'in kendisiydi. Damga zaten tam ms sınırındaydı,
  //     yani ceil ile floor aynı cevabı veriyordu — mutasyon ısırmazdı.
  //   • `from` ucu: +999_999 ns ULP'yi aşıyor ama BÖLMEYİ atlatamıyor.
  //     1_700_000_000_000_999_999 / 1e6 = 1700000000000.999999 ve 1,7e12 ms
  //     büyüklüğünde float64 adımı 0,000244 ms (≈244 ns), dolayısıyla değer
  //     1700000000001.0'a yuvarlanıyor: floor ZATEN bir ms yukarı çıkıyordu,
  //     yani pencere DARALIYORDU. İddia yine de geçiyordu, çünkü
  //     `fromMs * 1e6 <= fromNs` karşılaştırması 1,7e18'de 1 ns'lik farkı
  //     göremez. Test ihlali ölçmüyor, ölçemediğini ölçüyordu.
  //
  // Çare v0.9.1331'dekiyle aynı: yarım ms offset. Ve asıl iddia artık
  // eşitsizlik değil TAM DİZE — tamsayı dizesi karşılaştırması bu
  // büyüklükte körleşemez, eşitsizlik körleşebilir.
  it('never narrows the requested window when ns→ms rounds', () => {
    const fromNs = FROM_NS + 500_000; // .5ms → floor AŞAĞIDA kalmalı
    const toNs = TO_NS + 500_000;     // .5ms → ceil YUKARI taşmalı

    // Meta-kapı: offset float64'te hayatta kalmazsa aşağısı boşa koşar.
    // Testin ölçebildiğini testin kendisi kanıtlasın (v0.9.1354'ün dersi).
    expect(fromNs, 'offset float64\'te kayboldu — deltayı büyüt').not.toBe(FROM_NS);
    expect(toNs, 'offset float64\'te kayboldu — deltayı büyüt').not.toBe(TO_NS);
    expect(Math.floor(toNs / 1e6), 'bölme sonrası floor/ceil ayrışmıyor')
      .not.toBe(Math.ceil(toNs / 1e6));

    const range = params(tracesPivotHref({ window: { fromNs, toNs } })).get('range');
    expect(range).toBe('custom:1700000000000-1700000900001');

    const r = decodeRange(range, { preset: '30m' });
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

  // v0.9.1372 — süre bandı. ServiceLatencyHeatmap'in bant seçimi kapsamı
  // minMs/maxMs ile daraltıyordu ve o site KENDİ linkini kuruyordu
  // (`search: operation`), yani düzeltilmiş kardeşin yanında hatalı bir
  // ikiz olarak yaşıyordu. Parametre buraya alınınca ikiz silindi.
  it('süre bandını taşır', () => {
    const bp = params(operationTracesHref({
      window: { preset: '1h' }, operation: 'GET /cart', minMs: 250, maxMs: 500,
    }));
    expect(bp.get('minMs')).toBe('250');
    expect(bp.get('maxMs')).toBe('500');
    // Bandı taşırken kapsamı GEVŞETMEMELİ: isim filtresi yerinde kalır.
    expect(bp.has('search')).toBe(false);
    expect(JSON.parse(bp.get('filters') ?? '[]'))
      .toEqual([{ k: 'name', op: '=', v: ['GET /cart'] }]);
  });

  it('band verilmediğinde minMs/maxMs YAZILMAZ', () => {
    // Ayırt edici vaka: `0` geçerli bir alt sınır. Kurucu
    // `if (p.minMs !== undefined)` diyor; truthy denetimine kayarsa
    // 0 ms'lik taban sessizce düşer ve liste beklenenden geniş gelir.
    expect(p.has('minMs')).toBe(false);
    expect(params(operationTracesHref({
      window: { preset: '1h' }, operation: 'GET /cart', minMs: 0, maxMs: 40,
    })).get('minMs')).toBe('0');
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// statementTracesHref — v0.9.1324 (§3.1 K2).
//
// Symptom (audit-found): "→ traces" çipi `range=` HİÇ yazmıyordu. Sticky
// pencereye açılıyor, boş liste geliyor ve boş liste "bu ifade hiç
// çalışmamış" diye okunuyordu — oysa doğru cevap "yanlış saate baktın".
// Bu dosyanın var oluş sebebinin (dört kez gemiye giden bug) tekrarıydı ve
// pencere ZATEN aynı bileşendeydi: bir satır aşağıdaki HostLink onu
// v0.9.968'den beri taşıyor.
//
// Düzeltme kapı değil imza: builder bu aileye taşındı ve `window` zorunlu
// alan oldu. Aşağıdakiler imzanın ifade edemediklerini pinler.
// ─────────────────────────────────────────────────────────────────────────────
describe('statementTracesHref', () => {
  const SQL = 'SELECT id, name, created_at FROM orders WHERE tenant = :1 AND status = :2 ORDER BY created_at DESC';
  const p = params(statementTracesHref({ window: { preset: '6h' }, statement: SQL, service: 'oradb-1' }));

  it('pencereyi yazar (K2 bug\'ının kendisi)', () => {
    expect(decodeRange(p.get('range'), { preset: '30m' })).toEqual({ preset: '6h' });
  });

  it('mutlak pencere custom olarak kodlanır', () => {
    const q = params(statementTracesHref({
      window: { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_060_000_000_000 },
      statement: SQL,
    }));
    expect(q.get('range')).toBe('custom:1700000000000-1700000060000');
  });

  it('LIKE öneki ilk 60 karakter — normalize edilmiş metin tam eşleşmez', () => {
    const f = JSON.parse(p.get('filters') ?? '[]');
    expect(f).toEqual([{ k: 'db.statement', op: 'LIKE', v: [SQL.slice(0, 60)] }]);
    expect(f[0].v[0].length).toBe(60);
  });

  // Sabiti KENDİSİYLE karşılaştırmak yetmez — mutasyon turunda 60→30
  // değişikliği yukarıdaki assert'i (STATEMENT_LIKE_PREFIX_LEN okuyordu)
  // hiç kırmadı. Asıl sözleşme KARDEŞ YÜZEYLE aynı önekte olmak: /slow-queries
  // aynı LIKE'ı kendi satırından kuruyor ve ikisi ayrışırsa aynı ifade iki
  // sayfada iki farklı trace kümesi listeler.
  it('önek kardeş yüzeyle (SlowQueries.tsx) aynı', () => {
    const sibling = readFileSync(join(__dirname, '..', 'pages', 'SlowQueries.tsx'), 'utf8');
    expect(sibling, 'SlowQueries.tsx artık slice(0, N) yazmıyor — sözleşmeyi yeniden bul')
      .toContain(`slice(0, ${STATEMENT_LIKE_PREFIX_LEN})`);
  });

  it('60 karakterden kısa ifade kırpılmaz', () => {
    const short = 'SELECT 1';
    const f = JSON.parse(params(statementTracesHref({ window: { preset: '1h' }, statement: short })).get('filters') ?? '[]');
    expect(f[0].v[0]).toBe(short);
  });

  it('rootOnly=false — db.statement ÇOCUK client span\'inde (v0.8.585)', () => {
    expect(p.get('rootOnly')).toBe('false');
    expect(p.get('view')).toBe('list');
  });

  it('service verilirse daraltır, verilmezse filo geneli kalır', () => {
    expect(p.get('service')).toBe('oradb-1');
    expect(params(statementTracesHref({ window: { preset: '1h' }, statement: SQL })).has('service')).toBe(false);
  });

  it('özel karakterli SQL encode edilip aynen geri çözülür', () => {
    const weird = "SELECT * FROM t WHERE s = 'a&b?c=1#d'";
    const back = JSON.parse(params(statementTracesHref({
      window: { preset: '1h' }, statement: weird,
    })).get('filters') ?? '[]');
    expect(back[0].v[0]).toBe(weird);
  });
});

// Kaynak taraması — dependencies yüzeyinde el-yapımı `/traces?` kalmasın.
//
// K2 tam bu boşluktan doğdu: pivotHref ailesi pencereyi tip düzeyinde
// zorunlu kılarken, `/traces?` dizesini elle kuran bir dosya o zorlamanın
// TAMAMEN dışındaydı. İmza yalnız kendi çağıranlarını korur; ailenin
// dışında yazılmış bir dize onu hiç görmez.
//
// KAPSAM BİLİNÇLİ DAR. Depo genelinde `/traces?` yazan sekiz dosya daha
// var — ÖLÇÜLDÜ ve sekizinin de penceresi yerinde (slowTracesHref dahil;
// o `range` anahtarını p.set ile kuruyor). Onları buraya muafiyet olarak
// doldurmak, sekiz gerekçe UYDURMAK olurdu; aile göçü ayrı bir dilim.
// Bu kapı K2'nin yaşadığı yüzeyi kilitler ve orada bir daha doğmasını
// engeller.
describe('kaynak taraması — dependencies yüzeyinde el-yapımı /traces? yok', () => {
  const DIR = join(__dirname, '..', 'features', 'dependencies');
  const files = (dir: string, rel = ''): string[] => {
    const out: string[] = [];
    for (const e of readdirSync(dir)) {
      const full = join(dir, e);
      const r = rel ? `${rel}/${e}` : e;
      if (statSync(full).isDirectory()) { out.push(...files(full, r)); continue; }
      if (!/\.tsx?$/.test(e) || /\.test\.tsx?$/.test(e)) continue;
      out.push(r);
    }
    return out;
  };

  // Yorumda geçen bir örnek CANLI site sayılmasın (backLink.test.ts ile aynı
  // sınıflandırıcı; satır ORTASINDAKİ /* yorum başlatmaz).
  const strip = (text: string): string => {
    let inBlock = false;
    return text.split('\n').map(l => {
      const t = l.trim();
      const opens = !inBlock && (t.startsWith('/*') || t.startsWith('{/*'));
      const commented = inBlock || opens || t.startsWith('//') || t.startsWith('*');
      if (opens) inBlock = true;
      if (inBlock && t.includes('*/')) inBlock = false;
      return commented ? '' : l;
    }).join('\n');
  };

  const HANDROLLED = /[`'"]\/traces\?/;

  it('taranan şekil var (kapı boşa koşmuyor)', () => {
    const all = files(DIR);
    expect(all.length).toBeGreaterThan(3);
    // En az bir dosya AİLE üzerinden /traces'e gidiyor olmalı — hiç trace
    // pivotu kalmadıysa kapı anlamsızlaşmıştır, sessizce yeşil kalmasın.
    expect(all.some(f => readFileSync(join(DIR, f), 'utf8').includes('TracesHref'))).toBe(true);
  });

  it('el-yapımı `/traces?` dizesi yazan dosya yok', () => {
    const hits = files(DIR)
      .filter(f => HANDROLLED.test(strip(readFileSync(join(DIR, f), 'utf8'))))
      .sort();
    expect(hits, [
      'Bu dosyalar `/traces?` query string\'ini elle kuruyor ve pencereyi',
      'düşürebilir — pivotHref ailesinin tip zorlaması onları görmez (§3.1 K2).',
      'tracesPivotHref / statementTracesHref / messagingTracesHref kullan.',
    ].join('\n')).toEqual([]);
  });
});
