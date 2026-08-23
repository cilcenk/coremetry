import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { encodeStmtParam, decodeStmtParam, densifyTrend, stmtDetailHref, STMT_DETAIL_PATH } from './stmtParam';

// stmtParam.test.ts — v0.8.378 (Stage-2 slice D2). Pins the `?stmt=` URL
// codec for the /slow-queries statement detail drawer: decimal-string
// hash discipline (uint64 > 2^53 must survive), boundary-safe optional
// system field, and null-on-garbage so a hostile deep-link keeps the
// drawer closed instead of crashing or firing a 400-bound fetch.

describe('stmtParam codec', () => {
  it('round-trips a hash-only ref', () => {
    const ref = { hash: '12345678901234567890', system: '' };
    expect(decodeStmtParam(encodeStmtParam(ref))).toEqual(ref);
  });

  it('round-trips hash + system', () => {
    const ref = { hash: '42', system: 'postgresql' };
    expect(decodeStmtParam(encodeStmtParam(ref))).toEqual(ref);
  });

  it('preserves full uint64 precision as a string (no Number coercion)', () => {
    // 2^64 - 1: past 2^53 a JS number would silently round this.
    const ref = { hash: '18446744073709551615', system: '' };
    expect(decodeStmtParam(encodeStmtParam(ref))!.hash).toBe('18446744073709551615');
  });

  it('a literal | inside system cannot forge a field boundary', () => {
    const ref = { hash: '7', system: 'weird|engine' };
    expect(decodeStmtParam(encodeStmtParam(ref))).toEqual(ref);
  });

  it('rejects malformed input instead of throwing', () => {
    expect(decodeStmtParam(null)).toBeNull();
    expect(decodeStmtParam('')).toBeNull();
    expect(decodeStmtParam('abc')).toBeNull();            // non-digit hash
    expect(decodeStmtParam('12ab')).toBeNull();           // mixed
    expect(decodeStmtParam('-5')).toBeNull();             // sign
    expect(decodeStmtParam('1e10')).toBeNull();           // scientific
    expect(decodeStmtParam('0')).toBeNull();              // "no statement" sentinel
    expect(decodeStmtParam('000')).toBeNull();            // padded sentinel
    expect(decodeStmtParam('42|')).toBeNull();            // empty system field
    expect(decodeStmtParam('|postgresql')).toBeNull();    // missing hash
    expect(decodeStmtParam('42|pg|extra')).toBeNull();    // extra fields
    expect(decodeStmtParam('42|%E0%A4%A')).toBeNull();    // bad escape
    expect(decodeStmtParam('123456789012345678901')).toBeNull(); // 21 digits > uint64 width
  });
});

describe('densifyTrend', () => {
  const P = (tsSec: number, calls: number) => ({
    tsNs: tsSec * 1e9, calls, errors: calls > 1 ? 1 : 0, avgMs: 10, p95Ms: 25,
  });

  it('expands sparse buckets onto the dense grid with zero gaps', () => {
    // Window 11:03:27→12:00 snaps to 11:00; 300s buckets → 12 slots.
    const fromNs = Date.UTC(2026, 6, 8, 11, 3, 27) * 1e6;
    const toNs = Date.UTC(2026, 6, 8, 12, 0, 0) * 1e6;
    const t0 = Date.UTC(2026, 6, 8, 11, 0, 0) / 1000;
    const d = densifyTrend([P(t0, 5), P(t0 + 600, 3)], fromNs, toNs, 300);
    expect(d.calls).toHaveLength(12);
    expect(d.calls[0]).toBe(5);
    expect(d.calls[1]).toBe(0); // gap → zero
    expect(d.calls[2]).toBe(3);
    expect(d.errors[0]).toBe(1);
    expect(d.p95Ms[2]).toBe(25);
  });

  it('drops out-of-window points instead of writing out of bounds', () => {
    const fromNs = 1000 * 300 * 1e9;
    const toNs = fromNs + 600 * 1e9;
    const d = densifyTrend([P(1000 * 300 - 300, 9), P(1000 * 300 + 9000, 9)], fromNs, toNs, 300);
    expect(d.calls.every(v => v === 0)).toBe(true);
  });

  it('degenerate inputs return empty arrays', () => {
    expect(densifyTrend([], 0, 0, 300).calls).toHaveLength(0);
    expect(densifyTrend([P(0, 1)], 5e9, 4e9, 300).calls).toHaveLength(0); // inverted
    expect(densifyTrend([P(0, 1)], 0, 5e9, 0).calls).toHaveLength(0);     // zero width
  });

  it('caps the grid at 400 buckets (the backend LIMIT mirror)', () => {
    const fromNs = 0;
    const toNs = 90 * 24 * 3600 * 1e9; // 90d at 300s would be 25 920
    const d = densifyTrend([P(0, 1)], fromNs, toNs, 300);
    expect(d.calls).toHaveLength(400);
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// stmtDetailHref — v0.9.963 (UX denetimi G1-b).
//
// The service DB panel dead-ended at /traces: the statement detail drawer
// (per-service callers, trend, vs-prior) was reachable only by clicking a row
// on the FLEET catalog, so an operator looking at their own service had to
// re-find their SQL by eye in a cross-service list. This builder is the door.
//
// Two failure shapes it must not produce:
//   • a link for a row with no identity — `?stmt=undefined` opens
//     /slow-queries with the drawer silently shut ("the button is broken");
//   • a link without a window — the drawer's trend and vs-prior deltas then
//     answer a different hour than the panel the operator clicked from
//     (the tracesPivotHref class, v0.9.208/213).
// ─────────────────────────────────────────────────────────────────────────────
describe('stmtDetailHref', () => {
  const q = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

  it('carries the identity and the window', () => {
    const href = stmtDetailHref({ hash: '4242', system: 'postgresql' },
      { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_060_000_000_000 })!;
    // v0.9.1323 — burası `/slow-queries?` yazıyordu, yani TEST BUG'I
    // KORUYORDU: üretici yanlış rotayı basıyor, test de onu doğru diye
    // çiviliyordu. Artık sabit üreticiden geliyor ve aşağıdaki blok o
    // sabiti App.tsx'in rota listesine karşı doğruluyor.
    expect(href.startsWith(STMT_DETAIL_PATH + '?')).toBe(true);
    const p = q(href);
    expect(decodeStmtParam(p.get('stmt'))).toEqual({ hash: '4242', system: 'postgresql' });
    expect(p.get('range')).toBe('custom:1700000000000-1700000060000');
  });

  it('emits a ref the page\'s own decoder accepts — both shapes', () => {
    for (const system of ['', 'oracle', 'weird|engine']) {
      const href = stmtDetailHref({ hash: '9', system }, { preset: '6h' })!;
      expect(decodeStmtParam(q(href).get('stmt')), system).toEqual({ hash: '9', system });
    }
  });

  it('a TimeRange preset rides through unchanged', () => {
    expect(q(stmtDetailHref({ hash: '9' }, { preset: '24h' })!).get('range')).toBe('24h');
  });

  it('full uint64 precision survives the URL', () => {
    const href = stmtDetailHref({ hash: '18446744073709551615' }, { preset: '1h' })!;
    expect(decodeStmtParam(q(href).get('stmt'))!.hash).toBe('18446744073709551615');
  });

  it('NULL when the row has no usable identity — caller hides the affordance', () => {
    // undefined = pre-D1 cache entry (stmtHash is optional on DBQueryStat);
    // '0' = the backend's "no statement" sentinel, never a real class.
    for (const hash of [undefined, '', '   ', '0', '000', 'abc', '1x', '123456789012345678901']) {
      expect(stmtDetailHref({ hash }, { preset: '1h' }), String(hash)).toBeNull();
    }
  });

  it('a window decodeRange would reject is DROPPED, not written', () => {
    // Writing it would show a confident `custom:` in the address bar while
    // the drawer loads the sticky window — the hardest failure to notice.
    for (const w of [{ fromNs: -1e9, toNs: 5e9 }, { fromNs: 5e9, toNs: 5e9 }, { fromNs: 6e9, toNs: 5e9 }]) {
      const p = q(stmtDetailHref({ hash: '9' }, w)!);
      expect(p.has('range'), JSON.stringify(w)).toBe(false);
      expect(p.get('stmt')).toBe('9'); // …but the identity still ships
    }
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Rota doğrulaması — v0.9.1323 (§3.1 K1).
//
// Bug: stmtDetailHref `/slow-queries` basıyordu; gerçek rota
// `/databases/slow-queries`. Kayıtlı olmayan bir yol App.tsx'in catch-all'ına
// (`path="*"` → <Navigate to="/" replace />) düşüyor, yani "Detail →" düğmesi
// operatörü ANA SAYFAYA atıyordu. 404 değil, sessiz yön değişimi.
//
// Asıl ders bug'ın kendisi değil: YANLIŞ YAZIM BU DOSYADA ÇİVİLİYDİ
// (`expect(href.startsWith('/slow-queries?'))`). Yani test bug'ı KORUYORDU —
// üreticiyi düzeltmek testi kırardı ve "test kırmızı yandı" sinyali insanı
// düzeltmeden geri döndürürdü. Bir dizeyi kendisiyle karşılaştıran bir test,
// yalnız "değişmedi" der; "doğru" demez.
//
// Bu yüzden aşağıdaki blok üreticiyi TEK HAKİKAT KAYNAĞINA — App.tsx'in rota
// listesine — karşı doğruluyor. Rota yeniden adlandırılırsa gate kırılır.
// ─────────────────────────────────────────────────────────────────────────────
function appRoutePaths(): string[] {
  const src = readFileSync(join(__dirname, '..', '..', 'App.tsx'), 'utf8');
  const out: string[] = [];
  const re = /<Route\b[^>]*?\bpath="([^"]+)"/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) out.push(m[1]);
  return out;
}

describe('stmtDetailHref rotası App.tsx.te KAYITLI', () => {
  const routes = appRoutePaths();

  it('rota listesi okunabildi (gate boşa koşmuyor)', () => {
    expect(routes.length).toBeGreaterThan(30);
    expect(routes).toContain('*'); // catch-all — yanlış yolun gittiği yer
  });

  it('üreticinin yolu gerçek bir rota', () => {
    expect(routes, [
      `stmtDetailHref "${STMT_DETAIL_PATH}" basıyor ama App.tsx.te böyle bir rota yok.`,
      'Kayıtsız yol catch-all.a düşer ve operatör sessizce ANA SAYFAYA gider.',
    ].join('\n')).toContain(STMT_DETAIL_PATH);
  });

  it('emitilen href de aynı yola çözülür', () => {
    const href = stmtDetailHref({ hash: '7' }, { preset: '1h' })!;
    expect(routes).toContain(href.slice(0, href.indexOf('?')));
  });

  it('eski yanlış yazım artık bir rota DEĞİL (bug geri gelirse yakala)', () => {
    expect(routes).not.toContain('/slow-queries');
    expect(stmtDetailHref({ hash: '7' }, { preset: '1h' })!.startsWith('/slow-queries?')).toBe(false);
  });
});
