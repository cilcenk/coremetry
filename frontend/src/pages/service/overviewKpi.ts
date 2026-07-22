// Overview KPI value resolution (v0.9.170). The Service Overview must render
// even when the service-summary bundle (info) is absent — e.g. a service whose
// cluster attribute is unresolved (`${openshift.cluster}`) or missing and that
// therefore has no row in service_summary_5m for the window. The RED / latency
// span-metric batches are keyed on service.name (cluster-independent), so the
// headline numbers fall back to the live series when the summary is null, and
// only to 0 when neither exists — never NaN, never a blank panel.

// firstNum returns the first FINITE number in the preference list, else 0.
// Note: 0 is a valid value (0% errors, 0 req/s), so it is returned as-is;
// undefined / null / NaN / Infinity are skipped.
export function firstNum(...vals: Array<number | null | undefined>): number {
  for (const v of vals) {
    if (typeof v === 'number' && Number.isFinite(v)) return v;
  }
  return 0;
}
