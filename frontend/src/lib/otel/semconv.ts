// semconv.ts — OpenTelemetry semantic-convention resolver (Phase 1 Task C).
//
// Coremetry is OTel-native: every column maps back to a semconv attribute (see
// /otel-conventions). This module is the ONE place that knows the convention
// keys, so pages don't hand-roll `attrs['k8s.pod.name'] ?? attrs['k8s_pod_name']`
// all over. It resolves a resource-attribute map into a typed identity (with
// the coalesce chains the backend uses) and classifies any attribute key into
// its semconv family for grouping / faceting.

// The "critical 5" resource attributes every ingested signal should carry
// (CLAUDE.md / otel-conventions). Used to flag under-instrumented services.
export const CRITICAL_RESOURCE_ATTRS = [
  'service.name',
  'deployment.environment',
  'service.version',
  'host.name',
  'service.instance.id',
] as const;

export interface ResourceIdentity {
  serviceName: string;
  serviceNamespace?: string;
  serviceVersion?: string;
  serviceInstanceId?: string;
  deploymentEnvironment?: string;
  hostName?: string;
  cluster?: string;
  k8s: { namespace?: string; pod?: string; node?: string; deployment?: string; container?: string };
  cloud: { provider?: string; region?: string; availabilityZone?: string; accountId?: string };
  runtime: { name?: string; version?: string; description?: string };
  telemetrySdk: { name?: string; language?: string; version?: string };
  // Which of the critical-5 are present — drives an "under-instrumented" hint.
  missingCritical: string[];
}

function first(attrs: Record<string, string>, ...keys: string[]): string | undefined {
  for (const k of keys) {
    const v = attrs[k];
    if (v != null && v !== '') return v;
  }
  return undefined;
}

// resolveResource extracts the canonical identity from a resource-attribute
// map. Uses the same coalesce chains as the backend (e.g. k8s/openshift cluster
// name) so the UI never disagrees with a query.
export function resolveResource(attrs: Record<string, string> | undefined | null): ResourceIdentity {
  const a = attrs ?? {};
  const id: ResourceIdentity = {
    serviceName: first(a, 'service.name') ?? '',
    serviceNamespace: first(a, 'service.namespace'),
    serviceVersion: first(a, 'service.version'),
    serviceInstanceId: first(a, 'service.instance.id'),
    deploymentEnvironment: first(a, 'deployment.environment', 'deployment.environment.name'),
    hostName: first(a, 'host.name'),
    cluster: first(a, 'k8s.cluster.name', 'openshift.cluster.name', 'cluster'),
    k8s: {
      namespace: first(a, 'k8s.namespace.name'),
      pod: first(a, 'k8s.pod.name'),
      node: first(a, 'k8s.node.name'),
      deployment: first(a, 'k8s.deployment.name'),
      container: first(a, 'k8s.container.name', 'container.name'),
    },
    cloud: {
      provider: first(a, 'cloud.provider'),
      region: first(a, 'cloud.region'),
      availabilityZone: first(a, 'cloud.availability_zone'),
      accountId: first(a, 'cloud.account.id'),
    },
    runtime: {
      name: first(a, 'process.runtime.name'),
      version: first(a, 'process.runtime.version'),
      description: first(a, 'process.runtime.description'),
    },
    telemetrySdk: {
      name: first(a, 'telemetry.sdk.name'),
      language: first(a, 'telemetry.sdk.language'),
      version: first(a, 'telemetry.sdk.version'),
    },
    missingCritical: [],
  };
  id.missingCritical = CRITICAL_RESOURCE_ATTRS.filter(k => !a[k]);
  return id;
}

// ── Attribute family classification ──────────────────────────────────────────

export type AttrFamily =
  | 'service' | 'k8s' | 'cloud' | 'host' | 'process' | 'runtime' | 'telemetry'
  | 'deployment' | 'container' | 'os'
  | 'http' | 'rpc' | 'db' | 'messaging' | 'network' | 'exception' | 'gen_ai'
  | 'code' | 'other';

// Ordered longest-prefix-first so 'process.runtime' beats 'process'.
const FAMILY_PREFIXES: Array<[string, AttrFamily]> = [
  ['process.runtime.', 'runtime'],
  ['telemetry.sdk.', 'telemetry'], ['telemetry.', 'telemetry'],
  ['service.', 'service'],
  ['k8s.', 'k8s'],
  ['cloud.', 'cloud'],
  ['host.', 'host'],
  ['process.', 'process'],
  ['deployment.', 'deployment'],
  ['container.', 'container'],
  ['os.', 'os'],
  ['http.', 'http'],
  ['rpc.', 'rpc'],
  ['db.', 'db'],
  ['messaging.', 'messaging'],
  ['net.', 'network'], ['network.', 'network'], ['peer.', 'network'],
  ['exception.', 'exception'],
  ['gen_ai.', 'gen_ai'],
  ['code.', 'code'],
];

export function attrFamily(key: string): AttrFamily {
  const k = key.toLowerCase();
  for (const [prefix, fam] of FAMILY_PREFIXES) if (k.startsWith(prefix)) return fam;
  return 'other';
}

// Families that describe the PROCESS (set once per resource), not the
// operation. The backend already separates resourceAttributes vs attributes,
// but the FilterBuilder merges them — this re-classifies a free key.
const RESOURCE_FAMILIES = new Set<AttrFamily>([
  'service', 'k8s', 'cloud', 'host', 'process', 'runtime', 'telemetry',
  'deployment', 'container', 'os',
]);

export function isResourceAttrKey(key: string): boolean {
  return RESOURCE_FAMILIES.has(attrFamily(key));
}

// ── Span kind / status / scope ───────────────────────────────────────────────

export type SpanKindNorm = 'server' | 'client' | 'producer' | 'consumer' | 'internal' | 'unspecified';

// normSpanKind accepts the wire forms ('server', 'SPAN_KIND_SERVER', '2', …)
// and normalises to a lowercase enum.
export function normSpanKind(kind: string | undefined): SpanKindNorm {
  const k = (kind ?? '').toLowerCase().replace(/^span_kind_/, '');
  switch (k) {
    case 'server': case '2': return 'server';
    case 'client': case '3': return 'client';
    case 'producer': case '4': return 'producer';
    case 'consumer': case '5': return 'consumer';
    case 'internal': case '1': return 'internal';
    default: return 'unspecified';
  }
}

export type SpanStatus = 'ok' | 'error' | 'unset';

export function spanStatus(statusCode: string | undefined): SpanStatus {
  const s = (statusCode ?? '').toLowerCase();
  if (s === 'error' || s === '2' || s === 'status_code_error') return 'error';
  if (s === 'ok' || s === '1' || s === 'status_code_ok') return 'ok';
  return 'unset';
}

// scopeKey is the instrumentation-scope grouping key (otel.scope.name).
export function scopeKey(scopeName: string | undefined): string {
  return scopeName && scopeName !== '' ? scopeName : 'unknown.scope';
}
