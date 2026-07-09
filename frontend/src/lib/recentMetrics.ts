// recentMetrics (v0.8.417, Data-Explorer parity DE2) — client-side
// recently-picked metric MRU, the metric-catalogue sibling of
// recentServices (v0.7.89). Dynatrace's Data Explorer resurfaces the
// operator's working set of metrics at the top of the picker; at a
// 10k-metric install that rotation (an operator tunes 3-4 metrics
// during an investigation) otherwise costs a re-search per pick.
//
// Stores the full MetricInfo (not just the name) so re-picking wires
// unit/type back into the query row without a catalogue round-trip.
// localStorage-only per-browser convenience state, same as
// recentServices — shared/team state lives in saved_views.

import { getItem, setItem, STORAGE_KEYS } from './storage';
import type { MetricInfo } from './types';

const RECENT_KEY = STORAGE_KEYS.recentMetrics;
const RECENT_CAP = 8;

function isMetricInfo(x: unknown): x is MetricInfo {
  if (typeof x !== 'object' || x === null) return false;
  const m = x as Record<string, unknown>;
  return typeof m.name === 'string' && m.name !== '' &&
    typeof m.description === 'string' &&
    typeof m.unit === 'string' &&
    typeof m.type === 'string';
}

function read(): MetricInfo[] {
  const v = getItem<unknown>(RECENT_KEY, []);
  return Array.isArray(v) ? v.filter(isMetricInfo) : [];
}

// recordMetricPick pushes a metric to the front of the MRU (deduped by
// name — a re-pick refreshes unit/description too), capped at 8.
// Call it from the picker's onPick. No-op for empty names.
export function recordMetricPick(m: MetricInfo): void {
  if (!m?.name) return;
  const next = [m, ...read().filter(x => x.name !== m.name)].slice(0, RECENT_CAP);
  setItem(RECENT_KEY, next);
}

export function getRecentMetrics(): MetricInfo[] {
  return read();
}
