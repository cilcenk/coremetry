import { describe, it, expect } from 'vitest';
import { msgBalance, msgP99Delta, MSG_BALANCE_THRESHOLD } from './msgBalance';

// v0.9.815 — Denge kolonu + "kötüleşenler önce" saf çekirdeği.
//
// Üç sınır durumu sessizce yanlış olabilir ve üçü de ekranda MAKUL
// görünür: sıfır üretim (0'a bölme → NaN → çip "dengede" der),
// ölçülemeyen satır (0 döndürmek "mükemmel dengede" yalanı olur), ve
// eşiğin TAM üstündeki değer (kapsayıcı/dışlayıcı karışırsa eşik bir
// tarafta hiç tetiklenmez).

describe('msgBalance', () => {
  it('üretim tüketimi geçiyor → birikiyor', () => {
    // 1000 üretim, 500 tüketim → oran 0.5
    const r = msgBalance(1000, 500);
    expect(r.state).toBe('accumulating');
    expect(r.ratio).toBeCloseTo(0.5);
  });

  it('tüketim üretimi geçiyor → boşalıyor', () => {
    const r = msgBalance(500, 1000);
    expect(r.state).toBe('draining');
    expect(r.ratio).toBeCloseTo(-1);
  });

  it('eşiğin içinde → dengede', () => {
    // %5 fark — eşiğin (±%10) içinde.
    const r = msgBalance(1000, 950);
    expect(r.state).toBe('balanced');
    expect(r.ratio).toBeCloseTo(0.05);
  });

  it('TAM eşik dengede sayılır (dışlayıcı karşılaştırma)', () => {
    // Oran tam +0.10. `>` kullanıldığı için bu HÂLÂ dengede.
    const r = msgBalance(1000, 900);
    expect(r.ratio).toBeCloseTo(MSG_BALANCE_THRESHOLD);
    expect(r.state).toBe('balanced');
  });

  it('eşiğin bir tık üstü birikiyor', () => {
    const r = msgBalance(1000, 899);
    expect(r.state).toBe('accumulating');
  });

  it('negatif eşik simetrik', () => {
    expect(msgBalance(1000, 1100).state).toBe('balanced');   // oran -0.10, tam eşik
    expect(msgBalance(1000, 1101).state).toBe('draining');   // bir tık altı
  });

  it('iki taraf da sıfır → bilinmiyor, ratio NULL', () => {
    const r = msgBalance(0, 0);
    expect(r.state).toBe('unknown');
    // null ŞART: 0 döndürseydi satır "mükemmel dengede" diye
    // sıralanır ve ölçülemeyen bir satır ölçülenlerin arasına girerdi.
    expect(r.ratio).toBeNull();
  });

  it('tanımsız girdiler → bilinmiyor (0/0 = NaN sızmaz)', () => {
    const r = msgBalance(undefined, undefined);
    expect(r.state).toBe('unknown');
    expect(r.ratio).toBeNull();
  });

  it('üretim sıfır, tüketim var → tam boşalma (0a bölme YOK)', () => {
    const r = msgBalance(0, 500);
    expect(r.state).toBe('draining');
    expect(r.ratio).toBe(-1);
    expect(Number.isFinite(r.ratio as number)).toBe(true);
  });

  it('tüketim sıfır, üretim var → tam birikme', () => {
    const r = msgBalance(500, 0);
    expect(r.state).toBe('accumulating');
    expect(r.ratio).toBeCloseTo(1);
  });
});

describe('msgP99Delta', () => {
  it('kötüleşme pozitif oran', () => {
    expect(msgP99Delta(150, 100)).toBeCloseTo(0.5);
  });

  it('iyileşme negatif oran', () => {
    expect(msgP99Delta(50, 100)).toBeCloseTo(-0.5);
  });

  it('prior yok → NULL (satır listenin altına düşer)', () => {
    // sortRows null'ları en alta koyar ve kendi aralarında GELDİĞİ
    // sırayı korur — yani sunucunun spanCount DESC sırası. Brief'teki
    // "prior yoksa spanCount DESC'e düş" davranışı budur.
    expect(msgP99Delta(150, undefined)).toBeNull();
  });

  it('prior sıfır → NULL (0a bölme değil)', () => {
    expect(msgP99Delta(150, 0)).toBeNull();
  });

  it('prior negatif → NULL', () => {
    expect(msgP99Delta(150, -5)).toBeNull();
  });
});
