// podDetailPath (v0.9.152) — the single builder for a /pod detail-page drill
// URL, shared by every drill site (Service→Infra pod table, Clusters pod
// table, and the Metrics tab pod drill). Extracted after an adversarial review
// found the inline navigate() calls silently dropped the incoming ?range, so a
// brushed/absolute incident window was lost on drill-in (the pod page fell back
// to 1h). Centralising it means a new drill site can't re-introduce that bug.
//
// Only non-empty fields are emitted so a minimal link (service+pod) stays
// clean and lets /pod self-resolve the rest. range is forwarded verbatim
// (e.g. "custom:<from>-<to>") to preserve the operator's window; from marks the
// originating tab so /pod's back-breadcrumb reads "← <service> · Infrastructure"
// vs "· Metrics".
export function podDetailPath(opts: {
  pod: string;
  cluster?: string;
  namespace?: string;
  service?: string;
  deploy?: string;
  range?: string | null;
  from?: 'infra' | 'pods' | 'metrics' | 'clusters';
}): string {
  const q = new URLSearchParams();
  if (opts.cluster) q.set('cluster', opts.cluster);
  if (opts.namespace) q.set('namespace', opts.namespace);
  q.set('pod', opts.pod);
  if (opts.service) q.set('service', opts.service);
  if (opts.deploy) q.set('deploy', opts.deploy);
  if (opts.range) q.set('range', opts.range);
  if (opts.from) q.set('from', opts.from);
  return '/pod?' + q.toString();
}
