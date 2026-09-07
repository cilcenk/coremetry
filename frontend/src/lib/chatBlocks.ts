// chatBlocks.ts — v0.10.541 (Faz 3.3a): tipli blok biriktirme, SAF.
// Aynı id ikinci kez gelirse (final güncellemesi) yerine geçer; sıra seq.
import type { ChatTypedBlock, ChatAnswerLink, CosreChartSpecLike } from './types';

export function appendChatBlock(blocks: ChatTypedBlock[] | undefined, b: ChatTypedBlock): ChatTypedBlock[] {
  const rest = (blocks ?? []).filter(x => x.id !== b.id);
  rest.push(b);
  return rest.sort((a, c) => a.seq - c.seq);
}

export function chartBlocks(blocks: ChatTypedBlock[] | undefined): CosreChartSpecLike[] {
  return (blocks ?? []).filter(b => b.type === 'chart').map(b => b.payload as CosreChartSpecLike)
    .filter(p => p && typeof p.service === 'string' && typeof p.agg === 'string');
}

/** Link blokları cevap link çiplerine katılır; href tekil. */
export function mergeBlockLinks(links: ChatAnswerLink[] | undefined, blocks: ChatTypedBlock[] | undefined): ChatAnswerLink[] {
  const out: ChatAnswerLink[] = [...(links ?? [])];
  const seen = new Set(out.map(l => l.href));
  for (const b of blocks ?? []) {
    if (b.type !== 'link') continue;
    const p = b.payload as Partial<ChatAnswerLink> | null;
    if (p && typeof p.href === 'string' && p.href && !seen.has(p.href)) {
      seen.add(p.href);
      out.push({ label: typeof p.label === 'string' && p.label ? p.label : p.href, href: p.href });
    }
  }
  return out;
}
