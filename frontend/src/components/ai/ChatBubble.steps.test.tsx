// ChatBubble.steps.test.tsx — v0.10.161: araç-çağrısı şeffaflık paneli
// (etüt seçenek A) çalışma zamanında. Kapalı hâl ChatBubble üzerinden
// (özet satırı, rozetler, panel yokluğu); tablo gövdesi ToolStepsPanel'in
// `initialOpen` test dikişiyle (statik render tıklayamaz): 12 çağrıda ilk 5
// satır + «7 daha», hatalı satırda ToolErrorJSON sınıfı/ipucu + tekrar rozeti,
// etiket adımı (tool yok) SAYILMAZ, süre bilinmiyorsa «—», kırpık rozeti.
import { describe, it, expect } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { ChatBubble, ToolStepsPanel } from './ChatBubble';
import type { ChatTurn, ChatStepDetail } from '@/lib/types';

function html(turn: ChatTurn): string {
  return renderToStaticMarkup(<MemoryRouter><ChatBubble turn={turn} /></MemoryRouter>);
}
function panel(details: ChatStepDetail[], over: { error?: string; turnDone?: boolean } = {}): string {
  return renderToStaticMarkup(<ToolStepsPanel details={details} error={over.error} turnDone={over.turnDone ?? true} evId={null} setEvId={() => {}} initialOpen />);
}
const d = (i: number, over: Partial<ChatStepDetail> = {}): ChatStepDetail => ({ i, tool: `tool_${i}`, args: '{"service":"payments-orchestrator"}', ok: true, preview: 'ok-preview', durationMs: 100 * i, ...over });

describe('ToolStepsPanel (v0.10.161) — kapalı hâl (ChatBubble)', () => {
  it('özet satırı: araç · hata · toplam süre; çipler yerinde', () => {
    const details = [d(1), d(2, { ok: false, preview: '{"error":"timeout","retryable":true,"hint":"pencereyi daralt"}' }), d(3)];
    const h = html({ role: 'assistant', text: 'cevap', steps: details.map(x => x.tool), stepDetails: details });
    expect(h).toContain('3 araç');
    expect(h).toContain('1 hata');
    expect(h).toContain('600 ms');
    expect(h).toContain('⚙ tool_1');
  });
  it('etiket adımı (tool yok) araç sayılmaz; bir adımın süresi yoksa toplam «—»', () => {
    const details = [{ i: 1, tool: '', label: 'ekran bağlamı: api-gateway' }, d(2), d(3, { durationMs: undefined })];
    const h = html({ role: 'assistant', text: 'x', steps: ['ekran bağlamı: api-gateway', 'tool_2', 'tool_3'], stepDetails: details });
    expect(h).toMatch(/2 araç · —/);
  });
  it('guided → tek «ön-yükleme» rozeti; bütçe aşımı → rozet (satır değil)', () => {
    const details = [d(1, { origin: 'guided', durationMs: undefined }), d(2, { origin: 'guided', durationMs: undefined })];
    const h = html({ role: 'assistant', text: 'x', steps: ['a', 'b'], stepDetails: details });
    expect(h.match(/ön-yükleme/g)?.length).toBe(1);
    const h2 = html({ role: 'assistant', text: '', steps: ['a'], stepDetails: [d(1)], error: 'Bu alışveriş 3 dakika tavanına dayandı ve durduruldu.' });
    expect(h2.match(/bütçe aşıldı/g)?.length).toBe(1);
  });
  it('detaysız turda panel yok, çipler var; yalnız etiket adımı olan turda panel yok', () => {
    const h = html({ role: 'assistant', text: 'x', steps: ['get_topology'] });
    expect(h).toContain('⚙ get_topology');
    expect(h).not.toContain('cm-steps-sum');
    const h2 = html({ role: 'assistant', text: 'x', steps: ['pencere: 12:00'], stepDetails: [{ i: 1, tool: '', label: 'pencere: 12:00' }] });
    expect(h2).not.toContain('cm-steps-sum');
  });
});

describe('ToolStepsPanel (v0.10.161) — açık tablo', () => {
  it('12 çağrıda ilk 5 satır + «7 daha»', () => {
    const details = Array.from({ length: 12 }, (_, i) => d(i + 1));
    const h = panel(details);
    expect(h.match(/<tr class="/g)?.length ?? 0).toBe(5);
    expect(h).toContain('7 daha');
  });
  it('hatalı satır: ToolErrorJSON sınıfı + ipucu, «tekrar» rozeti, is-err sınıfı; kırpık rozeti Durum hücresinde', () => {
    const details = [d(1, { ok: false, preview: '{"error":"timeout","retryable":true,"hint":"pencereyi daralt","detail":"code: 159"}' }), d(2, { truncated: true, bytes: 38200 })];
    const h = panel(details);
    expect(h).toContain('is-err');
    expect(h).toContain('timeout — pencereyi daralt');
    expect(h).toContain('hata · tekrar');
    expect(h).toContain('kırpık');
  });
  it('tur bitmiş, kanıtsız adım → «kanıt yok», süre «—»; sürüyorsa «…»', () => {
    const h = panel([d(1), { i: 2, tool: 'search_logs' }], { turnDone: true });
    expect(h).toContain('kanıt yok');
    const h2 = panel([d(1), { i: 2, tool: 'search_logs' }], { turnDone: false });
    expect(h2).toContain('sürüyor…');
  });
});
