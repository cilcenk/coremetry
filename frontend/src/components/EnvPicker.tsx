import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useUrlEnv } from '@/lib/useUrlEnv';

// EnvPicker (v0.8.383 — env-separation Phase 1) — the GLOBAL
// deployment-environment filter, mounted once in the Topbar next to
// the range picker (Datadog's env tag / Dynatrace management-zone
// placement: the operator says "look at uat" and every page follows).
//
// A plain <select> is deliberate: /api/environments is server-backed
// but the value set is a handful of deploy-stable names (int/uat/
// prep/prod…), i.e. the ≤~10-values rule from frontend-conventions §3
// — no server-debounced picker machinery needed. No polling: the list
// is fetched once per mount and held for staleTime ≥ the server's 60s
// serveCached TTL (+ warmer), so page hops ride the RQ cache.
//
// Selection writes `?env=` via useUrlEnv (replace:true, prev-copying,
// localStorage-mirrored) so it persists across navigations exactly
// like the range does, is shareable, and rides SavedViewsBar's
// whole-query-string snapshots. Viewer-visible — it's a read filter,
// not an admin control.
//
// Consumers: /traces (Phase 1, v0.8.383 — list + aggregated + volume
// strip + CSV) and /services + /endpoints (Phase 2, v0.8.385 —
// cluster-parity raw-fallback). Other pages ignore `?env=` until
// their backends grow the filter (Phase 3+) — the title says so
// instead of implying a global effect.
export function EnvPicker() {
  const [env, setEnv] = useUrlEnv();

  const q = useQuery({
    queryKey: ['environments'],
    queryFn: () => api.environments(),
    staleTime: 120_000, // ≥ server TTL (60s) — ES-cost/cache-rung discipline
    refetchOnWindowFocus: false,
    retry: false,
  });

  const fetched = q.data?.environments ?? [];
  // Keep a sticky/shared value selectable even when it has no spans in
  // the enumeration window (never validate a pick against a sampled
  // subset — the v0.8.265 lesson).
  const options = env && !fetched.includes(env)
    ? [...fetched, env].sort()
    : fetched;

  // Installs without any deploy_env data get no extra chrome — the
  // picker appears the moment a service starts emitting environments.
  if (options.length === 0) return null;

  return (
    <select
      className="env-picker"
      value={env}
      aria-label="Environment filter"
      title={'Filter by deployment environment (deployment.environment.name).\nApplies to Traces, Services and Endpoints today; more pages follow in upcoming releases.'}
      onChange={e => setEnv(e.target.value)}
    >
      <option value="">All environments</option>
      {options.map(o => (
        <option key={o} value={o}>{o}</option>
      ))}
    </select>
  );
}
