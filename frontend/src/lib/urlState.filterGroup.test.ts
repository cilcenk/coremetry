import { describe, it, expect } from 'vitest';
import {
  encodeFilters,
  encodeFilterGroup,
  decodeFilterGroup,
  isFlatAndGroup,
} from './urlState';
import type { FilterExpr, FilterGroup } from './types';

// v0.8.x — trace-query gap-2 (grouped AND/OR builder) URL codec.
//
// Back-compat is the whole point: a flat-AND group must NOT serialise to the
// new `filterGroup` param — it stays on the legacy `filters` param so existing
// saved views / shared URLs are byte-identical. Only a real OR / nested group
// produces a `filterGroup` string. These pin that boundary + round-trip.

const A: FilterExpr = { k: 'http.status_code', op: '>=', v: ['500'] };
const B: FilterExpr = { k: 'db.system', op: '=', v: ['oracle'] };
const C: FilterExpr = { k: 'deployment.environment', op: '=', v: ['prod'] };

describe('isFlatAndGroup', () => {
  it('treats null / AND-no-groups as flat-AND', () => {
    expect(isFlatAndGroup(null)).toBe(true);
    expect(isFlatAndGroup({ join: 'AND', filters: [A, B] })).toBe(true);
  });
  it('treats OR or nested as NOT flat-AND', () => {
    expect(isFlatAndGroup({ join: 'OR', filters: [A, B] })).toBe(false);
    expect(isFlatAndGroup({ join: 'AND', filters: [C], groups: [{ join: 'OR', filters: [A, B] }] })).toBe(false);
  });
});

describe('encodeFilterGroup', () => {
  it('returns "" for a flat-AND group (legacy filters= carries it)', () => {
    expect(encodeFilterGroup(null)).toBe('');
    expect(encodeFilterGroup({ join: 'AND', filters: [A, B] })).toBe('');
  });

  it('emits a filterGroup string only for OR / nested groups', () => {
    const or = encodeFilterGroup({ join: 'OR', filters: [A, B] });
    expect(or).not.toBe('');
    const parsed = JSON.parse(or);
    expect(parsed.join).toBe('OR');
    expect(parsed.filters).toHaveLength(2);
  });

  it('strips empty leaves and empty nested groups', () => {
    const g: FilterGroup = {
      join: 'OR',
      filters: [A, { k: '', op: '=', v: [''] }],
      groups: [{ join: 'AND', filters: [] }, { join: 'OR', filters: [B, C] }],
    };
    const s = encodeFilterGroup(g);
    const parsed = JSON.parse(s);
    expect(parsed.filters).toHaveLength(1); // empty-key leaf dropped
    expect(parsed.groups).toHaveLength(1);  // empty group dropped
    expect(parsed.groups[0].filters).toHaveLength(2);
  });
});

describe('decodeFilterGroup', () => {
  it('returns null for absent / malformed input', () => {
    expect(decodeFilterGroup(null)).toBeNull();
    expect(decodeFilterGroup('')).toBeNull();
    expect(decodeFilterGroup('{not json')).toBeNull();
    expect(decodeFilterGroup('{"join":"OR"}')).toBeNull(); // no filters array
  });

  it('round-trips an OR group', () => {
    const g: FilterGroup = { join: 'OR', filters: [A, B] };
    const back = decodeFilterGroup(encodeFilterGroup(g));
    expect(back).not.toBeNull();
    expect(back!.join).toBe('OR');
    expect(back!.filters).toEqual([A, B]);
  });

  it('round-trips a one-level nested group', () => {
    const g: FilterGroup = {
      join: 'AND',
      filters: [C],
      groups: [{ join: 'OR', filters: [A, B] }],
    };
    const back = decodeFilterGroup(encodeFilterGroup(g));
    expect(back).not.toBeNull();
    expect(back!.join).toBe('AND');
    expect(back!.filters).toEqual([C]);
    expect(back!.groups).toHaveLength(1);
    expect(back!.groups![0].join).toBe('OR');
    expect(back!.groups![0].filters).toEqual([A, B]);
  });

  it('coerces an unknown join to AND (never silently flips to OR)', () => {
    const back = decodeFilterGroup(JSON.stringify({ join: 'bogus', filters: [A] }));
    expect(back!.join).toBe('AND');
  });
});

describe('flat path back-compat', () => {
  it('a flat-AND group routes through the legacy filters= encoder unchanged', () => {
    // The consumer encodes flat-AND via encodeFilters(group.filters); the
    // grouped encoder yields '' so the URL only ever has the legacy param.
    const flat: FilterExpr[] = [A, B];
    expect(encodeFilterGroup({ join: 'AND', filters: flat })).toBe('');
    expect(encodeFilters(flat)).toBe(JSON.stringify(flat));
  });
});
