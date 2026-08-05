import { describe, it, expect } from 'vitest';
import { contextStarter } from './chatContext';

// v0.9.653 — operatör: "Ekranda bir trace açıksa CoSRE 'bu trace'i
// açıklamamı ister misin' diye sorsun, chati açınca."
//
// Boş sohbetin üç sabit çipi (v0.9.652) EKRANDAN habersizdi: operatör
// bir trace'e bakarken sohbeti açtığında ona takımının servisleri
// soruluyordu ve elindeki bağlam kayboluyordu.

const ID = 'f2078ea2d927e028'.repeat(2); // 32 hex

describe('contextStarter', () => {
  it('trace sayfasında öneri üretir', () => {
    const got = contextStarter('/trace', `?id=${ID}`);
    expect(got).not.toBeNull();
    expect(got!.chip).toContain('trace');
    // Soru trace ID'yi TAŞIMALI — yoksa backend hangi trace olduğunu
    // bilemez ve "bu trace" boş bir işaret olur.
    expect(got!.question).toContain(ID);
  });

  it('başka sayfalarda öneri YOK', () => {
    for (const p of ['/traces', '/services', '/', '/trace/compare', '/inbox']) {
      expect(contextStarter(p, `?id=${ID}`), p).toBeNull();
    }
  });

  // Yarım/bozuk id ile soru sormak backend'in "bulunamadı" demesiyle
  // biter — öneri yokluğundan kötü.
  it('geçersiz trace id’de öneri YOK', () => {
    for (const q of ['', '?id=', '?id=abc', `?id=${ID}zz`, '?id=' + 'g'.repeat(32)]) {
      expect(contextStarter('/trace', q), q).toBeNull();
    }
  });

  it('büyük harfli id kabul edilir', () => {
    expect(contextStarter('/trace', `?id=${ID.toUpperCase()}`)).not.toBeNull();
  });

  it('yabancı parametreler öneriyi bozmaz', () => {
    expect(contextStarter('/trace', `?tab=logs&id=${ID}&x=1`)).not.toBeNull();
  });
});

// Sohbeti AÇMAK bir LLM çağrısı tetiklememeli: çözümleyici yalnız bir
// TEKLİF üretiyor, kendisi hiçbir şey çağırmıyor. Saf olduğunun kanıtı —
// aynı girdi her zaman aynı çıktı, yan etki yok.
describe('saflık', () => {
  it('aynı girdide aynı çıktı', () => {
    const a = contextStarter('/trace', `?id=${ID}`);
    const b = contextStarter('/trace', `?id=${ID}`);
    expect(a).toEqual(b);
  });
});
