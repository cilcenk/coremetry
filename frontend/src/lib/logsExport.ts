// logsExport.ts (v0.9.302 — L12(b)) — export the /logs rows that are
// ALREADY in the page.
//
// Deliberately zero-query. The rows have been fetched, parsed and are
// sitting in the accumulator; asking Elasticsearch for them a second
// time to produce a file would be the most expensive way to get bytes
// the browser already holds. At 10B docs/day the cheapest query stays
// the one you do not send.
//
// The corollary has to be stated in the UI, not here: this exports the
// LOADED page, not the query. A synchronous unbounded export is never
// offered — a real 100k-row export is an async job with a hard row
// ceiling and streamed CSV, and it is not this.

import { csvField } from '@/pages/explore/exploreCsv';

/** The row shape the exporters read. Structural, so LogRow satisfies it. */
export interface ExportableLog {
  timestamp?: number;          // unix nanos
  severityText?: string;
  severity?: number;
  serviceName?: string;
  body?: string;
  traceId?: string;
  spanId?: string;
}

// The column order is the table's reading order, so a spreadsheet opens
// looking like the screen the operator exported it from.
const COLUMNS = ['time', 'severity', 'service', 'body', 'traceId', 'spanId'] as const;

function cells(l: ExportableLog): string[] {
  // Nanos → ISO-8601 UTC. Excel and pandas both parse it, and unlike a
  // localised string it does not silently change meaning by machine.
  const iso = l.timestamp ? new Date(l.timestamp / 1e6).toISOString() : '';
  return [
    iso,
    l.severityText || (l.severity != null ? String(l.severity) : ''),
    l.serviceName ?? '',
    l.body ?? '',
    l.traceId ?? '',
    l.spanId ?? '',
  ];
}

/** CSV, RFC 4180 quoting via the shared csvField. */
export function logsToCSV(logs: ExportableLog[]): string {
  const lines = [COLUMNS.join(',')];
  for (const l of logs) lines.push(cells(l).map(csvField).join(','));
  return lines.join('\n');
}

/**
 * NDJSON — one JSON object per line.
 *
 * Offered beside CSV because log bodies routinely contain commas,
 * quotes and newlines, and a body that survives RFC 4180 quoting can
 * still be mangled by whatever opens the file. NDJSON has no such
 * ambiguity and is what every log pipeline ingests.
 */
export function logsToNDJSON(logs: ExportableLog[]): string {
  return logs.map(l => {
    const c = cells(l);
    const obj: Record<string, string> = {};
    COLUMNS.forEach((k, i) => { if (c[i] !== '') obj[k] = c[i]; });
    return JSON.stringify(obj);
  }).join('\n');
}

/**
 * Hand the text to the browser as a download. Uses a blob URL and
 * revokes it — an un-revoked object URL pins the whole payload in
 * memory for the life of the document, which on a 2000-row export is
 * exactly the leak this page just capped (v0.9.293).
 */
export function downloadText(text: string, filename: string, mime: string): void {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

/** Timestamped filename so repeated exports don't overwrite silently. */
export function exportFilename(ext: string): string {
  return `coremetry-logs-${new Date().toISOString().replace(/[:.]/g, '-')}.${ext}`;
}
