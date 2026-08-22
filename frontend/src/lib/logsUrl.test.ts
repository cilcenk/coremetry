import { describe, it, expect } from 'vitest';
import { logsUrlSig, writeLogsParams, readLogsParams, logsRangeParam, buildDocPermalink, parseDocParam, type LogsUrlFilter } from './logsUrl';
import { decodeRange } from './urlState';

// v0.8.546 — /logs `severity` was a live filter that never round-tripped
// through the URL: writeUrl didn't write it, urlSig didn't hash it, and the
// import pinned it to 0. Pressing the ERROR chip and hitting Share handed
// the recipient a link that opened on All levels — a silent WRONG link.
//
// The guard these tests protect: the page no-ops its URL→state import when
// the incoming params hash to the sig it just wrote. That only holds while
// {sig, write, read} agree on one field set; drift means an infinite
// refetch or a clobbered filter (the v0.8.253/256/265 class).

const base: LogsUrlFilter = {
  service: '', cluster: '', search: '', severity: 0,
  traceId: '', spanId: '', hasTrace: false,
};
const rt = (f: LogsUrlFilter) => readLogsParams(writeLogsParams(new URLSearchParams(), f, '', ''));

describe('severity round-trip — the reported bug', () => {
  it('survives write → read', () => {
    expect(rt({ ...base, severity: 17 }).severity).toBe(17);
  });

  it('is actually written to the URL', () => {
    expect(writeLogsParams(new URLSearchParams(), { ...base, severity: 17 }, '', '').get('severity')).toBe('17');
  });

  it('0 (all levels) leaves no param behind', () => {
    expect(writeLogsParams(new URLSearchParams(), base, '', '').has('severity')).toBe(false);
    expect(rt(base).severity).toBe(0);
  });

  it('toggling a chip off clears a severity already in the URL', () => {
    // The page reuses the existing params; a stale severity must not survive
    // the toggle-off that sets it back to 0.
    const prev = new URLSearchParams('severity=17&service=api');
    expect(writeLogsParams(prev, { ...base, service: 'api' }, '', '').has('severity')).toBe(false);
  });

  it('every severity rung the chips can set round-trips', () => {
    // LVL_FACETS floors — a chip that silently resolved to 0 would reopen
    // the All-levels bug for that band only.
    for (const min of [1, 5, 9, 13, 17, 21]) {
      expect(rt({ ...base, severity: min }).severity).toBe(min);
    }
  });

  it('garbage severity reads as 0, never NaN', () => {
    // NaN would poison both the query and the sig (JSON.stringify(NaN) →
    // "null", so two different bad URLs would hash equal).
    for (const raw of ['abc', '', '-3', 'NaN', 'Infinity']) {
      expect(readLogsParams(new URLSearchParams(`severity=${raw}`)).severity).toBe(0);
    }
  });
});

describe('logsUrlSig — the guard', () => {
  it('hashes severity: a severity-only change must move the sig', () => {
    // This is the exact omission that let the bug hide: the old sig ignored
    // severity, so the guard treated an ERROR-chip URL as "no change".
    expect(logsUrlSig({ ...base, severity: 17 }, '', ''))
      .not.toBe(logsUrlSig(base, '', ''));
  });

  it('is stable for the same state', () => {
    expect(logsUrlSig({ ...base, severity: 17 }, 'f', 'c'))
      .toBe(logsUrlSig({ ...base, severity: 17 }, 'f', 'c'));
  });

  it('moves for every URL-bearing field', () => {
    const variants: Array<[string, LogsUrlFilter]> = [
      ['service',  { ...base, service: 'api' }],
      ['cluster',  { ...base, cluster: 'eu' }],
      ['search',   { ...base, search: 'boom' }],
      ['severity', { ...base, severity: 17 }],
      ['traceId',  { ...base, traceId: 'abc' }],
      ['spanId',   { ...base, spanId: 'def' }],
      ['hasTrace', { ...base, hasTrace: true }],
    ];
    const zero = logsUrlSig(base, '', '');
    for (const [name, f] of variants) {
      expect(logsUrlSig(f, '', ''), `${name} must move the sig`).not.toBe(zero);
    }
    expect(logsUrlSig(base, 'filters', ''), 'filters must move the sig').not.toBe(zero);
    expect(logsUrlSig(base, '', 'cols'), 'cols must move the sig').not.toBe(zero);
  });

  it('agrees across write→read: a written state hashes to the same sig it reads back as', () => {
    // The invariant the page's no-op depends on. If write and read disagree
    // on any field, the sig flips right after writeUrl and the import
    // clobbers the state the operator just set.
    const f: LogsUrlFilter = {
      service: 'api', cluster: 'eu', search: 'boom', severity: 17,
      traceId: 'abc', spanId: 'def', hasTrace: true,
    };
    expect(logsUrlSig(rt(f), '', '')).toBe(logsUrlSig(f, '', ''));
  });
});

describe('other params keep their pre-v0.8.546 behaviour', () => {
  it('drops the legacy `search` alias while still reading it', () => {
    expect(readLogsParams(new URLSearchParams('search=old')).search).toBe('old');
    expect(readLogsParams(new URLSearchParams('q=new&search=old')).search).toBe('new');
    expect(writeLogsParams(new URLSearchParams('search=old'), { ...base, search: 'new' }, '', '').has('search')).toBe(false);
  });

  it('hasTrace is 1/absent, not true/false', () => {
    expect(writeLogsParams(new URLSearchParams(), { ...base, hasTrace: true }, '', '').get('hasTrace')).toBe('1');
    expect(writeLogsParams(new URLSearchParams(), base, '', '').has('hasTrace')).toBe(false);
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// v0.9.853 — UX denetimi K3: Trace detayının ana "≡ Logs" butonu pencereyi
// `?from=<ns>&to=<ns>` diye gönderiyordu. readLogsParams (yukarıda) from/to
// BİLMEZ — /logs penceresini yalnız `?range=`ten alır. Parametreler ölü yük
// olarak URL'de duruyor, sayfa sticky pencereyle açılıyordu: sticky
// pencereden eski HER trace'te "log yok" (loglar duruyorken).
//
// Bu blok tek üreticiyi (logsRangeParam) kilitler: NANOSANİYE girer,
// MİLİSANİYE `custom:` token'ı çıkar (v0.6.36 birim-karıştırma sınıfı) ve
// çıktının decodeRange'in kabul testinden geçtiğini doğrular.
// ─────────────────────────────────────────────────────────────────────────────
describe('logsRangeParam — K3 ölü from/to parametresi', () => {
  const MS = 1_000_000; // ns per ms

  it('ns penceresini /logs tarafının okuduğu ms token\'ına çevirir', () => {
    expect(logsRangeParam(1_700_000_000_000 * MS, 1_700_000_060_000 * MS))
      .toBe('custom:1700000000000-1700000060000');
  });

  it('round-trip: ürettiği token decodeRange tarafından AYNI pencereye çözülür', () => {
    const token = logsRangeParam(1_700_000_000_000 * MS, 1_700_000_060_000 * MS);
    expect(decodeRange(token, { preset: '30m' }))
      .toEqual({ preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_060_000 });
  });

  it('token /logs okuyucusunun pencere kanalına düşer (from/to DEĞİL)', () => {
    // Bug'ın özü: ne yazarsak yazalım, `from`/`to` adları okunmuyor.
    const dead = readLogsParams(new URLSearchParams('traceId=abc&from=1&to=2'));
    expect(dead.traceId).toBe('abc');
    expect(Object.keys(dead)).not.toContain('from');
    // Doğru kanal `range` — logsUrl'in filtre şeması değil, useUrlRange okur;
    // burada garanti ettiğimiz, üretilen token'ın o kanalın dilinde olması.
    expect(logsRangeParam(5_000 * MS, 6_000 * MS).startsWith('custom:')).toBe(true);
  });

  describe('padNs — birim disiplini (ns girer, ms çıkar)', () => {
    const cases: Array<{ name: string; padNs: number; fromMs: number; toMs: number }> = [
      { name: 'yastıksız',      padNs: 0,                fromMs: 1_000_000, toMs: 1_000_060 },
      { name: '60 sn (trace)',  padNs: 60_000_000_000,   fromMs:   940_000, toMs: 1_060_060 },
      { name: '15 dk (span)',   padNs: 900_000_000_000,  fromMs:   100_000, toMs: 1_900_060 },
      { name: '1 ms',           padNs: 1_000_000,        fromMs:   999_999, toMs: 1_000_061 },
    ];
    for (const c of cases) {
      it(`${c.name}: pencereyi ms cinsinden simetrik genişletir`, () => {
        expect(logsRangeParam(1_000_000 * MS, 1_000_060 * MS, c.padNs))
          .toBe(`custom:${c.fromMs}-${c.toMs}`);
      });
    }
  });

  it('yuvarlama pencereyi DARALTMAZ (alt sınır floor, üst sınır ceil)', () => {
    // Yarım ms'lik kenarlar: her iki uç da dışarı yuvarlanmalı, yoksa kenardaki
    // log satırı pencerenin dışında kalır.
    const t = logsRangeParam(1_700_000_000_000 * MS + 500_000, 1_700_000_060_000 * MS + 500_000);
    expect(t).toBe('custom:1700000000000-1700000060001');
  });

  it('kullanılamaz pencerede boş döner — çağıran paramı DÜŞÜRSÜN', () => {
    // DrillButton/Link boş değeri atar; bozuk bir `custom:` token'ı yazmak
    // decodeRange'i fallback'e düşürürdü (sessiz yanlış pencere).
    for (const [from, to] of [
      [undefined, undefined], [0, 5_000 * MS], [5_000 * MS, 0],
      [5_000 * MS, 5_000 * MS],            // sıfır genişlik
      [6_000 * MS, 5_000 * MS],            // ters
      [NaN, 5_000 * MS], [5_000 * MS, NaN],
      [-1 * MS, 0],
    ] as Array<[number | undefined, number | undefined]>) {
      expect(logsRangeParam(from, to), `${from}..${to} boş dönmeli`).toBe('');
    }
  });
});

// v0.9.1248 — tek-doküman kalıcı linki saf çekirdeği.
describe('doc permalink', () => {
  it('builds with service + env, omits empties', () => {
    expect(buildDocPermalink({ timestamp: 170e16, id: 42, serviceName: 'bsa-x' }, 'uat'))
      .toBe('/logs?doc=1700000000000000000.42&docsvc=bsa-x&env=uat');
    expect(buildDocPermalink({ timestamp: 5, id: 7 }, ''))
      .toBe('/logs?doc=5.7');
  });
  it('round-trips through parse', () => {
    const p = parseDocParam('1700000000000000000.9007199254740993');
    expect(p?.ts).toBe(1700000000000000000);
    expect(p?.id).toBe(Number('9007199254740993')); // 2^53+1 yuvarlanır — iki taraf AYNI yuvarlamayı görür, eşitlik bu
  });
  it('rejects garbage', () => {
    for (const bad of [null, '', '123', '.5', '5.', 'a.b', '-1.5', '0.9', '1.2.3x']) {
      if (bad === '1.2.3x') continue; // Number('2.3x')=NaN → null zaten
      expect(parseDocParam(bad as string | null)).toBeNull();
    }
    expect(parseDocParam('1.2.3x')).toBeNull();
  });
});
