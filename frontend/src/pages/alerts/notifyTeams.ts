// notifyTeams.ts — v0.10.519: kural bazında ekip bildirimi, SAF yardımcılar
// (NotifyTeamsField + kurallar tablosu). Backend NormalizeRuleNotify'ın
// aynası: kırp, boşları at, harfsiz kopyaları düşür, ≤10 ekip.
import type { RuleNotify } from '@/lib/types';

export const NOTIFY_MAX_TEAMS = 10;

/** Ekip ekle — kırpar, harfsiz kopyayı yok sayar, tavanı aşmaz. */
export function addNotifyTeam(cur: RuleNotify | undefined, raw: string): RuleNotify | undefined {
  const t = raw.trim();
  if (!t) return cur;
  const teams = cur?.teams ?? [];
  if (teams.some(x => x.toLowerCase() === t.toLowerCase())) return cur;
  if (teams.length >= NOTIFY_MAX_TEAMS) return cur;
  return { teams: [...teams, t], mode: cur?.mode ?? 'add' };
}

/** Ekip çıkar — son ekip de giderse hedef undefined (varsayılan yol). */
export function removeNotifyTeam(cur: RuleNotify | undefined, team: string): RuleNotify | undefined {
  if (!cur) return undefined;
  const teams = cur.teams.filter(x => x.toLowerCase() !== team.toLowerCase());
  return teams.length ? { ...cur, teams } : undefined;
}

/** Tablo/özet etiketi: "→ ug-mobile, sy" (+ " · yalnız" only kipinde). */
export function notifySummary(n: RuleNotify | undefined): string {
  if (!n || !n.teams.length) return '';
  return `→ ${n.teams.join(', ')}${n.mode === 'only' ? ' · yalnız' : ''}`;
}

/** Katalog ekiplerinden seçilmemiş olanlar, harfsiz sıralı (datalist seçenekleri). */
export function notifyTeamOptions(catalog: string[], selected: string[]): string[] {
  const sel = new Set(selected.map(s => s.toLowerCase()));
  return catalog.filter(t => !sel.has(t.toLowerCase()));
}
