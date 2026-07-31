import { useEffect, useRef } from 'react';

// AI-drawer ↔ sayfa köprüsü (v0.9.477). Çekmece AppShell'de, TEK yerde
// mount edilir; kanıt (evidence) sözleşmesini kullanan waterfall / örnek-
// trace tabloları ise sayfa state'inde yaşar. Aradaki bağı prop yerine
// window CustomEvent taşıyor — emsal: CopilotExplain'in `coremetry:ai-ask`
// köprüsü (v0.9.165). Böylece çekmece hiçbir sayfayı, sayfalar da çekmeceyi
// import etmez; tek-commit geri alma kolay kalır.
//
// v0.9.408 / v0.9.414 sözleşmesi AYNEN korunur: explain-trace'in
// evidenceSpanIds'i waterfall satırlarını, explain-exception'ın
// evidenceTraceIds'i örnek-trace satırlarını `.wf-evidence` ile kutular.
export const AI_EVIDENCE_EVENT = 'coremetry:ai-evidence';
export const AI_FOCUS_EVENT = 'coremetry:ai-focus';

export type AIEvidenceDetail = { spanIds?: string[]; traceIds?: string[] };
// Çekmecedeki kanıt satırına tıklama: sayfa hedefe kaydırır + seçer.
export type AIFocusDetail = { spanId?: string; traceId?: string };

export function emitAiEvidence(detail: AIEvidenceDetail) {
  if (typeof window === 'undefined') return;
  window.dispatchEvent(new CustomEvent(AI_EVIDENCE_EVENT, { detail }));
}

export function emitAiFocus(detail: AIFocusDetail) {
  if (typeof window === 'undefined') return;
  window.dispatchEvent(new CustomEvent(AI_FOCUS_EVENT, { detail }));
}

// Handler ref'te tutulur: çağıran satır-içi arrow geçse bile dinleyici
// her render'da sökülüp takılmaz.
function useWindowEvent<T>(name: string, handler: (detail: T) => void) {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    const h = (e: Event) => ref.current((e as CustomEvent<T>).detail);
    window.addEventListener(name, h);
    return () => window.removeEventListener(name, h);
  }, [name]);
}

export function useAiEvidence(handler: (d: AIEvidenceDetail) => void) {
  useWindowEvent<AIEvidenceDetail>(AI_EVIDENCE_EVENT, handler);
}

export function useAiFocus(handler: (d: AIFocusDetail) => void) {
  useWindowEvent<AIFocusDetail>(AI_FOCUS_EVENT, handler);
}

// scrollToAttr — kanıt satırına git. Waterfall satırları `data-span-id`,
// exception örnek satırları `data-trace-id` taşır; çekmece kapandıktan
// SONRA (bir frame) kaydır, yoksa overlay hâlâ ekranı kaplıyor olur.
export function scrollToAttr(attr: 'data-span-id' | 'data-trace-id', value: string) {
  if (typeof window === 'undefined') return;
  requestAnimationFrame(() => {
    // Öznitelik seçicisi tırnak içinde: yalnız " ve \ kaçırılır (id'ler
    // hex, ama elle üretilmiş bir değer seçiciyi bozmasın).
    const sel = `[${attr}="${value.replace(/["\\]/g, '\\$&')}"]`;
    document.querySelector(sel)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  });
}
