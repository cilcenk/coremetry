// aiProfiles.ts — v0.10.176: AI model profilleri paneli için saf yardımcılar
// (AiProfilesPanel.tsx). Test: aiProfiles.test.ts.
import type { AIModelProfile, AIProvider } from '@/lib/types';

/** Etiketten profil kimliği: [a-z0-9][a-z0-9_-]{0,39} (sunucu ValidateProfile ile aynı). */
export function slugifyProfileId(label: string): string {
  // Türkçe: ı → i açıkça (ayrışmaz); İ küçülünce i + U+0307, NFD + işaret silme onu ve ç/ğ/ö/ş/ü'yü çözer.
  const s = label.replace(/ı/g, 'i').replace(/I/g, 'i').toLowerCase().normalize('NFD').replace(/\p{M}/gu, '')
    .replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '').replace(/-{2,}/g, '-');
  return s.slice(0, 40).replace(/^[^a-z0-9]+/, '').replace(/-+$/, '');
}

export const PROFILE_ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/;

/** Tuning özeti: "8k tok · t=0 · 60 s" — eksikler küresel değere düşer, yazılmaz. */
export function tuningSummary(p: Pick<AIModelProfile, 'maxTokens' | 'temperature' | 'timeoutS'>): string {
  const parts: string[] = [];
  if (p.maxTokens) parts.push(p.maxTokens >= 1000 ? `${Math.round(p.maxTokens / 1000)}k tok` : `${p.maxTokens} tok`);
  if (p.temperature !== undefined && p.temperature !== null) parts.push(`t=${p.temperature}`);
  if (p.timeoutS) parts.push(`${p.timeoutS} s`);
  return parts.length ? parts.join(' · ') : 'küresel';
}

/** Endpoint sütunu: openai → baseUrl (yoksa api.openai.com), öteki sağlayıcılar sabit. */
export function endpointLabel(provider: AIProvider, baseUrl?: string): string {
  if (provider === 'openai') return baseUrl?.trim() || 'api.openai.com';
  if (provider === 'github') return 'GitHub Copilot';
  return 'api.anthropic.com';
}

/** Profil, çağrı alabilecek durumda mı (sunucu Active() mantığının aynası): anahtar ya da openai+baseUrl. */
export function profileUsable(p: Pick<AIModelProfile, 'provider' | 'hasKey' | 'baseUrl'>): boolean {
  return p.hasKey || (p.provider === 'openai' && !!p.baseUrl?.trim());
}
