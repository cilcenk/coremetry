import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.652 — boş sohbetteki başlangıç çipleri (operatör isteği).
//
// v0.9.579'da operatör bunları "çıkar" demişti: SEKİZ sabit soru bir
// MENÜYDÜ, araç değil. Karar değişti ama gerekçe ölmedi — üç çip, ve
// üçü de operatörün adıyla istediği sorular.
//
// Bu testler v0.9.579'un dersini çiviliyor: liste büyürse menü hissi
// geri gelir.

const src = readFileSync(resolve(__dirname, './CopilotChat.tsx'), 'utf8');

function arrayLiteral(name: string): string[] {
  const i = src.indexOf(`const ${name} = [`);
  if (i < 0) return [];
  const body = src.slice(src.indexOf('[', i) + 1, src.indexOf('];', i));
  // SATIR bazlı: her eleman kendi satırında. Karakter bazlı bir regex,
  // "Takımımın exception'ları?" içindeki KESME İŞARETİNİ tırnak sanıp
  // çipi ikiye bölüyordu — testi yazarken bulundu.
  return body.split('\n')
    .map(l => l.trim())
    .filter(l => l && !l.startsWith('//'))
    .map(l => l.replace(/,\s*$/, ''))
    .filter(l => /^['"]/.test(l))
    .map(l => l.slice(1, -1));
}

describe('başlangıç çipleri', () => {
  const starters = arrayLiteral('STARTERS');

  it('çipler okunabiliyor', () => {
    expect(starters.length).toBeGreaterThan(0);
  });

  // v0.9.579'un dersi: sekiz çip menüydü. Üç, araç.
  it('en fazla ÜÇ çip — menü hissi eşiği', () => {
    expect(starters.length).toBeLessThanOrEqual(3);
  });

  // Operatörün adıyla istediği üç soru.
  it('operatörün istediği konuları kapsıyor', () => {
    const joined = starters.join(' | ').toLowerCase();
    expect(joined).toContain('takımım');
    expect(joined).toContain('exception');
    expect(joined).toContain('yavaş');
  });

  // Operatör: "son deploy etkisine gerek yok".
  it('deploy sorusu YOK', () => {
    expect(starters.join(' ').toLowerCase()).not.toContain('deploy');
  });
});

describe('follow-up listesi', () => {
  const followups = arrayLiteral('FOLLOWUPS');

  it('statik follow-up listesinden de deploy çıktı', () => {
    expect(followups.join(' ').toLowerCase()).not.toContain('deploy');
  });

  it('follow-up listesi boşalmadı', () => {
    expect(followups.length).toBeGreaterThan(0);
  });
});
