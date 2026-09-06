import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.448 (log arama denetimi C8) — ?panel= URL'den okunur/yazılır,
// süzgeç kimliğine girmez, sekme panele URL'den geçer, localStorage
// varsayılanı korunur.
describe('Logs ?panel= (C8)', () => {
  const src = readFileSync(resolve(__dirname, 'Logs.tsx'), 'utf8');
  const url = readFileSync(resolve(__dirname, '../lib/logsUrl.ts'), 'utf8');
  it('reads the param with the narrowing parser and keeps localStorage as fallback', () => {
    expect(src).toContain("parseLogsPanel(searchParams.get('panel'))");
    expect(src).toContain("urlPanel !== null || getRaw('logs.patterns.open') === '1'");
  });
  it('writes the param on toggle/tab with replace:true and passes tab to the panel', () => {
    expect(src).toContain("next.set('panel', tab); else next.delete('panel');");
    expect(src).toContain('tab={panelTab} onTab={onPanelTab}');
  });
  it('panel never enters the filter identity', () => {
    const sig = url.slice(url.indexOf('export function logsUrlSig'), url.indexOf('export function logsUrlSig') + 600);
    expect(sig).not.toContain('panel');
    const reader = url.slice(url.indexOf('export function readLogsParams'), url.indexOf('export function readLogsParams') + 900);
    expect(reader).not.toContain("p.get('panel')");
  });
});
