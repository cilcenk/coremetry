import { describe, it, expect } from 'vitest';
import { logFieldGlyph } from './logFieldTypes';

describe('logFieldGlyph (v0.10.280)', () => {
  it('ES mapping tipleri → glif + etiket', () => {
    expect(logFieldGlyph('keyword')).toEqual({ glyph: 'k', label: 'keyword' });
    expect(logFieldGlyph('text')).toEqual({ glyph: 't', label: 'text' });
    expect(logFieldGlyph('long')).toEqual({ glyph: '#', label: 'number' });
    expect(logFieldGlyph('double')?.label).toBe('number');
    expect(logFieldGlyph('date')?.label).toBe('date');
    expect(logFieldGlyph('boolean')?.glyph).toBe('◐');
  });
  it('bilinmeyen tip ham adıyla, tipsiz alan rozetsiz', () => {
    expect(logFieldGlyph('geo_point')).toEqual({ glyph: '·', label: 'geo_point' });
    expect(logFieldGlyph(undefined)).toBeNull();
    expect(logFieldGlyph('')).toBeNull();
  });
});
