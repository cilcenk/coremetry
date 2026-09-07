// aiFeedbackAdoption.test.ts — v0.10.537 (CoSRE v2 Faz 2.6) kapısı:
// 👍/👎 rayı TEK atomdan (AIFeedbackButtons) geçer. Üç elle yazılmış kopya
// (ChatBubble.rateTurn, RCAVerdictPanel.rateVerdict, AIAnalysisPanel.
// rateAnalysis) yorum kutusu taşımıyordu; atom taşır. Kopya geri sızarsa
// burada kızarır: postAIFeedback yalnız atomda ve api istemcisinde.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';

const SRC = resolve(__dirname, '..', '..');
function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = resolve(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p);
  }
  return out;
}

describe('AI feedback rayı tek atom', () => {
  it('postAIFeedback çağrısı yalnız AIFeedbackButtons.tsx + lib/api.ts', () => {
    const offenders = walk(SRC).filter(f => {
      const s = readFileSync(f, 'utf8').replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');
      return /postAIFeedback\(/.test(s)
        && !f.endsWith('components/ai/AIFeedbackButtons.tsx') && !f.endsWith('lib/api.ts');
    });
    expect(offenders.map(f => f.replace(SRC, ''))).toEqual([]);
  });
  it('sohbet balonu, RCA paneli ve analiz paneli atomu kullanır', () => {
    for (const f of ['components/ai/ChatBubble.tsx', 'components/RCAVerdictPanel.tsx', 'components/AIAnalysisPanel.tsx']) {
      const s = readFileSync(resolve(SRC, f), 'utf8');
      expect(s, f).toContain('<AIFeedbackButtons exchangeId=');
      expect(s, f).not.toMatch(/function rate(Turn|Verdict|Analysis)\(/);
    }
  });
});
