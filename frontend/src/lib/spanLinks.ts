// spanLinks.ts — v0.10.274 (trace view Dilim 1a, docs/audit/trace-view.md §2.3).
// GET /api/traces/{id}/links trace-düzeyi iki yön döndürür; şerit (Trace.tsx
// LinkedTracesSection) bunu trace başına dedupe edip spanId'yi ATIYORDU —
// "bu span hangi span'e link veriyor" cevaplanamıyordu. Bu saf çekirdek aynı
// payload'ı span'e indirger: OUTGOING satırın sahibi `spanId` (görüntülenen
// trace'in span'i), INCOMING satırın hedefi `linkedSpanId` (yine bu trace'in
// span'i). Aynı-trace link'i (follows_from vb.) iki uçta da görünür: kaynakta
// outgoing, hedefte incoming. Tekrar (aynı kaynak+hedef) düşer. React/DOM yok.

import type { SpanLink, TraceLinks } from './types';

export interface SpanLinkEntry {
  outgoing: SpanLink[];
  incoming: SpanLink[];
}

export type SpanLinkIndex = Map<string, SpanLinkEntry>;

function entry(idx: SpanLinkIndex, spanId: string): SpanLinkEntry {
  let e = idx.get(spanId);
  if (!e) { e = { outgoing: [], incoming: [] }; idx.set(spanId, e); }
  return e;
}

/** indexSpanLinks — trace payload'ı → span başına giden/gelen link'ler. */
export function indexSpanLinks(traceId: string, links: TraceLinks | null | undefined): SpanLinkIndex {
  const idx: SpanLinkIndex = new Map();
  if (!links) return idx;
  const seen = new Set<string>();
  for (const l of links.outgoing ?? []) {
    if (!l.spanId || !l.linkedTraceId) continue;
    const key = `${l.spanId}>${l.linkedTraceId}/${l.linkedSpanId}`;
    if (seen.has(key)) continue;
    seen.add(key);
    entry(idx, l.spanId).outgoing.push(l);
    if (l.linkedTraceId === traceId && l.linkedSpanId) {
      entry(idx, l.linkedSpanId).incoming.push(l);
    }
  }
  for (const l of links.incoming ?? []) {
    if (!l.linkedSpanId || !l.traceId) continue;
    if (l.traceId === traceId) continue; // aynı-trace satırı outgoing'den zaten indekslendi
    const key = `${l.spanId}>${l.linkedTraceId}/${l.linkedSpanId}`;
    if (seen.has(key)) continue;
    seen.add(key);
    entry(idx, l.linkedSpanId).incoming.push(l);
  }
  return idx;
}

/** linkedSpanIds — şelale rozeti için: en az bir link'i olan span'ler. */
export function linkedSpanIds(idx: SpanLinkIndex): Set<string> {
  const out = new Set<string>();
  for (const [id, e] of idx) if (e.outgoing.length + e.incoming.length > 0) out.add(id);
  return out;
}

export function linkCount(e: SpanLinkEntry | undefined): number {
  return e ? e.outgoing.length + e.incoming.length : 0;
}
