// severityBand — canonical severity-band resolution for log series
// names. v0.8.377, operator-reported: the severity histogram (/logs +
// service Logs tab) showed wrong band counts — the CH backend emitted
// toString(severity_num) numeric strings and the ES backend leaked raw
// terms, which the text-only prefix matching then dumped into the
// DEBUG band (an SDK emitting only severity_number rendered its
// ERRORS as DEBUG).
//
// Both backends now band server-side into ERROR/WARN/INFO/DEBUG/TRACE
// (+OTHER); this module is the belt-and-braces client mirror so
// pre-fix cached payloads and exotic backends still band right.
// Rules — keep in lockstep with severityBands (logstore/
// elasticsearch.go) and chSeverityBandExpr (logstore/clickhouse.go):
//   - text prefix, case-insensitive: FATAL*/ERR* → ERROR (ERR catches
//     err/error/error:), WARN* → WARN, INFO* → INFO, DEBUG* → DEBUG,
//     TRACE* → TRACE;
//   - numeric string → OTel severity_number ranges: 17-24 ERROR,
//     13-16 WARN, 9-12 INFO, 5-8 DEBUG, 1-4 TRACE;
//   - anything else (0, >24, "notice", "_total", empty) → OTHER.

export type SeverityBand = 'ERROR' | 'WARN' | 'INFO' | 'DEBUG' | 'TRACE' | 'OTHER';

export function severityBandOf(name: string): SeverityBand {
  const u = name.trim().toUpperCase();
  if (u.startsWith('FATAL') || u.startsWith('ERR')) return 'ERROR';
  if (u.startsWith('WARN')) return 'WARN';
  if (u.startsWith('INFO')) return 'INFO';
  if (u.startsWith('DEBUG')) return 'DEBUG';
  if (u.startsWith('TRACE')) return 'TRACE';
  if (u !== '' && /^\d+$/.test(u)) {
    const n = Number(u);
    if (n >= 17 && n <= 24) return 'ERROR';
    if (n >= 13 && n <= 16) return 'WARN';
    if (n >= 9 && n <= 12) return 'INFO';
    if (n >= 5 && n <= 8) return 'DEBUG';
    if (n >= 1 && n <= 4) return 'TRACE';
  }
  return 'OTHER';
}
