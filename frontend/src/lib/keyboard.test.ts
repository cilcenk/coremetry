import { describe, it, expect } from 'vitest';
import { comboFromEvent } from './keyboard';

// v0.8.x — comboFromEvent read e.key.length unguarded, throwing
// "Cannot read properties of undefined (reading 'length')" on synthetic /
// programmatic KeyboardEvents (password managers, IME composition, autofill
// dispatch) that fire keydown with no `key`. These pin the guard.
const ev = (o: Partial<KeyboardEvent>): KeyboardEvent => o as KeyboardEvent;

describe('comboFromEvent', () => {
  it('does not throw when e.key is undefined', () => {
    expect(() => comboFromEvent(ev({}))).not.toThrow();
    expect(() => comboFromEvent(ev({ shiftKey: true }))).not.toThrow();
    expect(() => comboFromEvent(ev({ metaKey: true, altKey: true }))).not.toThrow();
  });

  it('builds the expected combo for real keys', () => {
    expect(comboFromEvent(ev({ key: 'k' }))).toBe('k');
    expect(comboFromEvent(ev({ key: 'K' }))).toBe('k'); // printable → lowercased
    expect(comboFromEvent(ev({ key: 'Enter' }))).toBe('Enter'); // special key kept verbatim
    expect(comboFromEvent(ev({ metaKey: true, key: 'k' }))).toBe('mod+k');
    expect(comboFromEvent(ev({ shiftKey: true, key: 'ArrowDown' }))).toBe('shift+ArrowDown');
    // shift on a printable key is folded into the lowercased letter, no 'shift'
    expect(comboFromEvent(ev({ shiftKey: true, key: 'A' }))).toBe('a');
  });
});
