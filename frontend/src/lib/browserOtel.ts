// OpenTelemetry browser SDK wiring — Coremetry instrumenting
// itself. Spans flow to /v1/traces (the OTLP/HTTP endpoint the
// Go backend is already listening on) so the operator can see
// a real end-to-end trace of their own UI clicks landing on
// the same backend they're investigating.
//
// Auto-instrumentations registered:
//   - DocumentLoadInstrumentation — page-load timings: DNS,
//     TCP, TLS, response, FCP, LCP. One trace per navigation.
//   - FetchInstrumentation — every fetch() call gets its own
//     span with method, status, duration. Outgoing headers
//     get the W3C traceparent so the backend (Go otelhttp)
//     stitches into the SAME trace.
//   - UserInteractionInstrumentation — click + keypress events
//     produce spans that include any descendant fetch spans,
//     so a "click on services nav → /api/services" lights up
//     as a parent click span with a fetch child.
//
// Disable knobs:
//   - VITE_OTEL_DISABLE=1 → skip the whole init. For
//     environments where the operator doesn't want their UI
//     traffic mixed into the trace store.
//   - VITE_OTEL_ENDPOINT — override the OTLP endpoint (default:
//     same-origin /v1/traces). For deployments where the UI
//     and backend are different hosts.

import { trace, context, type Tracer } from '@opentelemetry/api';
import { resourceFromAttributes } from '@opentelemetry/resources';
import {
  WebTracerProvider,
  BatchSpanProcessor,
} from '@opentelemetry/sdk-trace-web';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http';
import { ZoneContextManager } from '@opentelemetry/context-zone';
import { registerInstrumentations } from '@opentelemetry/instrumentation';
import { FetchInstrumentation } from '@opentelemetry/instrumentation-fetch';
import { DocumentLoadInstrumentation } from '@opentelemetry/instrumentation-document-load';
import { UserInteractionInstrumentation } from '@opentelemetry/instrumentation-user-interaction';
import {
  ATTR_SERVICE_NAME,
  ATTR_SERVICE_VERSION,
  ATTR_DEPLOYMENT_ENVIRONMENT_NAME,
} from '@opentelemetry/semantic-conventions/incubating';

let tracer: Tracer | null = null;

export function initOtel(): void {
  // Hard kill switch via env var. Read at module init —
  // changing it requires a rebuild, which is fine since OTel
  // setup is one-time-per-process.
  if (import.meta.env.VITE_OTEL_DISABLE === '1') return;

  const endpoint = import.meta.env.VITE_OTEL_ENDPOINT
    ?? `${window.location.origin}/v1/traces`;

  const resource = resourceFromAttributes({
    [ATTR_SERVICE_NAME]: 'coremetry-frontend',
    // VITE_APP_VERSION baked at build time via the existing
    // version-stamp pipeline; falls back to 'dev' for local
    // builds without it.
    [ATTR_SERVICE_VERSION]: import.meta.env.VITE_APP_VERSION ?? 'dev',
    [ATTR_DEPLOYMENT_ENVIRONMENT_NAME]: import.meta.env.MODE,
  });

  // Span processor uses BatchSpanProcessor — accumulates spans
  // and flushes every 5s or 512 spans, whichever comes first.
  // Keeps network noise minimal even on a click-heavy page.
  const provider = new WebTracerProvider({
    resource,
    spanProcessors: [
      new BatchSpanProcessor(new OTLPTraceExporter({
        url: endpoint,
        // The OTLP/HTTP endpoint accepts cookies via the same-
        // origin auth check; a cross-origin endpoint would
        // need explicit headers + CORS allow.
      }), {
        maxQueueSize: 1024,
        maxExportBatchSize: 512,
        scheduledDelayMillis: 5000,
      }),
    ],
  });

  // ZoneContextManager keeps the active span across async
  // boundaries (Promise, setTimeout, fetch) so
  // FetchInstrumentation's fetch span lands UNDER the
  // user-interaction click span instead of as a top-level
  // sibling. Without this every fetch is orphaned.
  provider.register({
    contextManager: new ZoneContextManager(),
  });

  registerInstrumentations({
    instrumentations: [
      new DocumentLoadInstrumentation(),
      new FetchInstrumentation({
        // We send /v1/traces ourselves — instrumenting that
        // endpoint creates a feedback loop where every
        // exported span generates another fetch span.
        ignoreUrls: [/\/v1\/traces$/],
        // Enable W3C trace-context propagation on outgoing
        // fetches. The Go backend (otelhttp) already accepts
        // traceparent and continues the trace, so a
        // /api/services call lights up under the same trace
        // ID in the Coremetry trace explorer.
        propagateTraceHeaderCorsUrls: [
          new RegExp(window.location.origin),
        ],
      }),
      new UserInteractionInstrumentation({
        eventNames: ['click', 'submit'],
      }),
    ],
  });

  tracer = trace.getTracer('coremetry-frontend');

  // Console breadcrumb on init — visible in DevTools so the
  // operator can confirm the SDK booted without diving into
  // the network tab.
  // eslint-disable-next-line no-console
  console.info('[otel] coremetry-frontend instrumented → ' + endpoint);
}

// useTracer is the convenience escape hatch for components
// that want to wrap their own work in a custom span (heavy
// chart render, JSON parse of a big payload, etc.). Lazy
// because the SDK might be disabled — falls back to a no-op
// tracer that returns inert spans.
export function getTracer(): Tracer {
  if (!tracer) tracer = trace.getTracer('coremetry-frontend');
  return tracer;
}

// withSpan wraps any sync/async function in a span. Errors
// flow through; the span is end()'d in a finally block so
// thrown exceptions don't leak open spans.
export async function withSpan<T>(
  name: string,
  fn: () => Promise<T> | T,
  attrs?: Record<string, string | number | boolean>,
): Promise<T> {
  const span = getTracer().startSpan(name);
  if (attrs) span.setAttributes(attrs);
  try {
    return await context.with(trace.setSpan(context.active(), span), fn);
  } catch (err) {
    if (err instanceof Error) {
      span.recordException(err);
      span.setStatus({ code: 2, message: err.message }); // 2 = ERROR
    }
    throw err;
  } finally {
    span.end();
  }
}
