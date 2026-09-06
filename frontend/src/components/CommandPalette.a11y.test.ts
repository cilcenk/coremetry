import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.458 (dış skill denetimi D4) — komut paleti diyalog: role/aria-modal,
// odak tuzağı, combobox → listbox/option + activedescendant; ham renk yok.
describe('CommandPalette a11y (D4)', () => {
  const src = readFileSync(resolve(__dirname, 'CommandPalette.tsx'), 'utf8');
  it('is a modal dialog with the shared focus trap and modal tokens', () => {
    expect(src).toContain('role="dialog" aria-modal="true" aria-label="Komut paleti"');
    expect(src).toContain('useFocusTrap(dialogRef, open)');
    expect(src).toContain('className="modal-backdrop"');
    expect(src).toContain("boxShadow: 'var(--shadow-modal)'");
  });
  it('exposes the results as a combobox-controlled listbox', () => {
    expect(src).toContain('role="combobox" aria-expanded={results.length > 0} aria-controls="cp-listbox"');
    expect(src).toContain('aria-activedescendant={results.length > 0 ? `cp-opt-${selected}` : undefined}');
    expect(src).toContain('id="cp-listbox" role="listbox"');
    expect(src).toContain('id={`cp-opt-${i}`} role="option" aria-selected={i === selected}');
  });
  it('carries no raw rgb/rgba literals', () => {
    expect(src).not.toMatch(/rgba?\(/);
  });
});
