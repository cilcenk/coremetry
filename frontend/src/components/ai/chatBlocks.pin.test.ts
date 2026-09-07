// chatBlocks.pin.test.ts — v0.10.541 kaynak pini: hook `block` olayını biriktirir;
// balon tipli chart bloklarını çizer, blok varken fence grafiğini atlar; link
// blokları çiplere katılır.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const hook = readFileSync(resolve(__dirname, 'useChatThread.ts'), 'utf8');
const bubble = readFileSync(resolve(__dirname, 'ChatBubble.tsx'), 'utf8');

describe('blok protokolü kablolaması', () => {
  it('hook block → appendChatBlock; balon chartBlocks + fence atlama + effLinks', () => {
    expect(hook).toContain("e.kind === 'block'");
    expect(hook).toContain('blocks: appendChatBlock(t.blocks,');
    expect(bubble).toContain('renderMessage(turn.text, turn.pending, turn.blocks)');
    expect(bubble).toContain('if (typedCharts.length > 0) break;');
    expect(bubble).toContain('chartBlocks(turn.blocks).map(');
    expect(bubble).toContain('const effLinks = mergeBlockLinks(turn.links, turn.blocks)');
    expect(bubble).not.toContain('turn.links.map(');
  });
});

// v0.10.542 — action bloğu: parseAction + görünürlük + applyActionHref + replace:true.
describe('action bloğu kablolaması', () => {
  it('balon aksiyonu yalnız açık sayfada, URL birleşimiyle uygular', () => {
    expect(bubble).toContain("filter(b => b.type === 'action')");
    expect(bubble).toContain('actionVisible(a, loc.pathname)');
    expect(bubble).toContain('applyActionHref(a, loc.pathname, loc.search); if (to) navigate(to, { replace: true });');
  });
});
