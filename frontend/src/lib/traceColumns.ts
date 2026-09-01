// traceColumns — the /traces list column ORDER and the default
// attribute set (v0.9.841, operator request 2026-08-09).
//
// Extracted from Traces.tsx because both halves are decisions, not
// mechanics, and one of them had already drifted once: the order lived
// in a one-line useMemo whose shape ("time, then the attribute columns,
// then everything else") was never stated anywhere, so nothing could
// notice when it stopped matching what an operator wanted to read.

/**
 * The six built-in columns, in their canonical order.
 *
 * v0.10.217 (operatör, Dynatrace "Distributed traces" düzeni — mockup
 * onayı 2026-09-01): Name (= operation) ilk ve baskın kolon, Start time
 * (= time) EN SONDA. Kolon id'leri DEĞİŞMEDİ (`operation`/`time`) — kalıcı
 * genişlikler, `?cols=` ve sunucu sort anahtarları id'ye bağlı; yalnız
 * etiket ve sıra değişti (Traces.tsx COL_LABEL: Name / Start time).
 */
export const FIXED_COLS = ['operation', 'service', 'duration', 'status', 'spans', 'time'] as const;

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
 *   • 2026-08-24, operatör isteği (iki adımda): `function_id` EKLENDİ,
 *     `http.status_code` ÇIKARILDI. Küme yine dört ve artık operatörün
 *     canlı oturumunda fiilen kullandığının aynısı — v0.9.1360'ta "da"
 *     denildiği için status_code korunmuştu, operatör aynı gün açıkça
 *     çıkarılmasını istedi.
 *
 *     Gerekçe kayda değer: bu deployment'ta satırı TANIMLAYAN alanlar
 *     cluster/channel/function üçlüsü; `http.status_code` ise satırın
 *     SONUCU ve o bilgi zaten Status kolonunda var (OK/ERROR rozeti).
 *     Yani ikinci kez, daha dar bir biçimde gösteriliyordu.
 *
 *     ⚠ Genişlik bütçesi: beş kolon için Traces.tsx COL_W yeniden
 *     dağıtılmıştı (v0.9.1360); dörde inince o kısıntı GERİ ALINMADI —
 *     kısıntılar içeriğe bakılarak yapılmıştı (duration 104 "44.36ms"e,
 *     spans 58 tek haneye yeter) ve dört kolonla artık rahat pay var.
 */
export const DEFAULT_TRACE_COLUMNS: string[] = [
  'openshift.cluster.name',
  'channel_code',
  'function_code',
  'function_id',
];

/**
 * traceColumnOrder — the rendered column ids, in order.
 *
 * Name · Service · <attribute columns> · Duration · Status · Spans ·
 * Start time (v0.10.217, Dynatrace düzeni). Tarihçe: v0.9.841'de
 * Time · Service · Operation · attrs · Duration · Spans · Status idi
 * (operatör isteği 2026-08-09 — attr kolonları Time'ın hemen arkasından
 * alınıp satırı KİMLİKLEYEN iki alanın, Service ve Operation'ın, sağına
 * konmuştu). O karar korunuyor: attr kolonları yine kimlik alanlarından
 * hemen sonra ("Kalsın böyle", 2026-09-01); değişen, kimliğin başa
 * (Name) ve zaman damgasının sona gitmesi.
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
    'operation', 'service',
    ...extraCols,
    ...FIXED_COLS.filter(c => c !== 'service' && c !== 'operation'),
  ];
}
