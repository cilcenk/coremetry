import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

// v0.9.646 — `.table-wrap.is-fit` yapışkan tablo başlığını açıyor ama
// bunu KAYDIRMA KONTEYNERİNİ KALDIRARAK yapıyor (v0.9.644). Geniş bir
// tabloya yanlışlıkla eklenirse yatay kaydırma #content'e çıkar ve
// yapışkan filtre barı içerikle yana kayar — v0.9.640'ta operatörün
// bildirdiği sızıntının aynısı.
//
// Güvenliğin YAPISAL kanıtı `tableLayout: 'fixed'` + width:100%: sabit
// düzende tablo sığmaya ZORLANIYOR, yani yatay taşma olamaz. Bu test o
// koşulu her is-fit kullanımı için zorunlu kılıyor.

const SRC = resolve(__dirname, '../..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (p.endsWith('.tsx')) out.push(p);
  }
  return out;
}

describe('is-fit güvenlik kuralı', () => {
  const users = walk(SRC).filter(p => {
    const s = readFileSync(p, 'utf8');
    return s.includes('table-wrap is-fit');
  });

  it('en az bir kullanım var (regex/yol bozulmadıysa)', () => {
    expect(users.length).toBeGreaterThan(0);
  });

  // Elle yazılmış <thead>'li iki tablo (v0.9.644 pilotları) fixed layout
  // TAŞIMIYOR: kolon sayıları tek haneli ve içerik kısa olduğu için GÖZLE
  // seçildiler, yapısal kanıtları yok. İstisna olduklarını AÇIKÇA
  // yazıyoruz ki sessizce çoğalmasınlar — yeni kullanımlar için kural
  // fixed layout.
  const HAND_PICKED = ['/pages/Runbooks.tsx', '/pages/settings/ApiTokensTab.tsx'];

  it('her is-fit tablosu tableLayout:fixed taşıyor (istisnalar hariç)', () => {
    const bad = users
      .filter(p => !HAND_PICKED.some(h => p.endsWith(h)))
      .filter(p => !readFileSync(p, 'utf8').includes("tableLayout: 'fixed'"));
    expect(bad.map(p => p.replace(SRC, ''))).toEqual([]);
  });

  it('istisna listesi bayatlamamış', () => {
    for (const rel of HAND_PICKED) {
      const p = users.find(u => u.endsWith(rel));
      expect(p, `${rel} artık is-fit kullanmıyor — istisnadan çıkarılmalı`).toBeTruthy();
    }
  });
});
