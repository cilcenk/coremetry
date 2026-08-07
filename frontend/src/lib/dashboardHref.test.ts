import { describe, it, expect } from 'vitest';
import { dashboardHref } from './dashboardHref';

// v0.9.772 regression — the pin-to-dashboard success link used the
// non-existent `/dashboards/<id>` shape and App.tsx's `path="*"`
// swallowed it into a redirect home. These pin the query-param form
// the real route reads (`/dashboard?id=`) and the encoding, so an id
// carrying a URL-significant character can't truncate the param.
describe('dashboardHref', () => {
  const cases: Array<{ name: string; id: string; want: string }> = [
    { name: 'plain id', id: 'abc123', want: '/dashboard?id=abc123' },
    { name: 'uuid', id: '5f3e-9a2b', want: '/dashboard?id=5f3e-9a2b' },
    { name: 'ampersand is escaped', id: 'a&edit=1', want: '/dashboard?id=a%26edit%3D1' },
    { name: 'space is escaped', id: 'my dash', want: '/dashboard?id=my%20dash' },
    { name: 'slash is escaped', id: 'a/b', want: '/dashboard?id=a%2Fb' },
    { name: 'empty id', id: '', want: '/dashboard?id=' },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(dashboardHref(c.id)).toBe(c.want);
    });
  }

  it('never emits the dead /dashboards/<id> path shape', () => {
    expect(dashboardHref('abc123').startsWith('/dashboard?')).toBe(true);
    expect(dashboardHref('abc123')).not.toContain('/dashboards/');
  });

  it('round-trips the id through URLSearchParams', () => {
    const href = dashboardHref('a&b c/d');
    const sp = new URLSearchParams(href.slice(href.indexOf('?') + 1));
    expect(sp.get('id')).toBe('a&b c/d');
  });
});
