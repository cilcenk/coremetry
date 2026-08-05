import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.659 — operatör: "Problems sayfasında P1 P2 exceptions default
// listelensin."
//
// Bu bir KARAR TERSİ: v0.9.487'de operatör "defaultta sadece P1'ler
// gözüksün" demişti. Kararın arkasındaki olay kayıtlı — tek servisten 12
// dakikada 11.260 olay üreten bir exception P2 görünüyordu ve varsayılan
// görünümde HİÇ çıkmıyordu (v0.9.627).

const src = readFileSync(resolve(__dirname, './Inbox.tsx'), 'utf8');

// arr — `const NAME[: type] = ['a','b']` dizisini okur.
//
// Tip açıklaması TUZAK: `const KIND_DEFAULT: readonly InboxKind[] = [...]`
// içinde ilk `[` tipe ait. Ve bulunamayınca indexOf -1 döndürüp dosyanın
// İLK dizisini okutuyordu — sessizce makul görünen yanlış bir cevap,
// yani testin en tehlikeli hâli. Artık regex `= [` sonrasını alıyor ve
// eşleşme yoksa PATLIYOR.
function arr(name: string): string[] {
  const m = new RegExp(`const ${name}[^=]*=\\s*\\[([^\\]]*)\\]`).exec(src);
  if (!m) throw new Error(`${name} bulunamadı — sabit yeniden adlandırılmış olabilir`);
  return m[1].split(',').map(s => s.trim().replace(/['"]/g, '')).filter(Boolean);
}

describe('test yardımcısı', () => {
  it('eşleşme yoksa PATLIYOR, sessizce yanlış dizi okumuyor', () => {
    expect(() => arr('YOK_BOYLE_BIR_SABIT')).toThrow();
  });
});

describe('Problems varsayılan görünümü', () => {
  it('öncelik varsayılanı P1 + P2', () => {
    expect(arr('PRIO_DEFAULT')).toEqual(['P1', 'P2']);
  });

  // P3 kronik/düşük-şiddet gürültüsü; varsayılan görünümü doldurur ve
  // v0.9.487'nin çözdüğü sorun buydu.
  it('P3 varsayılanda YOK', () => {
    expect(arr('PRIO_DEFAULT')).not.toContain('P3');
  });

  // v0.9.328 (operatör): "Problems ilk açtığında exception görsün."
  it('tür varsayılanı exception', () => {
    expect(arr('KIND_DEFAULT')).toEqual(['exception']);
  });

  // Ekrandaki açıklama sabit "P1" yazıyordu; varsayılan değişince YALAN
  // söylerdi. Artık sabitten türüyor.
  it('ekrandaki açıklama sabitten türüyor, elle yazılmıyor', () => {
    expect(src).toContain("PRIO_DEFAULT.join(' + ')");
    expect(src).not.toContain('Default view: <b>P1</b>');
  });
});
