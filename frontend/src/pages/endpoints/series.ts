import { timeRangeToNs } from '@/lib/utils';
import type { EndpointRow, SpanMetricSeries, TimeRange } from '@/lib/types';

// series.ts — the /endpoint page's RED chart data (v0.9.839; extracted
// from the retired sparkline modal in Endpoints.tsx, v0.5.387).
//
// NO FETCH AT ALL, and specifically NOT /api/spans/metric-batch: an
// endpoint's identity on this surface is http.route, and metric-batch
// keys on span NAME. The series are the table row's OWN sparkline
// arrays, which the MV produced beside the RED numbers on the same row
// — so the charts and the score strip can never disagree.

/**
 * bucketsToSeries converts the row's three 30-bucket sparkline arrays
 * into SpanMetricSeries shape so the chart can plot them on a time
 * axis. The backend doesn't ship per-bucket timestamps (the payload
 * size is bounded that way) so we reconstruct them client-side from the
 * selected range — bucket i sits at the midpoint of its slice of the
 * window, matching the backend's `intDiv(time - from, bucketNs)`.
 *
 * Calls are divided by the bucket width to become real rps, and errors
 * are divided by calls to become an error %, because a bare per-bucket
 * counter ("12500") answers no question on its own. The tile's headline
 * stats keep reading the raw counters — only the axis and tooltip turn
 * into rates (the Datadog dialect).
 */
export function bucketsToSeries(row: EndpointRow, range: TimeRange): {
  calls: SpanMetricSeries[]; errors: SpanMetricSeries[]; p99: SpanMetricSeries[];
} {
  const { from, to } = timeRangeToNs(range);
  const calls = row.sparkline ?? [];
  const errs = row.errorsSparkline ?? [];
  const p99s = row.p99Sparkline ?? [];
  const n = Math.max(calls.length, errs.length, p99s.length);
  if (n === 0 || to <= from) {
    return { calls: [], errors: [], p99: [] };
  }
  const bucketNs = (to - from) / n;
  const bucketSec = bucketNs / 1e9;
  const timeAtBucket = (i: number) => from + bucketNs * i + bucketNs / 2;
  const rpsPoints = calls.map((v, i) => ({
    time: timeAtBucket(i),
    value: bucketSec > 0 ? v / bucketSec : v,
  }));
  const errPctPoints = errs.map((v, i) => ({
    time: timeAtBucket(i),
    value: (calls[i] ?? 0) > 0 ? (v / calls[i]) * 100 : 0,
  }));
  const p99Points = p99s.map((v, i) => ({ time: timeAtBucket(i), value: v }));
  return {
    calls: calls.length ? [{ groupKey: ['calls'], points: rpsPoints }] : [],
    errors: errs.length ? [{ groupKey: ['errors'], points: errPctPoints }] : [],
    p99: p99s.length ? [{ groupKey: ['p99 ms'], points: p99Points }] : [],
  };
}
