import { describe, it, expect } from 'vitest';
import { isPathAllowed } from './AppShell';

// v0.9.230 — the custom-role guard compares the live pathname against the
// pages an admin granted. Several list pages open a detail route that is a
// SIBLING path rather than a sub-path (/incidents → /incident), so the
// generic `startsWith(p + '/')` rule never matched and every row click
// bounced the user back to their first allowed page. /clusters → /pod only
// became reachable-in-principle in v0.9.209, which is when /clusters was
// first added to the grantable catalogue.

describe('isPathAllowed — list pages reach their detail routes', () => {
  const cases: [string, string][] = [
    ['/incidents', '/incident'],
    ['/runbooks', '/runbook'],
    ['/runbooks', '/runbook-exec'],
    ['/clusters', '/pod'],
    ['/traces', '/trace'],
    ['/traces', '/trace/compare'],
    ['/dashboards', '/dashboard'],
    ['/services', '/service'],
    ['/services', '/service/backtrace'],
    ['/databases', '/databases/slow-queries'],
  ];
  for (const [granted, detail] of cases) {
    it(`${granted} grants ${detail}`, () => {
      expect(isPathAllowed(detail, [granted])).toBe(true);
    });
  }
});

describe('isPathAllowed — separate checkboxes stay separate', () => {
  // Services and Topology are distinct entries in the custom-role grid, so
  // granting one must not smuggle in the other. The old rule was
  // startsWith('/service'), which silently matched /service-map.
  it('/services does NOT grant /service-map', () => {
    expect(isPathAllowed('/service-map', ['/services'])).toBe(false);
  });
  it('/service-map is reachable when granted explicitly', () => {
    expect(isPathAllowed('/service-map', ['/service-map'])).toBe(true);
  });
  it('a granted list does not leak unrelated pages', () => {
    expect(isPathAllowed('/logs', ['/incidents'])).toBe(false);
    expect(isPathAllowed('/pod', ['/incidents'])).toBe(false);
    expect(isPathAllowed('/runbook', ['/clusters'])).toBe(false);
  });
  it('an empty grant list allows nothing but the always-allowed set', () => {
    expect(isPathAllowed('/logs', [])).toBe(false);
    expect(isPathAllowed('/profile', [])).toBe(true); // ALWAYS_ALLOWED
    expect(isPathAllowed('/login', [])).toBe(true);
  });
});

describe('isPathAllowed — sub-paths and exact matches', () => {
  it('a granted page matches itself exactly', () => {
    expect(isPathAllowed('/logs', ['/logs'])).toBe(true);
  });
  it('a granted page covers its own sub-paths', () => {
    expect(isPathAllowed('/system/stats', ['/system'])).toBe(true);
  });
  it('a prefix that is not a path boundary does not match', () => {
    // /log must not be granted by /logs, nor /logsomething by /logs.
    expect(isPathAllowed('/logsomething', ['/logs'])).toBe(false);
  });
});
