// toolSteps.ts — v0.10.161: Copilot araç-çağrısı şeffaflık panelinin saf
// çekirdeği (tasarım etüdü seçenek A «Adım listesi», iki yargıçta birinci;
// scratchpad/copilot-tools). Sözleşme toolSteps.test.ts'te pinli.
//
// Yargıç must-fix'leri burada kural oldu:
//   - toplam süre yalnız TÜM yürütülen adımların `durationMs`i varsa (yoksa
//     null → «—»; ölçüm gibi çizilmez),
//   - «ön-yükleme» rozeti `step.origin === 'guided'`ten, delta olayından
//     çıkarım YOK (drawer katmanı da delta yayınlıyor),
//   - hata sayacı ok=false olan her sonuç (timeout + tekrar koruması dâhil).
import type { ChatStepDetail } from '@/lib/types';

/** Kapalı panelde görünen satır sayısı; kalanı «▸ N daha». */
export const VISIBLE_ROWS = 5;

export interface StepsSummary {
  count: number;
  /** ok === false olan sonuç sayısı */
  errors: number;
  /** sonucu henüz gelmemiş adım sayısı (preview undefined) — tur bitmişse 0 */
  pending: number;
  /** tur bitti ama kanıtı hiç yayınlanmamış adım (sunucu boş metinde step-result yayınlamaz) */
  noEvidence: number;
  /** Σ durationMs — yürütülen HER adımın süresi biliniyorsa; aksi null */
  totalMs: number | null;
  /** sonucu gelmiş ama süresi olmayan adım sayısı (guided ön-yükleme, eski sunucu) */
  unknownDuration: number;
  /** tüm adımlar sunucu ön-yüklemesi (step.origin === 'guided') */
  guided: boolean;
  /** v0.10.172 — serbest soru niyet sınıflandırmasından geçti (step.origin === 'intent') */
  intent: boolean;
}

/**
 * @param turnDone tur bitti (pending=false): sonucu hiç gelmemiş adım artık
 * «sürüyor» değil «kanıt yok» (emitStepEvidence boş metinde yayınlamaz).
 */
export function summarizeSteps(details: ChatStepDetail[], turnDone = false): StepsSummary {
  let errors = 0, pending = 0, noEvidence = 0, total = 0, unknown = 0, guidedN = 0, intentN = 0;
  for (const d of details) {
    if (d.origin === 'guided') guidedN++;
    if (d.origin === 'intent') intentN++;
    if (d.preview === undefined) { if (turnDone) { noEvidence++; unknown++; } else pending++; continue; }
    if (d.ok === false) errors++;
    if (typeof d.durationMs === 'number' && d.durationMs >= 0) total += d.durationMs; else unknown++;
  }
  return {
    count: details.length,
    errors, pending, noEvidence,
    totalMs: details.length > 0 && unknown === 0 && pending === 0 ? total : null,
    unknownDuration: unknown,
    guided: details.length > 0 && guidedN === details.length,
    // yalnız sınıflandırma GERÇEKTEN dispatch ettiyse (kalan adımlar ön-yükleme); none/hata → serbest döngü, rozet yalan olurdu
    intent: intentN > 0 && guidedN + intentN === details.length,
  };
}

export interface ToolErrorView { cls: string; retryable?: boolean; hint?: string; detail?: string }

/**
 * mcp.ToolErrorJSON sözleşmesi ({error, retryable, hint, detail}) — çipin
 * önizlemesi bu JSON'sa satır sınıf + ipucu gösterir; değilse null (ham
 * metin olduğu gibi kalır).
 */
export function parseToolError(preview: string | undefined): ToolErrorView | null {
  const s = (preview ?? '').trim();
  if (!s.startsWith('{')) return null;
  try {
    const o = JSON.parse(s) as Record<string, unknown>;
    if (typeof o.error !== 'string' || !o.error) return null;
    const v: ToolErrorView = { cls: o.error };
    if (typeof o.retryable === 'boolean') v.retryable = o.retryable;
    if (typeof o.hint === 'string' && o.hint) v.hint = o.hint;
    if (typeof o.detail === 'string' && o.detail) v.detail = o.detail;
    return v;
  } catch {
    return null;
  }
}

/** Önizlemenin ilk satırı, `max` karaktere kırpılmış («…»); boş → «(boş)». */
export function previewFirstLine(preview: string | undefined, max: number): string {
  if (preview === undefined) return '';
  const line = preview.split('\n')[0].trim();
  if (!line) return '(boş)';
  return line.length > max ? line.slice(0, max) + '…' : line;
}

export function visibleRows<T>(rows: T[], expanded: boolean): T[] {
  return expanded ? rows : rows.slice(0, VISIBLE_ROWS);
}

/**
 * Bütçe aşımı — sunucu yapısal bir `budget` olayı yayınlamıyor (brief §5);
 * chatDeadlineMessageTR metninden («… tavanına dayandı …») tanınır.
 */
export function isDeadlineError(err: string | undefined): boolean {
  return /tavan[ıi]na dayand[ıi]/i.test(err ?? '');
}

export function fmtMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
}
