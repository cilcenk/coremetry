import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.678 — operatör: "trace'in toplam süresini daha net görebilsek".
// Süre artık gri sayım satırının içinde değil; .trace-summary__dur ile
// şeridin belirgin sayısı (tarih vurgusu v0.10.347'nin ikizi). Kiosk başlığı
// aynı sınıfı taşır. Pin: sınıf + fmtNs(totalNs) her iki sayfada; eski
// "· {fmtNs(totalNs)}" birleşik yazımı geri gelmez.
const trace = readFileSync(resolve(__dirname, 'Trace.tsx'), 'utf8');
const kiosk = readFileSync(resolve(__dirname, 'TraceKiosk.tsx'), 'utf8');
const css = readFileSync(resolve(__dirname, '../styles/globals.css'), 'utf8');

describe('trace toplam süresi vurgusu (v0.10.678)', () => {
  it('Trace.tsx ve TraceKiosk.tsx süreyi ayrı, belirgin sınıfla çizer', () => {
    for (const src of [trace, kiosk]) {
      expect(src).toContain('className="trace-summary__dur"');
      expect(src).toContain('{fmtNs(totalNs)}</span>');
      expect(src).not.toContain("'s'} · {fmtNs(totalNs)}");
    }
  });
  it('sınıf token tabanlı ve tabular-nums', () => {
    const i = css.indexOf('.trace-summary__dur {');
    expect(i).toBeGreaterThan(-1);
    const rule = css.slice(i, css.indexOf('}', i));
    expect(rule).toContain('var(--fs-lg)');
    expect(rule).toContain('tabular-nums');
  });
});
