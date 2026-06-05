// lib/otel — OpenTelemetry-native correlation layer (Phase 1 Task C).
//
// The cross-cutting data layer that stitches traces ↔ logs ↔ metrics ↔ profiles
// on ONE trace_id/span_id, plus the semantic-convention resolver every page
// reads attributes through. Pages A (metrics), B (traces), D (service/topology/
// logs) import from here rather than re-deriving joins or hard-coding semconv
// keys. Barrel export — import { useResource, useCorrelatedLogs, … } from '@/lib/otel'.

export * from './semconv';
export * from './links';
export * from './hooks';
