import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { traceCountReasonHint } from './traceCountReason';

// v0.9.638 — sayım reddedildiğinde SEBEP gösterilmeli. Sessiz bir "—"
// operatöre filtreyi gevşetmeyi denemesi gerektiğini söylemez.

describe('traceCountReasonHint', () => {
  it('her sebep için eyleme dönük bir metin verir', () => {
    for (const r of ['raw-path-filter', 'duration-filter', 'service+filter']) {
      const h = traceCountReasonHint(r);
      expect(h.length).toBeGreaterThan(30);
      // Eyleme dönük olmalı: ya ne yapacağını söylesin ya neden olmadığını.
      expect(h).toMatch(/kaldır|atlandı|karşılanamıyor/);
    }
  });

  it('bilinmeyen sebepte de bir şey söyler', () => {
    expect(traceCountReasonHint('kim-bilir')).toBeTruthy();
  });
});

// EN ÖNEMLİSİ: backend'in ürettiği HER sebebin bir karşılığı olmalı.
// Backend yeni bir ret sebebi eklerse ve buraya metin eklenmezse
// operatör jenerik bir cümle görür — sessiz bozulma.
describe('backend ile eşleşme', () => {
  const go = readFileSync(
    resolve(__dirname, '../../../internal/chstore/trace_count.go'), 'utf8',
  );
  const reasons = [...go.matchAll(/traceCountReason\w+\s*=\s*"([^"]+)"/g)].map(m => m[1]);

  it('backend sebep sabitleri okunabiliyor', () => {
    expect(reasons.length).toBeGreaterThanOrEqual(3);
  });

  it('her backend sebebinin ÖZEL bir metni var', () => {
    const generic = traceCountReasonHint('__yok__');
    for (const r of reasons) {
      expect(traceCountReasonHint(r), `sebep ${r} için metin yok`).not.toBe(generic);
    }
  });
});
