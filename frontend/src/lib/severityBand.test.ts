import { describe, it, expect } from 'vitest';
import { severityBandOf, type SeverityBand } from './severityBand';

// v0.8.377 — Operator-reported: severity histogram band counts wrong
// on the external-ES test env. One failure mode was purely client-side:
// bandToFacet prefix-matched TEXT only, so the CH backend's
// toString(severity_num) numeric series names ('17', '9', …) all fell
// into the DEBUG bucket — an SDK emitting only severity_number showed
// its ERRORS as DEBUG. severityBandOf adds OTel numeric banding and is
// the single client-side mirror of the server-side canonical bands.

describe('severityBandOf', () => {
  const cases: Array<[string, SeverityBand]> = [
    // Canonical names the fixed backends now emit — trivially recognised.
    ['ERROR', 'ERROR'], ['WARN', 'WARN'], ['INFO', 'INFO'],
    ['DEBUG', 'DEBUG'], ['TRACE', 'TRACE'], ['OTHER', 'OTHER'],
    // Text variants (pre-fix cached payloads, exotic backends): casing,
    // suffixes, FATAL folding, short err.
    ['error', 'ERROR'], ['Error:', 'ERROR'], ['err', 'ERROR'], ['FATAL', 'ERROR'], ['fatal', 'ERROR'],
    ['warning', 'WARN'], ['Warn', 'WARN'],
    ['information', 'INFO'], ['info', 'INFO'],
    ['debug', 'DEBUG'], ['trace', 'TRACE'],
    [' error ', 'ERROR'], // trimmed
    // Numeric severity_number strings — THE v0.8.377 bug: every one of
    // these previously banded as debug. OTel: 17-24 ERROR, 13-16 WARN,
    // 9-12 INFO, 5-8 DEBUG, 1-4 TRACE.
    ['17', 'ERROR'], ['21', 'ERROR'], ['24', 'ERROR'],
    ['13', 'WARN'], ['16', 'WARN'],
    ['9', 'INFO'], ['12', 'INFO'],
    ['5', 'DEBUG'], ['8', 'DEBUG'],
    ['1', 'TRACE'], ['4', 'TRACE'],
    // Out-of-range numbers + non-severity names → OTHER.
    ['0', 'OTHER'], ['25', 'OTHER'], ['100', 'OTHER'],
    ['notice', 'OTHER'], ['_total', 'OTHER'], ['', 'OTHER'],
    ['1.5', 'OTHER'], ['-3', 'OTHER'], // non-integer / negative are not OTel numbers
  ];
  for (const [name, want] of cases) {
    it(`bands ${JSON.stringify(name)} as ${want}`, () => {
      expect(severityBandOf(name)).toBe(want);
    });
  }
});
