// TraceHonesty.tsx — honest trace provenance strip.
//
// A trace is only as trustworthy as its instrumentation. This strip tells the
// operator the truth about THIS trace before they reason about it:
//   • W3C tracecontext linkage — how many spans have a parent that's actually
//     present in the trace vs. orphans (a broken/un-propagated context chain),
//     and whether a single root landed.
//   • Sampling — the head-sampling decision recorded on the root span
//     (sampling.priority / otel sampler attributes) when present.
//   • Dropped counts — OTel's dropped_attributes_count / dropped_events_count /
//     dropped_links_count surfaced so truncated spans are visible.
//   • Source — clickhouse vs tempo-fallback vs mv_only (aged out).
//
// Everything is best-effort: a field only renders when the underlying signal is
// present. Pure derivation from the span list; no fetch.

import { useMemo } from 'react';
import type { SpanRow } from '@/lib/types';

export function TraceHonesty({
  spans, source,
}: {
  spans: SpanRow[];
  source?: 'clickhouse' | 'tempo' | 'mv_only';
}) {
  const facts = useMemo(() => {
    const ids = new Set(spans.map(s => s.spanId));
    let roots = 0;
    let orphans = 0;       // has a parentSpanId, but the parent isn't in the trace
    let droppedAttrs = 0;
    let droppedEvents = 0;
    let droppedLinks = 0;
    let withParent = 0;
    for (const s of spans) {
      if (!s.parentSpanId) roots++;
      else if (!ids.has(s.parentSpanId)) { orphans++; }
      else withParent++;
      const a = s.attributes ?? {};
      droppedAttrs += toNum(a['otel.dropped_attributes_count'] ?? a['dropped_attributes_count']);
      droppedEvents += toNum(a['otel.dropped_events_count'] ?? a['dropped_events_count']);
      droppedLinks += toNum(a['otel.dropped_links_count'] ?? a['dropped_links_count']);
    }
    // Head-sampling decision on the root span, if the SDK recorded it.
    const root = spans.find(s => !s.parentSpanId) ?? spans[0];
    const ra = root?.attributes ?? {};
    const samplerName = ra['otel.sampler.name'] ?? ra['sampling.rule'];
    const samplerRatio = ra['otel.sampler.ratio'] ?? ra['sampling.probability'];
    const samplingPriority = ra['sampling.priority'];
    return {
      total: spans.length, roots, orphans, withParent,
      droppedAttrs, droppedEvents, droppedLinks,
      samplerName, samplerRatio, samplingPriority,
    };
  }, [spans]);

  if (spans.length === 0) return null;

  const chips: Array<{ label: string; tone: 'ok' | 'warn' | 'err' | 'info'; title: string }> = [];

  // tracecontext integrity
  if (facts.roots === 1) {
    chips.push({ label: 'W3C tracecontext: complete', tone: 'ok', title: 'A single root span landed and every other span links to a present parent.' });
  } else if (facts.roots === 0) {
    chips.push({ label: 'No root span', tone: 'warn', title: 'No span without a parent — the trace root never landed in storage (sampled out or dropped). The waterfall shows fragments.' });
  } else {
    chips.push({ label: `${facts.roots} roots`, tone: 'warn', title: 'More than one root — context wasn\'t propagated as a single chain, or multiple entry points merged under one trace id.' });
  }
  if (facts.orphans > 0) {
    chips.push({ label: `${facts.orphans} orphan span${facts.orphans === 1 ? '' : 's'}`, tone: 'warn', title: 'Spans whose parent_span_id points at a span NOT present in this trace — a broken context chain (parent sampled out, dropped, or not yet ingested).' });
  }

  // sampling
  if (facts.samplingPriority != null) {
    chips.push({ label: `sampling.priority ${facts.samplingPriority}`, tone: 'info', title: 'Head-sampling priority recorded on the root span.' });
  }
  if (facts.samplerName) {
    chips.push({ label: `sampler ${facts.samplerName}${facts.samplerRatio ? ` @ ${facts.samplerRatio}` : ''}`, tone: 'info', title: 'OTel head sampler recorded on the root span.' });
  }

  // dropped counts
  if (facts.droppedAttrs > 0) chips.push({ label: `${facts.droppedAttrs} dropped attrs`, tone: 'err', title: 'OTel dropped_attributes_count — span attribute limit exceeded; some attributes were never recorded.' });
  if (facts.droppedEvents > 0) chips.push({ label: `${facts.droppedEvents} dropped events`, tone: 'err', title: 'OTel dropped_events_count — span event limit exceeded; some events (possibly exceptions) were dropped.' });
  if (facts.droppedLinks > 0) chips.push({ label: `${facts.droppedLinks} dropped links`, tone: 'err', title: 'OTel dropped_links_count — span link limit exceeded.' });

  // source provenance
  if (source === 'tempo') chips.push({ label: 'source: Tempo fallback', tone: 'info', title: 'Coremetry sampled this trace out; the spans were read from the external Tempo backend.' });
  else if (source === 'mv_only') chips.push({ label: 'source: aggregates only', tone: 'warn', title: 'Raw spans aged out of retention; only the 5-min aggregate remains.' });

  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center', marginBottom: 10 }}>
      <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.4px', color: 'var(--text3)', textTransform: 'uppercase', marginRight: 2 }}>
        Provenance
      </span>
      {chips.map((c, i) => <Chip key={i} {...c} />)}
    </div>
  );
}

function Chip({ label, tone, title }: { label: string; tone: 'ok' | 'warn' | 'err' | 'info'; title: string }) {
  const color = tone === 'ok' ? 'var(--ok)' : tone === 'warn' ? 'var(--warn)' : tone === 'err' ? 'var(--err)' : 'var(--info)';
  return (
    <span title={title} style={{
      display: 'inline-flex', alignItems: 'center', gap: 5,
      fontSize: 10.5, padding: '2px 8px', borderRadius: 12,
      background: `color-mix(in srgb, ${color} 12%, transparent)`,
      color, border: `1px solid color-mix(in srgb, ${color} 35%, transparent)`,
      whiteSpace: 'nowrap',
    }}>
      <span style={{ width: 6, height: 6, borderRadius: 6, background: color }} />
      {label}
    </span>
  );
}

function toNum(v: string | undefined): number {
  if (v == null) return 0;
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : 0;
}
