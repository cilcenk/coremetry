import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// explainAnswerType.test.ts — v0.10.77.
//
// `links` her ✨ Explain yanıtında var (sunucu tarafında deliverExplain
// hepsine ekliyor) ama TİP SİSTEMİNE hiç girmemişti: CopilotExplain
// alanı DÖRT ayrı `as { links?… }` cast'iyle okuyordu.
//
// ⚠ Cast, tipin YALAN SÖYLEMESİNE izin verir. Sunucu alanı kaldırsa ya
// da adını değiştirse tsc hiçbir şey söylemez ve satır içi linkler
// sessizce kaybolurdu — tam olarak v0.10.35'te düzeltilen kusurun geri
// gelmesi, üstelik fark edilmeden.
//
// CLAUDE.md: "Tip sorununu `as` ile çözme" + "lib/types.ts paylaşılan
// şekillerin TEK kaynağı".

const read = (rel: string) => readFileSync(new URL(rel, import.meta.url), 'utf8');

describe('Explain yanıtı tip sisteminde', () => {
  it('links için cast KALMADI', () => {
    const src = read('../components/CopilotExplain.tsx');
    expect(src).not.toContain('as { links?');
  });

  it('ortak taban tip lib/types.ts\'te', () => {
    const t = read('./types.ts');
    expect(t).toContain('export interface ExplainAnswerBase');
    expect(t).toContain('links?: AIAnswerLink[]');
    // `id` alanı satır içi linkin ÖN KOŞULU: onsuz arayüz kimliği
    // metinde bulup saramaz (v0.10.35).
    expect(t).toContain('export interface AIAnswerLink');
  });

  it('explain uçları tabanı GENİŞLETİYOR', () => {
    const api = read('./api.ts');
    // En az bir kod-bağlamlı uç ve bir sade uç tabanı kullanmalı;
    // biri unutulursa orada cast geri gelir.
    expect(api).toContain('ExplainAnswerBase');
    expect(api).not.toMatch(/explainCall<\{ explanation: string; exchangeId\?: string \}>/);
  });
});
