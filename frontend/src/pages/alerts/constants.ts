import type { AlertRule } from '@/lib/types';

// Shared constants + types for the Alerts page. Split out of the
// Alerts.tsx monolith (v0.8.252 refactor) verbatim.

// User-saved presets (v0.5.157) — same shape as the built-in
// TEMPLATES but persisted server-side via saved_views (page='
// alert-template'). The queryString column holds the draft as
// JSON; reusing the existing saved-views table avoids inventing
// a new persistence path.
export type UserPreset = {
  id: string;
  name: string;
  shared: boolean;
  draft: Partial<AlertRule>;
};

export const METRICS = [
  { v: 'error_rate',   label: 'Error rate (%)' },
  { v: 'p99_ms',       label: 'P99 latency (ms)' },
  { v: 'p95_ms',       label: 'P95 latency (ms)' },
  { v: 'p50_ms',       label: 'P50 latency (ms)' },
  { v: 'avg_ms',       label: 'Avg latency (ms)' },
  { v: 'request_rate', label: 'Request rate (/s)' },
  { v: 'error_count',  label: 'Error count' },
];

export const COMPARATORS = ['>', '>=', '<', '<='];
export const SEVERITIES = ['info', 'warning', 'critical'];

export const WINDOWS = [
  { v: 60,    label: '1 min' },
  { v: 300,   label: '5 min' },
  { v: 600,   label: '10 min' },
  { v: 1800,  label: '30 min' },
  { v: 3600,  label: '1 hour' },
];

// Empty draft used for both "+ New" and as the reset value after save.
// Kept at module scope so the create/edit reset paths share one source.
export const emptyDraft: Partial<AlertRule> = {
  name: '', service: '', metric: 'error_rate', comparator: '>',
  threshold: 5, windowSec: 300, severity: 'warning', enabled: true,
};

// Alert rule templates — Datadog "monitor templates" pattern.
// Each is a one-click pre-fill of the new-rule form for a
// scenario operators wire up over and over. Tweaks (specific
// service, custom threshold) happen after the operator clicks
// the template; the form stays editable.
//
// Naming convention: `{Severity} · {Scenario}` so the picker
// reads as "what kind of problem is this watching for".
export const TEMPLATES: { id: string; label: string; description: string; draft: Partial<AlertRule> }[] = [
  {
    id: 'tpl-high-error-rate',
    label: 'High error rate (>5%)',
    description: 'Fires when a service\'s span error rate stays above 5% for 5 minutes — the canonical "something is broken" alarm.',
    draft: { ...emptyDraft, name: 'High error rate', metric: 'error_rate', comparator: '>', threshold: 5,  windowSec: 300, severity: 'critical' },
  },
  {
    id: 'tpl-warn-error-rate',
    label: 'Warning error rate (>1%)',
    description: 'Lower-severity counterpart — early warning before the critical alarm trips.',
    draft: { ...emptyDraft, name: 'Elevated error rate', metric: 'error_rate', comparator: '>', threshold: 1, windowSec: 600, severity: 'warning' },
  },
  {
    id: 'tpl-slow-p99',
    label: 'Slow P99 (>1s)',
    description: 'Tail-latency regression catcher. Most user-visible slowness lives in P99, not the median.',
    draft: { ...emptyDraft, name: 'Slow P99 latency', metric: 'p99_ms', comparator: '>', threshold: 1000, windowSec: 600, severity: 'warning' },
  },
  {
    id: 'tpl-very-slow-p99',
    label: 'Very slow P99 (>5s)',
    description: 'Critical latency band — typical "user is staring at a spinner" threshold.',
    draft: { ...emptyDraft, name: 'Critical P99 latency', metric: 'p99_ms', comparator: '>', threshold: 5000, windowSec: 300, severity: 'critical' },
  },
  {
    id: 'tpl-traffic-drop',
    label: 'Service disappeared (RPS = 0)',
    description: 'Triggers when request rate drops to zero — useful for detecting a crashed service even when no errors are emitted.',
    draft: { ...emptyDraft, name: 'Service disappeared', metric: 'request_rate', comparator: '<', threshold: 0.01, windowSec: 300, severity: 'critical' },
  },
  {
    id: 'tpl-error-burst',
    label: 'Error burst (>100 errors in 5m)',
    description: 'Absolute count threshold — catches a sudden spike independent of traffic ratio.',
    draft: { ...emptyDraft, name: 'Error count burst', metric: 'error_count', comparator: '>', threshold: 100, windowSec: 300, severity: 'warning' },
  },
];
