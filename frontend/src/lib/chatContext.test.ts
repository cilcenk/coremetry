import { describe, it, expect } from 'vitest';
import { contextStarter, serviceFromRoute } from './chatContext';

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

// v0.9.1226 — servis bağlam devri artık yalnız /service|/pod değil.
describe('serviceFromRoute', () => {
  it('reads ?name= (then ?service=) on /service', () => {
    expect(serviceFromRoute('/service', '?name=bsa-x&service=y')).toBe('bsa-x');
    expect(serviceFromRoute('/service/backtrace', '?service=y')).toBe('y');
  });
  it('reads ?service= on every service-carrying list route', () => {
    for (const p of ['/traces', '/endpoints', '/logs', '/inbox', '/deploys', '/metrics', '/explore', '/clusters', '/profiling']) {
      expect(serviceFromRoute(p, '?service=bsa-pay')).toBe('bsa-pay');
    }
  });
  it('stays blind on non-service routes and without the param', () => {
    expect(serviceFromRoute('/problems', '?service=x')).toBe('');
    expect(serviceFromRoute('/traces', '?range=1h')).toBe('');
  });
});

// v0.9.1260 — triage çekmecelerinden başlangıç çipleri.
describe('contextStarter — problem/exception', () => {
  it('offers the problem chip on triage routes with ?problem=', () => {
    for (const p of ['/inbox', '/problems', '/anomalies', '/exceptions']) {
      const st = contextStarter(p, '?problem=pr-123');
      expect(st?.chip).toBe('Bu problemin kök nedeni?');
      expect(st?.question).toContain('pr-123');
    }
  });
  it('falls back to the exception chip; problem wins when both present', () => {
    expect(contextStarter('/inbox', '?exception=abc123ff')?.chip).toBe("Bu exception'ın kök nedeni?");
    expect(contextStarter('/inbox', '?problem=p1&exception=e1')?.chip).toBe('Bu problemin kök nedeni?');
  });
  it('rejects junk ids and foreign routes', () => {
    expect(contextStarter('/inbox', '?problem=')).toBeNull();
    expect(contextStarter('/inbox', '?problem=' + 'x'.repeat(80))).toBeNull();
    expect(contextStarter('/inbox', '?exception=a b')).toBeNull();
    expect(contextStarter('/services', '?problem=p1')).toBeNull();
  });
});
