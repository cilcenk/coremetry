// aiChatRetention.ts — v0.10.561 SAF: form ↔ tel. Boş kutu = varsayılan (90);
// 0 = süpürme kapalı; 0–3650 dışı hata metni (vmForm dersi: kutuya varsayılanı basma).
import type { AIChatRetention } from '@/lib/types';

export const AI_CHAT_RETENTION_DEFAULT = 90;
export const AI_CHAT_RETENTION_MAX = 3650;

export function retentionToForm(c: AIChatRetention | null | undefined): string {
  if (!c) return '';
  return String(c.days);
}

export function retentionToWire(raw: string): AIChatRetention | string {
  const t = raw.trim();
  if (t === '') return { days: AI_CHAT_RETENTION_DEFAULT };
  if (!/^\d+$/.test(t)) return 'Gün sayısı tam sayı olmalı (0 = süpürme kapalı).';
  const n = Number(t);
  if (n > AI_CHAT_RETENTION_MAX) return `En fazla ${AI_CHAT_RETENTION_MAX} gün.`;
  return { days: n };
}

export function retentionSummaryTR(c: AIChatRetention): string {
  return c.days === 0 ? 'Süpürme kapalı — sohbetler silinmez.' : `Son yazımından ${c.days} gün sonra sohbet silinir (saatlik süpürme).`;
}
