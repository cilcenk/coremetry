// chatEvidence.ts — v0.10.558 (CoSRE Faz 4c-2). SAF: evidence bloğu → kart
// satırları. Delta: taban 0 iken "yeni"; ±%15 içindeyse "~"; aksi ×N (bir
// ondalık). Zaman yerel HH:MM. Hipotez yoksa verdict metni aynen (kart yine
// çizilir — değişiklik + desen listesi tek başına değerli, operatör mockup onayı).
import type { ChatEvidence, ChatEvidenceRED, ChatTypedBlock } from './types';

export function isChatEvidence(p: unknown): p is ChatEvidence {
  const o = p as Partial<ChatEvidence> | null;
  return !!o && typeof o.service === 'string' && typeof o.verdict === 'string' && Array.isArray(o.problems) && Array.isArray(o.changes) && Array.isArray(o.logPatterns);
}

export function evidenceBlocks(blocks: ChatTypedBlock[] | undefined): ChatEvidence[] {
  return (blocks ?? []).filter(b => b.type === 'evidence').map(b => b.payload).filter(isChatEvidence);
}

export interface EvidenceDelta { dir: 'up' | 'down' | 'flat' | 'new'; text: string }

export function fmtDelta(now: number, base: number): EvidenceDelta {
  if (!(base > 0)) return now > 0 ? { dir: 'new', text: '▲ yeni' } : { dir: 'flat', text: '~' };
  const r = now / base;
  if (r >= 1.15) return { dir: 'up', text: `▲ ×${r.toFixed(1)}` };
  if (r <= 0.85) return { dir: 'down', text: `▼ ×${(base / now || 0).toFixed(1)}` };
  return { dir: 'flat', text: '~' };
}

export interface EvidenceRedRow { key: string; label: string; now: string; base: string; delta: EvidenceDelta }

const fmtPct = (v: number) => `%${v >= 10 ? v.toFixed(0) : v.toFixed(1)}`;
const fmtMs = (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(2)} s` : `${Math.round(v)} ms`);
const fmtRate = (v: number) => (v >= 10 ? v.toFixed(0) : v.toFixed(1));

export function redRows(red: ChatEvidence['red']): EvidenceRedRow[] {
  if (!red) return [];
  const c: ChatEvidenceRED = red.current, b: ChatEvidenceRED = red.baseline;
  return [
    { key: 'errorRate', label: 'hata oranı', now: fmtPct(c.errorRate), base: fmtPct(b.errorRate), delta: fmtDelta(c.errorRate, b.errorRate) },
    { key: 'p95', label: 'p95', now: fmtMs(c.p95Ms), base: fmtMs(b.p95Ms), delta: fmtDelta(c.p95Ms, b.p95Ms) },
    { key: 'rate', label: 'istek/sn', now: fmtRate(c.rate), base: fmtRate(b.rate), delta: fmtDelta(c.rate, b.rate) },
  ];
}

export function fmtEvidenceTime(ns: number): string {
  if (!(ns > 0)) return '—';
  const d = new Date(ns / 1e6);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

export function fmtRangeTR(rangeS: number): string {
  if (rangeS >= 86400 && rangeS % 86400 === 0) return `son ${rangeS / 86400} gün`;
  if (rangeS >= 3600) return `son ${+(rangeS / 3600).toFixed(rangeS % 3600 ? 1 : 0)} sa`;
  return `son ${Math.max(1, Math.round(rangeS / 60))} dk`;
}

export const evidenceHasHypothesis = (e: ChatEvidence) => e.problems.some(p => !!p.topSuspect);
