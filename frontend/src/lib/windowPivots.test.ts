import { describe, it, expect } from 'vitest';
import { tracesURL } from '@/components/DBQueriesPanel';
import { patternLogWindow } from '@/features/anomalies/streams';
import { decodeRange } from './urlState';
import type { DBQueryStat } from './types';

// v0.9.862 — UX denetimi Ö1 + Ö2: pencere taşımayan iki pivot.
//
// İkisi de AYNI yanılgıyı üretiyordu: hedef sayfa sticky pencereyle açılıp
// boş dönüyor, operatör "veri silinmiş / bu sorgunun trace'i yok" sonucuna
// varıyordu. İkisinin de düzeltilmiş emsali aynı dosya ailesindeydi
// (AnomalyDetailDrawer v0.9.213 / pivotHref) — bu ikisi geçirilmemişti.
//
// pivotHref.ts kendi yorumunda bu sınıfı "the exact class pivotHref exists to
// prevent" diye belgeliyor ve dört kez gemiye girdiğini yazıyor.

const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

// ── Ö1 — Service "Database queries" paneli → /traces ────────────────────────
describe('DBQueriesPanel tracesURL — Ö1 düşen pencere', () => {
  const row: DBQueryStat = {
    statement: 'SELECT * FROM orders WHERE id = ?',
    sampleStatement: 'SELECT * FROM orders WHERE id = 42',
  } as DBQueryStat;
  const win = { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_060_000_000_000 };

  it('pencereyi taşır — zoom\'lu custom pencere kaybolmaz', () => {
    const p = params(tracesURL('checkout', row, win));
    expect(decodeRange(p.get('range'), { preset: '30m' }))
      .toEqual({ preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_060_000 });
  });

  it('pencere ZORUNLU argüman — çağıran unutamaz', () => {
    // Tip düzeyinde zorunlu; burada pinlenen, imzanın opsiyonele geri
    // dönmediği (dönerse bu satır derlenmez).
    expect(tracesURL.length).toBe(3);
  });

  it('mevcut kapsam davranışı korunur — LIKE deseni ve iki bayrak', () => {
    const p = params(tracesURL('checkout', row, win));
    expect(p.get('view')).toBe('list');       // aggregate her eşleşmeyi tek satıra çökertirdi
    expect(p.get('rootOnly')).toBe('false');  // db span'i asla kök değildir
    const filters = JSON.parse(p.get('filters') ?? '[]');
    expect(filters[0]).toEqual({ k: 'service.name', op: '=', v: ['checkout'] });
    expect(filters[1].k).toBe('db.statement');
    expect(filters[1].op).toBe('LIKE');
    // Normalleştirilmiş `?` → SQL `%`; literal `_`/`%` kaçırılır.
    expect(filters[1].v[0]).toBe('SELECT * FROM orders WHERE id = %');
  });

  it('normalleştirilmiş biçim boşsa örnek ifadeye tam eşleşme', () => {
    const p = params(tracesURL('checkout', { statement: '', sampleStatement: 'SELECT 1' } as DBQueryStat, win));
    const filters = JSON.parse(p.get('filters') ?? '[]');
    expect(filters[1]).toEqual({ k: 'db.statement', op: '=', v: ['SELECT 1'] });
  });
});

// ── Ö2 — log-pattern anomalisinin "logs ↗" linki ────────────────────────────
describe('patternLogWindow — Ö2 düşen spike penceresi', () => {
  const t0 = 1_700_000_000_000_000_000; // lastSeenNs

  it('son görülme etrafında lead-in\'li pencere üretir', () => {
    // Lead-in olmadan grafik/lista karşılaştırılacak taban olmadan açılır;
    // kardeş AnomalyDetailDrawer aynı nedenle lead-in taşıyor.
    const r = decodeRange(patternLogWindow(t0), { preset: '30m' });
    expect(r.preset).toBe('custom');
    expect(r.fromMs).toBe(Math.floor((t0 - 30 * 60 * 1e9) / 1e6));
    expect(r.toMs).toBe(Math.ceil((t0 + 10 * 60 * 1e9) / 1e6));
  });

  it('pencere /logs\'un okuduğu kanalda ve decodeRange ile round-trip eder', () => {
    // /logs pencereyi YALNIZ ?range='ten okur; başka bir ad ölü yük olurdu.
    expect(patternLogWindow(t0).startsWith('custom:')).toBe(true);
    expect(decodeRange(patternLogWindow(t0), { preset: '30m' }).preset).toBe('custom');
  });

  it('damga yok/bozuksa BOŞ döner — sahte pencere yazılmaz', () => {
    // decodeRange'in reddedeceği bir token adres çubuğunda kendinden emin
    // görünür ama sayfa sticky'yi çizer: fark edilmesi en zor yanlış.
    for (const v of [undefined, null, 0, NaN, -1, 1e9 /* epoch'a çok yakın */]) {
      expect(patternLogWindow(v as never), String(v)).toBe('');
    }
  });
});
