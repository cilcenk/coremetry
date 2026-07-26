import { describe, it, expect } from 'vitest';
import { logsToCSV, logsToNDJSON, exportFilename } from './logsExport';

// v0.9.302 (L12(b)) — export the rows already in the page, with zero
// backend calls. The interesting part is not the format, it is that log
// BODIES are hostile input: they routinely contain commas, quotes and
// newlines, which is precisely what breaks a naive CSV writer and turns
// one exported row into several.

const T = 1_700_000_000_123_000_000; // unix nanos

const row = (over: Partial<Parameters<typeof logsToCSV>[0][number]> = {}) => ({
  timestamp: T,
  severityText: 'ERROR',
  serviceName: 'checkout',
  body: 'connection timeout',
  traceId: 'abc123',
  spanId: 'def456',
  ...over,
});

describe('logsToCSV', () => {
  it('writes a header plus one line per row', () => {
    const out = logsToCSV([row(), row()]).split('\n');
    expect(out[0]).toBe('time,severity,service,body,traceId,spanId');
    expect(out).toHaveLength(3);
  });

  it('renders time as ISO-8601 UTC, not a localised string', () => {
    // A localised timestamp silently changes meaning by machine; ISO
    // parses identically in Excel and pandas.
    expect(logsToCSV([row()]).split('\n')[1]).toContain('2023-11-14T22:13:20.123Z');
  });

  it('quotes a body containing a comma', () => {
    const line = logsToCSV([row({ body: 'a,b' })]).split('\n')[1];
    expect(line).toContain('"a,b"');
  });

  it('escapes embedded quotes by doubling them', () => {
    const line = logsToCSV([row({ body: 'say "hi"' })]).split('\n')[1];
    expect(line).toContain('"say ""hi"""');
  });

  it('keeps a body with a newline on ONE csv record', () => {
    // The failure that matters: an unquoted newline turns one log line
    // into two rows, and every column after it shifts.
    const csv = logsToCSV([row({ body: 'line1\nline2' })]);
    expect(csv).toContain('"line1\nline2"');
    // header + 1 record, even though the payload contains a newline
    expect(csv.split('"')[0].split('\n')).toHaveLength(2);
  });

  it('renders a numeric severity when there is no text', () => {
    const line = logsToCSV([row({ severityText: undefined, severity: 17 })]).split('\n')[1];
    expect(line.split(',')[1]).toBe('17');
  });

  it('renders missing fields as empty cells, never "undefined"', () => {
    const line = logsToCSV([{ timestamp: T }]).split('\n')[1];
    expect(line).not.toContain('undefined');
    expect(line.endsWith(',,,,')).toBe(true);
  });

  it('exports an empty list as a header only — a valid, empty file', () => {
    expect(logsToCSV([])).toBe('time,severity,service,body,traceId,spanId');
  });
});

describe('logsToNDJSON', () => {
  it('emits one parseable object per line', () => {
    const lines = logsToNDJSON([row(), row({ serviceName: 'auth' })]).split('\n');
    expect(lines).toHaveLength(2);
    expect(JSON.parse(lines[0]).service).toBe('checkout');
    expect(JSON.parse(lines[1]).service).toBe('auth');
  });

  it('survives bodies that would need CSV quoting', () => {
    // The reason NDJSON is offered at all: no quoting rules to get
    // wrong, whatever the body contains.
    const nasty = 'a,b "c" \n d';
    const parsed = JSON.parse(logsToNDJSON([row({ body: nasty })]));
    expect(parsed.body).toBe(nasty);
  });

  it('omits empty fields rather than emitting empty strings', () => {
    const parsed = JSON.parse(logsToNDJSON([{ timestamp: T, body: 'x' }]));
    expect(parsed).toEqual({ time: '2023-11-14T22:13:20.123Z', body: 'x' });
    expect('traceId' in parsed).toBe(false);
  });

  it('a line never contains a raw newline', () => {
    // Otherwise the format's one guarantee — one record per line — is
    // broken by exactly the payload it exists to handle.
    const out = logsToNDJSON([row({ body: 'l1\nl2' })]);
    expect(out.split('\n')).toHaveLength(1);
  });
});

describe('exportFilename', () => {
  it('is filesystem-safe and carries the extension', () => {
    const f = exportFilename('csv');
    expect(f.endsWith('.csv')).toBe(true);
    expect(f).not.toMatch(/[:.]\d/); // no colons from the ISO time
    expect(f.startsWith('coremetry-logs-')).toBe(true);
  });
});
