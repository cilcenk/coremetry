// v0.5.487 — OTel semantic-conventions → chart template registry.
//
// Picks sensible defaults for an arbitrary metric the moment the
// operator chooses it, the way Datadog/Honeycomb propose "p99 of
// http.server.request.duration grouped by route" without making
// you build it. The eight or so OTel semconv families cover the
// long tail; everything else falls back to type-based defaults
// (gauge=last, histogram=p99, counter=sum).
//
// Templates are a SUGGESTION not a forced state — the chip near
// the picker says "Template: HTTP latency · clear" so the
// operator can override without losing the picker.
//
// Single source of truth: match against the FULL metric name
// (case-insensitive) using a regex. First match wins, so order
// the array from most-specific to least-specific.

import type { MetricInfo } from './types';

export interface MetricTemplate {
  // Stable id — also the chip label.
  id: string;
  // Regex against metric name. Case-insensitive automatically.
  match: RegExp;
  // Defaults applied on auto-apply.
  agg: 'avg' | 'sum' | 'last' | 'min' | 'max' | 'p50' | 'p95' | 'p99';
  unit?: string;            // display hint; overrides MetricInfo.unit when set
  groupBy?: string[];       // suggested split keys (operator can clear)
  // SLO-style threshold suggestion. Used for the chart's threshold
  // line + the Sparkline `threshold` prop when applicable.
  threshold?: {
    value: number;
    cmp: '>' | '>=' | '<' | '<=';
    reason: string;         // shown in the chip tooltip
  };
  // Short one-liner that surfaces near the picker.
  description: string;
}

// Ordered most-specific → least-specific. First regex match wins.
//
// Naming reference:
//   OTel semconv: https://opentelemetry.io/docs/specs/semconv/general/metrics/
//   Prom community: *_count / *_total / *_seconds / *_bytes
const TEMPLATES: MetricTemplate[] = [
  // ── HTTP server latency — the dashboard's #1 metric.
  {
    id: 'HTTP server latency',
    match: /^http\.server\.(request\.)?duration$|^http_server_duration|^http_request_duration/i,
    agg: 'p99',
    unit: 'ms',
    groupBy: ['http.route', 'http.status_code'],
    threshold: { value: 1000, cmp: '>', reason: 'p99 > 1s = SLO breach territory' },
    description: 'p99 latency, split by route + status. Threshold: 1s.',
  },
  {
    id: 'HTTP client latency',
    match: /^http\.client\.(request\.)?duration$|^http_client_duration/i,
    agg: 'p99',
    unit: 'ms',
    groupBy: ['http.method', 'server.address'],
    threshold: { value: 2000, cmp: '>', reason: 'outbound p99 > 2s = downstream is slow' },
    description: 'p99 outbound latency, split by destination. Threshold: 2s.',
  },

  // ── DB / RPC latency.
  {
    id: 'DB query latency',
    match: /^db\.(client\.)?(operation\.)?duration$|^db_client_duration|^database_query_duration/i,
    agg: 'p99',
    unit: 'ms',
    groupBy: ['db.system', 'db.operation.name'],
    threshold: { value: 500, cmp: '>', reason: 'DB p99 > 500ms = query review' },
    description: 'p99 query latency, split by system + op. Threshold: 500ms.',
  },
  {
    id: 'RPC latency',
    match: /^rpc\.(server|client)\.duration$/i,
    agg: 'p99',
    unit: 'ms',
    groupBy: ['rpc.service', 'rpc.method'],
    threshold: { value: 1000, cmp: '>', reason: 'RPC p99 > 1s' },
    description: 'p99 RPC latency, split by service + method.',
  },
  {
    id: 'Messaging latency',
    match: /^messaging\.(publish|receive|process)\.duration$/i,
    agg: 'p99',
    unit: 'ms',
    groupBy: ['messaging.system', 'messaging.destination.name'],
    description: 'p99 messaging latency, split by destination.',
  },

  // ── Resource utilisation — CPU / memory / disk.
  {
    id: 'CPU utilisation',
    match: /^(system|process|container)\.cpu\.utilization$|cpu\.usage$|cpu_percent/i,
    agg: 'avg',
    unit: '%',
    groupBy: ['host.name'],
    threshold: { value: 0.85, cmp: '>', reason: 'CPU > 85% sustained = saturation' },
    description: 'Avg CPU, split by host. Threshold: 85%.',
  },
  {
    id: 'Memory usage',
    match: /^(system|process|container|jvm)\.memory\.(usage|used)$|memory_bytes|memory_used/i,
    agg: 'last',
    unit: 'bytes',
    groupBy: ['host.name'],
    description: 'Current memory, split by host.',
  },
  {
    id: 'Disk I/O',
    match: /^(system|process)\.disk\.io$|disk_bytes/i,
    agg: 'sum',
    unit: 'bytes',
    groupBy: ['system.device', 'direction'],
    description: 'Disk throughput, split by device + direction.',
  },
  {
    id: 'Network I/O',
    match: /^(system|process)\.network\.io$|network_bytes/i,
    agg: 'sum',
    unit: 'bytes',
    groupBy: ['network.interface.name', 'direction'],
    description: 'Network throughput, split by interface + direction.',
  },

  // ── JVM / GC — JVM apps lean heavy on these.
  {
    id: 'JVM GC pause',
    match: /^jvm\.gc\.(duration|pause)$/i,
    agg: 'p99',
    unit: 'ms',
    groupBy: ['jvm.gc.name'],
    threshold: { value: 200, cmp: '>', reason: 'GC pause > 200ms hurts p99 latency' },
    description: 'p99 GC pause, split by GC. Threshold: 200ms.',
  },
  {
    id: 'JVM threads',
    match: /^jvm\.thread(s)?\.count$/i,
    agg: 'last',
    description: 'Live thread count.',
  },

  // ── DB pool — JDBC/HikariCP signal.
  {
    id: 'DB pool usage',
    match: /^db\.client\.connections\.(usage|active|idle|max)$|jdbc_pool|hikaricp/i,
    agg: 'last',
    groupBy: ['pool.name', 'state'],
    description: 'Connection-pool state, split by pool.',
  },

  // ── Messaging lag — Kafka/RabbitMQ.
  {
    id: 'Consumer lag',
    match: /^messaging\.(kafka\.)?consumer\.lag$|kafka_lag|consumer_lag/i,
    agg: 'max',
    groupBy: ['messaging.destination.name', 'messaging.kafka.consumer.group'],
    threshold: { value: 10_000, cmp: '>', reason: 'lag > 10k = consumer behind' },
    description: 'Max consumer lag, split by topic + group. Threshold: 10k.',
  },

  // ── Error counters — anything *.errors / *_errors_total.
  {
    id: 'Error counter',
    match: /\.errors?$|_errors?(_total)?$|\.error\.count$/i,
    agg: 'sum',
    groupBy: ['error.type'],
    threshold: { value: 0, cmp: '>', reason: 'any error in window = investigate' },
    description: 'Error rate, split by type. Threshold: > 0.',
  },

  // ── Request / event counters — *_count / *_total.
  {
    id: 'Request counter',
    match: /\.requests?$|_requests?(_total)?$|\.request\.count$|\.calls?(_total)?$/i,
    agg: 'sum',
    groupBy: ['service.name'],
    description: 'Request rate, split by service.',
  },
];

// Map MetricInfo.type → reasonable fallback when no template matches.
// OTel types: counter, updowncounter, gauge, histogram, summary.
function fallbackForType(type: string): { agg: MetricTemplate['agg']; description: string } {
  switch ((type || '').toLowerCase()) {
    case 'histogram':    return { agg: 'p99',  description: 'Histogram → p99 (override if you want avg/p50).' };
    case 'summary':      return { agg: 'p99',  description: 'Summary → p99.' };
    case 'counter':      return { agg: 'sum',  description: 'Counter → sum over window.' };
    case 'updowncounter':
    case 'gauge':        return { agg: 'last', description: 'Gauge → last value.' };
    default:             return { agg: 'avg',  description: '' };
  }
}

// Classify a metric and return its template, or null if neither
// the regex registry nor the OTel-type fallback applies (rare —
// usually means the backend didn't report a type).
export function classifyMetric(info: MetricInfo): MetricTemplate | null {
  if (!info?.name) return null;
  const hit = TEMPLATES.find(t => t.match.test(info.name));
  if (hit) return hit;
  const fb = fallbackForType(info.type);
  if (!fb.agg) return null;
  return {
    id: `OTel ${info.type || 'metric'}`,
    match: /.*/,
    agg: fb.agg,
    unit: info.unit || undefined,
    description: fb.description,
  };
}

// Export the registry so a future "Templates" admin page can list them.
export const METRIC_TEMPLATES = TEMPLATES;
