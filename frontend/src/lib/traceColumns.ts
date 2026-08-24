// traceColumns — the /traces list column ORDER and the default
// attribute set (v0.9.841, operator request 2026-08-09).
//
// Extracted from Traces.tsx because both halves are decisions, not
// mechanics, and one of them had already drifted once: the order lived
// in a one-line useMemo whose shape ("time, then the attribute columns,
// then everything else") was never stated anywhere, so nothing could
// notice when it stopped matching what an operator wanted to read.

/** The six built-in columns, in their canonical order. */
export const FIXED_COLS = ['time', 'service', 'operation', 'duration', 'spans', 'status'] as const;

/**
 * DEFAULT_TRACE_COLUMNS — the attribute columns a fresh session gets.
 *
 * History, because this value has now been decided twice:
 *   • FAZ 2B (2026-07-23) set it EMPTY — attribute columns opt-in, on
 *     the reasoning that a fresh session should get the narrowest,
 *     fastest list.
 *   • 2026-08-09, operator request: the four attributes below ARE the
 *     working set on this deployment, and starting from a list that
 *     omits them meant re-adding the same four columns in every fresh
 *     browser. Coremetry is single-tenant, so "the operator's
 *     attributes" and "the product's default" are the same thing here.
 *
 * These are environment attribute KEYS, not identities — nothing about
 * them names a customer. An attribute a given service never emits is
 * already handled honestly by missingExtraKeys (traceExtrasMerge), so a
 * default that does not apply everywhere degrades into a labelled
 * empty column, not a silent one.
 *
 * Precedence on load is UNCHANGED: URL ?cols= (shareable source of
 * truth) → localStorage (per-browser continuity) → this default. The
 * 8-column ceiling and ColumnManager behaviour are untouched.
 *
 *   • 2026-08-24, operatör isteği: `function_id` DA varsayılan olsun.
 *     Operatörün canlı oturumu şu dördünü taşıyordu — cluster /
 *     channel_code / function_code / function_id — yani `http.status_code`
 *     fiilen elle DÜŞÜRÜLMÜŞTÜ. "da" denildiği için BEŞ olarak bırakıldı;
 *     `http.status_code`'u da istemiyorsan onu çıkarmak tek satır.
 *
 *     ⚠ Genişlik bütçesi bu ekleme İÇİN yeniden dağıtıldı — bkz.
 *     Traces.tsx COL_W şerhi. Beşinci kolon bedavaya gelmiyor.
 */
export const DEFAULT_TRACE_COLUMNS: string[] = [
  'openshift.cluster.name',
  'channel_code',
  'function_code',
  'function_id',
  'http.status_code',
];

/**
 * traceColumnOrder — the rendered column ids, in order.
 *
 * Time · Service · Operation · <attribute columns> · Duration · Spans ·
 * Status (operator request 2026-08-09). Attribute columns used to sit
 * directly behind Time, which pushed Service and Operation — the two
 * fields that IDENTIFY the row — past four attribute columns, so
 * scanning the list meant reading right to find out what each row even
 * was.
 *
 * Header and body both derive from this one array (DataTableHead over
 * `columns`, cells over the same ids), so the two can never disagree
 * about which column is which — the failure mode that makes a table
 * quietly print the wrong value under the right heading.
 *
 * Unknown / duplicate extras are the caller's business (ColumnManager
 * dedupes on add and caps at 8); this function only orders.
 */
export function traceColumnOrder(extraCols: string[]): string[] {
  return [
    'time', 'service', 'operation',
    ...extraCols,
    ...FIXED_COLS.filter(c => c !== 'time' && c !== 'service' && c !== 'operation'),
  ];
}
