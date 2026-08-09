import { describe, it, expect } from 'vitest';
import {
  databaseDetailHref, parseDatabasePageRef, legacyDatabaseRowTarget,
} from './databaseParam';
import { depRowKey } from '@/lib/depsTable';

// databaseParam.test.ts — v0.9.840. Pins the /database page's URL
// contract now that the /databases row drawer is retired.
//
// The two things that can fail silently here:
//
//  1. IDENTITY IS A TRIPLE (v0.9.821). (system, instance, dbName) — an
//     empty dbName means "every database on this instance", which is a
//     real state, not a missing one. Dropping the distinction is what
//     made every row on a multi-DB host open the same drawer.
//  2. ?row= IS A SHARED PARAM. /databases and /messaging both write it
//     through depRowKey. The redirect must claim DB rows (cluster
//     empty) and leave messaging rows alone — hijacking one would send
//     an operator looking at a Kafka topic to a database page.

describe('databaseDetailHref / parseDatabasePageRef', () => {
  it('round-trips the full identity triple', () => {
    const ref = {
      system: 'oracle', instance: 'db-core-01:1521',
      dbName: 'CORE', source: 'receiver' as const,
    };
    const href = databaseDetailHref(ref);
    expect(parseDatabasePageRef(href.split('?')[1])).toEqual(ref);
  });

  it('keeps an empty dbName empty — never invents a narrowing', () => {
    const href = databaseDetailHref({
      system: 'postgresql', instance: 'pg-1', dbName: '', source: 'spans',
    });
    const p = new URLSearchParams(href.split('?')[1]);
    expect(p.has('name')).toBe(false);
    expect(parseDatabasePageRef(href.split('?')[1])?.dbName).toBe('');
  });

  it('only marks source when it is receiver', () => {
    const spans = databaseDetailHref({
      system: 'mysql', instance: 'm1', dbName: 'app', source: 'spans',
    });
    expect(new URLSearchParams(spans.split('?')[1]).has('source')).toBe(false);
    // Defaulting to 'spans' merely hides the receiver-only engine panel;
    // defaulting the other way would render an engine panel for a row
    // that has no receiver behind it.
    expect(parseDatabasePageRef(spans.split('?')[1])?.source).toBe('spans');
  });

  it('carries the scope', () => {
    const href = databaseDetailHref(
      { system: 'redis', instance: 'cache-01', dbName: '', source: 'spans' },
      { range: '6h', env: 'uat' });
    const p = new URLSearchParams(href.split('?')[1]);
    expect(p.get('range')).toBe('6h');
    expect(p.get('env')).toBe('uat');
  });

  it('needs system AND instance', () => {
    expect(parseDatabasePageRef('system=oracle')).toBeNull();
    expect(parseDatabasePageRef('instance=db-1')).toBeNull();
    expect(parseDatabasePageRef('')).toBeNull();
  });

  it('survives an instance with separators and spaces', () => {
    const ref = {
      system: 'oracle', instance: 'host|weird:1521', dbName: 'A B',
      source: 'spans' as const,
    };
    expect(parseDatabasePageRef(databaseDetailHref(ref).split('?')[1])).toEqual(ref);
  });
});

describe('legacyDatabaseRowTarget', () => {
  it('redirects a DB row key produced by depRowKey', () => {
    const key = depRowKey({
      system: 'oracle', cluster: '', instance: 'db-core-01:1521', dbName: 'CORE',
    });
    const t = legacyDatabaseRowTarget(`row=${encodeURIComponent(key)}&range=1h`);
    expect(t).not.toBeNull();
    const p = new URLSearchParams(t!.split('?')[1]);
    expect(p.get('system')).toBe('oracle');
    expect(p.get('instance')).toBe('db-core-01:1521');
    expect(p.get('name')).toBe('CORE');
    expect(p.get('range')).toBe('1h');
  });

  it('redirects an instance-wide row (no db.name)', () => {
    const key = depRowKey({ system: 'redis', cluster: '', instance: 'cache-01' });
    const p = new URLSearchParams(legacyDatabaseRowTarget(`row=${encodeURIComponent(key)}`)!.split('?')[1]);
    expect(p.get('instance')).toBe('cache-01');
    expect(p.has('name')).toBe(false);
  });

  it('never claims a messaging row (cluster is populated)', () => {
    const key = depRowKey({
      system: 'kafka', cluster: 'prod-eu', destination: 'orders-topic',
    });
    expect(legacyDatabaseRowTarget(`row=${encodeURIComponent(key)}`)).toBeNull();
  });

  it('does not guess the origin it cannot know', () => {
    // depRowKey carries no source field, so the redirect must omit it
    // rather than assert 'receiver' and render an engine panel for a
    // row that may have none.
    const key = depRowKey({ system: 'oracle', cluster: '', instance: 'db-1', dbName: 'X' });
    const t = legacyDatabaseRowTarget(`row=${encodeURIComponent(key)}`)!;
    expect(new URLSearchParams(t.split('?')[1]).has('source')).toBe(false);
  });

  it('returns null when there is nothing to redirect', () => {
    expect(legacyDatabaseRowTarget('')).toBeNull();
    expect(legacyDatabaseRowTarget('dbsys=oracle')).toBeNull();
    expect(legacyDatabaseRowTarget('row=oracle|')).toBeNull();       // wrong arity
    expect(legacyDatabaseRowTarget('row=|||')).toBeNull();           // empty identity
    expect(legacyDatabaseRowTarget('row=oracle||db-1')).toBeNull();  // 3 fields, not 4
  });
});
