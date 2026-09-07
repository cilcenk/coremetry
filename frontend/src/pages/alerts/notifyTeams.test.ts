// v0.10.519 — kural bazında ekip bildirimi: saf yardımcılar + kaynak pinleri.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { addNotifyTeam, removeNotifyTeam, notifySummary, notifyTeamOptions, NOTIFY_MAX_TEAMS } from './notifyTeams';

describe('notifyTeams', () => {
  it('ekle: kırpar, harfsiz kopyayı ve boşu yok sayar, varsayılan kip add', () => {
    let n = addNotifyTeam(undefined, '  ug-mobile ');
    expect(n).toEqual({ teams: ['ug-mobile'], mode: 'add' });
    n = addNotifyTeam(n, 'UG-MOBILE');
    expect(n!.teams).toEqual(['ug-mobile']);
    expect(addNotifyTeam(n, '   ')).toBe(n);
  });
  it('tavan: 10 ekip', () => {
    let n = undefined as ReturnType<typeof addNotifyTeam>;
    for (let i = 0; i < NOTIFY_MAX_TEAMS + 3; i++) n = addNotifyTeam(n, `t${i}`);
    expect(n!.teams.length).toBe(NOTIFY_MAX_TEAMS);
  });
  it('çıkar: son ekip gidince undefined (varsayılan yol), kip korunur', () => {
    const n = { teams: ['a', 'b'], mode: 'only' as const };
    expect(removeNotifyTeam(n, 'A')).toEqual({ teams: ['b'], mode: 'only' });
    expect(removeNotifyTeam({ teams: ['a'] }, 'a')).toBeUndefined();
    expect(removeNotifyTeam(undefined, 'a')).toBeUndefined();
  });
  it('özet ve seçenekler', () => {
    expect(notifySummary(undefined)).toBe('');
    expect(notifySummary({ teams: ['ug', 'sy'] })).toBe('→ ug, sy');
    expect(notifySummary({ teams: ['dev'], mode: 'only' })).toBe('→ dev · yalnız');
    expect(notifyTeamOptions(['ug', 'sy', 'dev'], ['SY'])).toEqual(['ug', 'dev']);
  });
  it('kaynak pinleri: form alanı Runbook altında, tablo özeti, tip', () => {
    const alerts = readFileSync(resolve(__dirname, '../Alerts.tsx'), 'utf8');
    expect(alerts).toContain('<NotifyTeamsField value={draft.notify}');
    expect(alerts.indexOf('Runbook URL (optional)')).toBeLessThan(alerts.indexOf('<NotifyTeamsField'));
    expect(alerts).toContain('notifySummary(r.notify)');
    const types = readFileSync(resolve(__dirname, '../../lib/types.ts'), 'utf8');
    expect(types).toMatch(/export interface RuleNotify \{\s*teams: string\[\];\s*mode\?: 'add' \| 'only';/);
    expect(types).toMatch(/export interface AlertRule \{[^}]*notify\?: RuleNotify;/);
  });
});
