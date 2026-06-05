// links.ts — span links + exception model (Phase 1 Task C).
//
// The OTel span model carries LINKS (pointers to causally-related spans in
// OTHER traces — fan-in/fan-out, batch, retry) and span EVENTS (timestamped
// occurrences, most importantly `exception` with exception.type/message/
// stacktrace). SpanRow exposes events natively; span links aren't a typed
// column yet, so we extract them from the conventional attribute encoding when
// present and degrade to empty otherwise — callers (Trace detail) render a
// "Links (n)" affordance only when n>0.

import type { SpanRow, SpanEvent } from '@/lib/types';

export interface SpanLink {
  traceId: string;
  spanId: string;
  attributes: Record<string, string>;
}

// extractSpanLinks reads links from a span. Two sources, in order:
//   1. A JSON-encoded `otel.links` / `links` attribute (some exporters flatten
//      links into a single attribute at ingest).
//   2. Indexed `otel.link.<i>.trace_id` / `.span_id` attribute triples.
// Returns [] when neither is present — the common case today.
export function extractSpanLinks(span: SpanRow | undefined | null): SpanLink[] {
  if (!span) return [];
  const a = span.attributes ?? {};

  const blob = a['otel.links'] ?? a['links'];
  if (blob) {
    try {
      const parsed = JSON.parse(blob) as Array<{ traceId?: string; trace_id?: string; spanId?: string; span_id?: string; attributes?: Record<string, string> }>;
      const out = parsed
        .map(l => ({
          traceId: l.traceId ?? l.trace_id ?? '',
          spanId: l.spanId ?? l.span_id ?? '',
          attributes: l.attributes ?? {},
        }))
        .filter(l => l.traceId);
      if (out.length) return out;
    } catch { /* not JSON — fall through to indexed form */ }
  }

  const links: SpanLink[] = [];
  for (let i = 0; ; i++) {
    const tid = a[`otel.link.${i}.trace_id`];
    if (!tid) break;
    links.push({
      traceId: tid,
      spanId: a[`otel.link.${i}.span_id`] ?? '',
      attributes: {},
    });
  }
  return links;
}

export interface ExceptionInfo {
  type?: string;
  message?: string;
  stacktrace?: string;
  timeNano: number;
}

// spanExceptions pulls the OTel `exception` span events out as typed records —
// exception.type / exception.message / exception.stacktrace. Drives the
// stack-trace panel + the red error marker on the waterfall.
export function spanExceptions(span: SpanRow | undefined | null): ExceptionInfo[] {
  const events: SpanEvent[] = span?.events ?? [];
  return events
    .filter(e => e.name === 'exception')
    .map(e => ({
      type: e.attributes['exception.type'],
      message: e.attributes['exception.message'],
      stacktrace: e.attributes['exception.stacktrace'],
      timeNano: e.timeNano,
    }));
}

// hasError is the one place that decides "does this span represent a failure" —
// an error status OR a recorded exception event. Keeps the waterfall tint and
// the error-count honest even when status was left unset but an exception fired.
export function spanHasError(span: SpanRow): boolean {
  const s = (span.statusCode ?? '').toLowerCase();
  if (s === 'error' || s === '2') return true;
  return (span.events ?? []).some(e => e.name === 'exception');
}
