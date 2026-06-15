// hooks.ts — the OTel correlation hooks (Phase 1 Task C).
//
// The product's differentiator is ONE trace_id/span_id stitching traces ↔ logs
// ↔ metrics ↔ profiles. These typed hooks are the seam: pages A/B/D import them
// instead of re-deriving the joins. Each is read-only, react-query-cached, and
// gracefully disabled when its key input is missing.

import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { SpanRow, LogRow, TraceRow } from '@/lib/types';
import { resolveResource, scopeKey, type ResourceIdentity } from './semconv';
import { extractSpanLinks, spanExceptions, type SpanLink, type ExceptionInfo } from './links';

// useResource — memoised resource identity for a resource-attribute map (a
// span's resourceAttributes, or a service's). Pure; no fetch.
export function useResource(attrs: Record<string, string> | undefined | null): ResourceIdentity {
  return useMemo(() => resolveResource(attrs), [attrs]);
}

// useSpanLinks — the span's links (other-trace pointers) + its exception
// events, memoised. The hook a Trace-detail span panel calls to render the
// "Links" + "Exceptions" sub-panels.
export function useSpanLinks(span: SpanRow | undefined | null): { links: SpanLink[]; exceptions: ExceptionInfo[] } {
  return useMemo(() => ({
    links: extractSpanLinks(span),
    exceptions: spanExceptions(span),
  }), [span]);
}

// TRACE_LOG_WINDOW_BUFFER_NS — ±1 minute (in Unix nanoseconds) padding around a
// trace's own span times when bounding the trace→logs ES lookup. Logs correlated
// to a trace are temporally local to it, so the search window is anchored to the
// TRACE's span times — NOT now() (that was the v0.5.223 bug: a now()-relative 24h
// cap hid old traces). The buffer absorbs clock skew between hosts + logs emitted
// just before/after the span boundaries.
export const TRACE_LOG_WINDOW_BUFFER_NS = 60_000_000_000;

// traceLogWindow — pure: derive the {from, to} (Unix ns) lookup window for a
// trace's logs from its spans. from = min(startTime) - buffer, to =
// max(endTime) + buffer. Returns null when no usable span times exist so the
// caller can fall through to an unbounded lookup rather than a degenerate window.
// span.startTime / span.endTime are Unix NANOSECONDS in this codebase, matching
// LogsParams.from/to (parseTime → time.Unix(0, ns) server-side).
export function traceLogWindow(
  spans: Array<{ startTime?: number; endTime?: number }> | null | undefined,
  buffer: number = TRACE_LOG_WINDOW_BUFFER_NS,
): { from: number; to: number } | null {
  if (!spans || spans.length === 0) return null;
  let min = Infinity;
  let max = -Infinity;
  for (const s of spans) {
    if (typeof s.startTime === 'number' && s.startTime > 0 && s.startTime < min) min = s.startTime;
    if (typeof s.endTime === 'number' && s.endTime > 0 && s.endTime > max) max = s.endTime;
  }
  if (min === Infinity || max === -Infinity) return null;
  return { from: min - buffer, to: max + buffer };
}

// useCorrelatedLogs — the traces→logs join: every log line sharing this
// trace_id (optionally narrowed to one span_id). Backs the Trace-detail "Logs"
// sub-tab and the log-row "trace →" round-trip. Disabled until a traceId
// exists; 30s cache matches the logs surface.
//
// from/to (Unix ns) bound the underlying ES search to the trace's own time
// window ±1min (v0.8.180) instead of a full-index scan by trace_id. They are
// STABLE per trace (derived from the trace's span times), so they belong in the
// query key for cache correctness but don't cause refetch churn.
export function useCorrelatedLogs(
  traceId: string | undefined,
  spanId?: string,
  opts?: { limit?: number; from?: number; to?: number },
) {
  const limit = opts?.limit ?? 200;
  const from = opts?.from;
  const to = opts?.to;
  return useQuery<LogRow[]>({
    queryKey: ['otel', 'correlated-logs', traceId ?? '', spanId ?? '', limit, from ?? 0, to ?? 0],
    queryFn: async () => {
      const res = await api.logs({ traceId: traceId!, spanId, limit, from, to });
      return res?.logs ?? [];
    },
    enabled: !!traceId,
    staleTime: 30_000,
  });
}

// useExemplars — the metrics→traces jump: representative traces for a
// (service, window) cell, optionally errors-only. OTLP metric exemplars aren't
// on the wire yet, so this surfaces the matching traces (the same set a metric
// data point would exemplar) — when the backend starts emitting true exemplars
// this hook swaps its source without changing a single caller. Disabled until
// the window is set.
export function useExemplars(params: {
  service?: string;
  fromNs?: number;
  toNs?: number;
  errorsOnly?: boolean;
  limit?: number;
}) {
  const { service, fromNs, toNs, errorsOnly, limit = 20 } = params;
  return useQuery<TraceRow[]>({
    queryKey: ['otel', 'exemplars', service ?? '', fromNs ?? 0, toNs ?? 0, !!errorsOnly, limit],
    queryFn: async () => {
      const res = await api.traces({
        service,
        from: fromNs,
        to: toNs,
        hasError: errorsOnly || undefined,
        limit,
      });
      return res?.traces ?? [];
    },
    enabled: !!(fromNs && toNs),
    staleTime: 30_000,
  });
}

// useScopeGroups — group a span set by instrumentation scope (otel.scope.name)
// so the Trace-detail / Explore views can show "which library emitted what".
export function useScopeGroups(spans: SpanRow[] | undefined): Array<{ scope: string; spans: SpanRow[] }> {
  return useMemo(() => {
    const groups = new Map<string, SpanRow[]>();
    for (const s of spans ?? []) {
      const key = scopeKey(s.scopeName);
      const g = groups.get(key);
      if (g) g.push(s);
      else groups.set(key, [s]);
    }
    return Array.from(groups, ([scope, spans]) => ({ scope, spans }))
      .sort((a, b) => b.spans.length - a.spans.length);
  }, [spans]);
}
