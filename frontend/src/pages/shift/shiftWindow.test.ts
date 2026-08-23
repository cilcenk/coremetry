import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { SHIFT_WINDOWS, SHIFT_DEFAULT, normalizeShiftWindow, shiftWindowNs } from './shiftWindow';
import { serviceHref, exceptionGroupWindow } from '@/lib/serviceHref';
import { decodeRange } from '@/lib/urlState';
import { PRESET_SECONDS } from '@/lib/utils';

// shiftWindow.test.ts — v0.9.1322 (§3.1 K3).
//
// Symptom (audit-found): /shift'in üç servis linki penceresizdi, yani 24
// saatlik vardiya özetinden tıklanan servis operatörün sticky penceresinde
// açılıyordu.
//
// Bu dosya HER PENCERE BİRİMİNİ ayrı ayrı koşar. Sebebi kurumsal: değer+birim
// alan bir şablon (Nh/Nd, ms/s) eksen dışı dalında sessizce bozulur — v0.6.36
// olayı tam buydu ve CLAUDE.md o günden beri "her birimi test et" diyor.

const NOW_MS = 1_700_000_000_000;
const H_NS = 3600 * 1e9;

describe('shiftWindowNs — her pencere birimi', () => {
  const want: Record<string, number> = { '8h': 8, '12h': 12, '24h': 24 };
  for (const w of SHIFT_WINDOWS) {
    it(`${w} → ${want[w]} saat geriye`, () => {
      const r = shiftWindowNs(w, NOW_MS);
      expect(r.toNs).toBe(NOW_MS * 1e6);
      expect(r.toNs - r.fromNs).toBe(want[w] * H_NS);
    });
  }

  it('tanınmayan/boş değer sayfanın VARSAYILANINA düşer', () => {
    for (const bad of [null, undefined, '', '3h', 'garbage']) {
      const r = shiftWindowNs(bad, NOW_MS);
      expect(r.toNs - r.fromNs, `girdi=${bad}`).toBe(want[SHIFT_DEFAULT] * H_NS);
    }
  });

  // Yukarıdaki assert SHIFT_DEFAULT'u OKUYOR, yani sabiti değiştiren biri
  // testi de beraberinde taşır — mutasyon turunda tam bu ortaya çıktı:
  // varsayılanı '24h' yapmak hiçbir şeyi kırmadı. Varsayılanın KENDİSİ bir
  // ürün kararı ve sessizce genişlemesi tam olarak kaçınılan bug sınıfı
  // (sayfa "Son 12h" der, altındaki link 24 saat açar).
  it('varsayılan sayfanın gösterdiği penceredir ve en GENİŞİ değildir', () => {
    expect(SHIFT_DEFAULT).toBe('12h');
    const widest = Math.max(...SHIFT_WINDOWS.map(w => want[w]));
    expect(want[SHIFT_DEFAULT]).toBeLessThan(widest);
  });

  it('pencere hep ileri akar', () => {
    for (const w of SHIFT_WINDOWS) {
      const r = shiftWindowNs(w, NOW_MS);
      expect(r.toNs).toBeGreaterThan(r.fromNs);
    }
  });
});

// Bu testin varlık sebebi: `range=8h` yazmak CAZİP ama YANLIŞ.
// PRESET_SECONDS'ta 8h yok; preset olarak geçilse timeRangeToNs onu tanımaz
// ve sessizce 86400'e düşerdi — "Son 8h" düğmesinden 24 saatlik bir sayfa.
describe('sayfanın pencereleri global preset DEĞİL', () => {
  it('8h bir preset değil — bu yüzden mutlak pencere üretiyoruz', () => {
    expect(PRESET_SECONDS['8h']).toBeUndefined();
  });

  it('mutlak pencere serviceHref üzerinden custom olarak kodlanır', () => {
    for (const w of SHIFT_WINDOWS) {
      const href = serviceHref('checkout', { range: shiftWindowNs(w, NOW_MS) });
      const raw = new URLSearchParams(href.slice(href.indexOf('?'))).get('range');
      const decoded = decodeRange(raw, { preset: '30m' });
      expect(decoded.preset, `pencere=${w}`).toBe('custom');
      expect((decoded.toMs ?? 0) - (decoded.fromMs ?? 0), `pencere=${w}`)
        .toBe(want8h12h24h(w) * 3600 * 1000);
    }
  });
});

function want8h12h24h(w: string): number {
  return { '8h': 8, '12h': 12, '24h': 24 }[w] ?? 0;
}

describe('normalizeShiftWindow', () => {
  it('bilinen değerleri aynen geçirir', () => {
    for (const w of SHIFT_WINDOWS) expect(normalizeShiftWindow(w)).toBe(w);
  });
  it('bilinmeyeni varsayılana indirger', () => {
    expect(normalizeShiftWindow('99h')).toBe(SHIFT_DEFAULT);
  });
});

// Kaynak taraması — /shift bir OLAY yüzeyi (§4.1 kural 3: olay-bağlamlı
// çağıranlar pencere geçmek ZORUNDA). serviceHref'te `range` genel olarak
// isteğe bağlı — katalog sayfalarının dürüst penceresi sayfanınkidir — ama
// bu sayfada üçünün de bir penceresi var ve üçü de düşürüyordu.
describe('kaynak taraması — /shift servis linkleri penceresiz olamaz', () => {
  const text = readFileSync(join(__dirname, '..', 'Shift.tsx'), 'utf8');

  it('taranan şekil var (kapı boşa koşmuyor)', () => {
    expect((text.match(/serviceHref\(/g) ?? []).length).toBe(3);
  });

  it('her serviceHref çağrısı bir range taşır', () => {
    const windowless: string[] = [];
    const re = /serviceHref\([^\n]*/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(text))) {
      if (!m[0].includes('range:')) {
        windowless.push(`satır ${text.slice(0, m.index).split('\n').length}: ${m[0].slice(0, 70)}`);
      }
    }
    expect(windowless, [
      '/shift bir olay yüzeyi: 24 saatlik bir vardiya özetinden tıklanan',
      'servis, operatörün sticky penceresinde açılamaz (§3.1 K3).',
      'eventLifespanWindow / exceptionGroupWindow / shiftWindowNs kullan.',
    ].join('\n')).toEqual([]);
  });
});

describe('exceptionGroupWindow', () => {
  const NS = 1e6;
  it('firstSeen→lastSeen aralığını pad ile taşır', () => {
    const w = exceptionGroupWindow({ firstSeen: 2_000_000 * NS, lastSeen: 2_600_000 * NS })!;
    expect(w.fromNs).toBeLessThan(2_000_000 * NS);
    expect(w.toNs).toBeGreaterThan(2_600_000 * NS);
  });

  // Bir grup 02:10'da susmuşsa pencere ORADA bitmeli. eventLifespanWindow'un
  // "açıksa şimdiye kadar" kuralı burada YANLIŞ olurdu — link "şimdi"yi açıp
  // "sorun yok" yanılgısı üretirdi (streams.tsx:528 aynı gerekçe).
  it('susmuş bir grup için pencere GEÇMİŞTE kalır', () => {
    const stopped = 1_000_000 * NS;
    const w = exceptionGroupWindow({ firstSeen: stopped - 60_000 * NS, lastSeen: stopped })!;
    expect(w.toNs).toBeLessThan(Date.now() * NS);
  });

  it('bozuk satırda undefined — epoch-0 penceresi çizilmez', () => {
    for (const g of [undefined, null, {}, { firstSeen: 0 }, { firstSeen: -1 }]) {
      expect(exceptionGroupWindow(g as never)).toBeUndefined();
    }
  });
});
