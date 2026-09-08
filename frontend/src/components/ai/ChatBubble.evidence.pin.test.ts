// ChatBubble.evidence.pin.test.ts — v0.10.558: kanıt kartı chart bloklarının
// hemen altında, yalnız tamamlanmış turda; kart bileşeni ayrı dosyada.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const bubble = readFileSync(resolve(__dirname, 'ChatBubble.tsx'), 'utf8');
const card = readFileSync(resolve(__dirname, 'EvidenceCard.tsx'), 'utf8');

describe('EvidenceCard yerleşimi', () => {
  it('chart bloklarından sonra, pending değilken', () => {
    const chart = bubble.indexOf('chartBlocks(turn.blocks).map(');
    const ev = bubble.indexOf('evidenceBlocks(turn.blocks).map(');
    expect(chart).toBeGreaterThan(0);
    expect(ev).toBeGreaterThan(chart);
    expect(bubble).toContain('{!turn.pending && evidenceBlocks(turn.blocks)');
  });
  it('kart: verdict, RED tablosu, değişiklikler, desenler, problem linki', () => {
    for (const w of ['ev-verdict', 'RED şimdi / taban', 'Değişiklikler (pencere)', 'Log desenleri', '/problems?problem=']) {
      expect(card).toContain(w);
    }
    expect(card).not.toContain('useDataTable'); // sabit ≤10 satır: ham tablo meşru
  });
});
