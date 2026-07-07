import { describe, it, expect } from 'vitest';
import { encodeDestinationParam, decodeDestinationParam } from './destinationParam';

// destinationParam.test.ts — v0.8.364 (Stage-2 slice M1). Pins the
// `?destination=` URL codec for the /messaging topic detail drawer
// (round-trip incl. hostile characters — the field separator `|`
// inside any field must never forge a boundary; malformed
// deep-links must decode to null, never throw).

describe('destinationParam codec', () => {
  it('round-trips a plain tuple', () => {
    const ref = { system: 'kafka', cluster: '(default)', destination: 'orders' };
    expect(decodeDestinationParam(encodeDestinationParam(ref))).toEqual(ref);
  });

  it('round-trips a bootstrap-host cluster', () => {
    const ref = {
      system: 'kafka',
      cluster: 'kafka-broker-1.prod.internal:9092',
      destination: 'payments.settled',
    };
    expect(decodeDestinationParam(encodeDestinationParam(ref))).toEqual(ref);
  });

  it('a literal | inside any field cannot forge a field boundary', () => {
    const ref = { system: 'kaf|ka', cluster: 'a|b:9092', destination: 'top|ic' };
    expect(decodeDestinationParam(encodeDestinationParam(ref))).toEqual(ref);
  });

  it('round-trips unicode + percent signs', () => {
    const ref = { system: 'rabbitmq', cluster: 'küme-1', destination: 'sipariş/%25/kuyruk' };
    expect(decodeDestinationParam(encodeDestinationParam(ref))).toEqual(ref);
  });

  it('rejects malformed input instead of throwing', () => {
    expect(decodeDestinationParam(null)).toBeNull();
    expect(decodeDestinationParam('')).toBeNull();
    expect(decodeDestinationParam('kafka')).toBeNull();               // 1 field
    expect(decodeDestinationParam('kafka|cluster')).toBeNull();       // 2 fields
    expect(decodeDestinationParam('kafka|c|dest|extra')).toBeNull();  // 4 fields
    expect(decodeDestinationParam('|cluster|dest')).toBeNull();       // empty system
    expect(decodeDestinationParam('kafka||dest')).toBeNull();         // empty cluster
    expect(decodeDestinationParam('kafka|cluster|')).toBeNull();      // empty destination
    expect(decodeDestinationParam('kafka|c|%E0%A4%A')).toBeNull();    // bad escape
  });
});
