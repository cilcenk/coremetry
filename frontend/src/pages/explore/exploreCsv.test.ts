// exploreCsv.test.ts — v0.8.412 Data-Explorer parity DE1.
import { describe, expect, it } from 'vitest';
import { csvField, panelsToCSV } from './exploreCsv';
import type { PanelData } from './PanelStack';

describe('csvField (RFC 4180)', () => {
  it('quotes only when needed and doubles embedded quotes', () => {
    expect(csvField('plain')).toBe('plain');
    expect(csvField('a,b')).toBe('"a,b"');
    expect(csvField('say "hi"')).toBe('"say ""hi"""');
    expect(csvField('line\nbreak')).toBe('"line\nbreak"');
  });
});

describe('panelsToCSV', () => {
  const panel = (over: Partial<PanelData>): PanelData => ({
    key: 'A', letter: 'A', desc: '', unit: 'ms', isFormula: false,
    loading: false, series: [], more: 0,
    ...over,
  } as PanelData);

  it('long format: one row per (query, series, bucket), ISO time, gaps empty', () => {
    const csv = panelsToCSV([panel({
      series: [{
        label: 'service=checkout, p99',
        points: [
          { time: 1751980000000000000, value: 12.5 },
          { time: 1751980060000000000, value: null },
        ],
      }],
    })]);
    const lines = csv.trimEnd().split('\n');
    expect(lines[0]).toBe('query,series,unit,time,value');
    expect(lines[1]).toBe('A,"service=checkout, p99",ms,2025-07-08T13:06:40.000Z,12.5');
    expect(lines[2]).toBe('A,"service=checkout, p99",ms,2025-07-08T13:07:40.000Z,');
  });

  it('skips loading panels', () => {
    const csv = panelsToCSV([panel({ loading: true, series: [{ label: 'x', points: [{ time: 1, value: 1 }] }] })]);
    expect(csv.trimEnd()).toBe('query,series,unit,time,value');
  });
});
