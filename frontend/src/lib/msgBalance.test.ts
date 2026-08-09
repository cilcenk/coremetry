import { describe, it, expect } from 'vitest';
import { msgP99Delta } from './msgBalance';

// v0.9.815 — "kötüleşenler önce" sıralamasının saf çekirdeği.
// v0.9.835 — Denge kolonu kaldırıldı, msgBalance testleri onunla gitti;
// bu dosyada yalnız gecikme tabanlı sıralama kaldı.
//
// Sabitlenen sınır durumu: TABAN YOKLUĞU sessizce 0 olmamalı. prior
// yok / 0 / negatifken null dönmezse satır "hiç kötüleşmemiş" gibi
// sıralanır — oysa doğrusu "bilinmiyor" ve sortRows onu null'larla
// birlikte listenin altına, kararlı sırayla koyar.

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
