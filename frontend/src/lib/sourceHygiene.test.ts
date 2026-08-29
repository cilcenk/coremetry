// sourceHygiene.test.ts — v0.10.167: kaynak ağacında HAM kontrol baytı yok.
//
// Dört dosya (LogTable, ShapesView, MetricQueryEditor, axisSize) anahtar
// ayırıcı olarak DÜZ NUL/0x01/0x1f baytı taşıyordu (kaçış yerine ham
// karakter). Çalışıyordu — ama grep dosyayı «Binary file … matches» diye
// atlıyor: make audit ve her grep-tabanlı tarama o dosyalara KÖRDÜ
// ([[feedback-binary-poisoned-source]]: NUL baytı tüm gate'lerden geçer).
// Kaçışlı yazım (`\\0`, `\\x1f`) aynı dizgi; bu kapı sınıfı kapatır.
// Tab/LF/CR serbest; 0x00-0x08, 0x0b, 0x0c, 0x0e-0x1f yasak.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';

const ROOT = resolve(__dirname, '..');
const EXT = /\.(tsx?|css|json|mjs|html)$/;
const CTRL = /[\x00-\x08\x0b\x0c\x0e-\x1f]/;

function walk(dir: string, out: string[]): string[] {
  for (const f of readdirSync(dir)) {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) { if (f !== 'node_modules') walk(p, out); }
    else if (EXT.test(f)) out.push(p);
  }
  return out;
}

describe('kaynak hijyeni — ham kontrol baytı yok (v0.10.167)', () => {
  it('src altında hiçbir kaynak dosyası 0x00-0x1f (tab/LF/CR hariç) taşımaz', () => {
    const bad: string[] = [];
    for (const p of walk(ROOT, [])) {
      const s = readFileSync(p, 'latin1');
      const m = s.match(CTRL);
      if (m) bad.push(`${p.slice(ROOT.length + 1)} (0x${m[0].charCodeAt(0).toString(16).padStart(2, '0')} @ satır ${s.slice(0, m.index).split('\n').length})`);
    }
    expect(bad).toEqual([]);
  });
});
