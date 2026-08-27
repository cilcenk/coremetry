import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// explainCacheFE.scan.test.ts — v0.10.83'ün FE yarısı (node/kaynak).
//
// Üç sözleşme:
//  1. "Yeniden sor" fresh geçer → sunucu önbelleği atlar. Geçmezse düğme
//     aynı cevabı anında geri getirir ve İŞLEVSİZDİR.
//  2. explainCall fresh'i ?refresh=1'e çevirir — her iki kipte de (yol
//     dizesine eklendiği için buffered+stream ikisini de kapsar).
//  3. İsabet ETİKETLENİR: rozet cevabın üstünde, yaşıyla.

const read = (rel: string) => readFileSync(new URL(rel, import.meta.url), 'utf8');

describe('explain önbelleği FE kablolaması', () => {
  it('"Yeniden sor" fresh=true geçiyor; otomatik koşu geçmiyor', () => {
    const src = read('./CopilotExplain.tsx');
    expect(src).toContain('void run(includeCode, true)');
    // auto koşu fresh'siz kalmalı — önbellekten faydalanan yol o.
    expect(src).toContain('void run();');
  });

  it('explainCall fresh → refresh=1', () => {
    const api = read('../lib/api.ts');
    expect(api).toContain("path += (path.includes('?') ? '&' : '?') + 'refresh=1'");
  });

  it('isabet rozeti çiziliyor ve taze yolu söylüyor', () => {
    const src = read('./CopilotExplain.tsx');
    expect(src).toContain('♻ önbellekten');
    expect(src).toContain('Yeniden sor');
    expect(src).toContain('setCachedAtMs(r.cached ?');
  });
});
