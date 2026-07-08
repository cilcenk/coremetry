import { useEffect, useId, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useUrlEnv } from '@/lib/useUrlEnv';

// EnvPicker (v0.8.383 — env-separation Phase 1) — the GLOBAL
// deployment-environment filter, mounted once in the Topbar next to
// the range picker (Datadog's env tag / Dynatrace management-zone
// placement: the operator says "look at uat" and every page follows).
//
// v0.8.389 (operator-reported): the original plain <select> assumed
// the ≤~10-values rule — feature-branch envs (int-feature-*) broke
// it, and the alphabetical LIMIT 50 enumeration starved later names
// ("release" never appeared, unsearchable). Now the ServicePicker
// anatomy: debounced server search (?q=) over a count-ordered list,
// truncation labelled from the server's total, datalist pick
// auto-commits. The backend widens its scan clamp 1h→24h for
// searched lookups so quiet-but-real envs are findable by name.
//
// Selection writes `?env=` via useUrlEnv (replace:true, prev-copying,
// localStorage-mirrored) so it persists across navigations exactly
// like the range does, is shareable, and rides SavedViewsBar's
// whole-query-string snapshots. Viewer-visible — a read filter.
//
// Consumers: /traces (v0.8.383), /services + /endpoints (v0.8.385),
// /problems + /inbox + sidebar badge (v0.8.387, service-scoped).
// Other pages ignore `?env=` until their backends grow the filter —
// the title says so instead of implying a global effect.
export function EnvPicker() {
  const [env, setEnv] = useUrlEnv();
  const listId = useId();
  // draft = what's in the input while typing; committed env only
  // changes on pick / Enter / clear so half-typed text never filters.
  const [draft, setDraft] = useState(env);
  const [q, setQ] = useState('');
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastValueRef = useRef(env);

  // Keep the input in sync when the env changes from outside (shared
  // link, SavedViews restore, another tab writing localStorage).
  useEffect(() => { setDraft(env); lastValueRef.current = env; }, [env]);

  const listQ = useQuery({
    queryKey: ['environments', q],
    queryFn: () => api.environments(q),
    staleTime: 120_000, // ≥ server TTL (60s) — cache-rung discipline
    refetchOnWindowFocus: false,
    retry: false,
  });
  const fetched = listQ.data?.environments ?? [];
  const total = listQ.data?.total ?? fetched.length;
  // Sticky/shared value stays selectable even when outside the
  // enumeration window (never validate a pick against a sampled
  // subset — the v0.8.265 lesson).
  const options = env && !fetched.includes(env) ? [env, ...fetched] : fetched;
  const truncated = total > fetched.length;

  // Installs without any deploy_env data get no extra chrome — but
  // never hide while a committed env or a search is active.
  if (options.length === 0 && !env && !q) return null;

  const commit = (v: string) => {
    setEnv(v);
    setDraft(v);
    lastValueRef.current = v;
  };

  const handleChange = (next: string) => {
    const prev = lastValueRef.current;
    lastValueRef.current = next;
    setDraft(next);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => setQ(next.trim()), 180);
    // Datalist-pick heuristic (ServicePicker's): a multi-char jump to
    // a known option = a click on the dropdown row → commit.
    const jumped = Math.abs(next.length - prev.length) > 1 || (next.length > 0 && prev === '');
    if (jumped && options.includes(next)) {
      setTimeout(() => commit(next), 0);
    }
  };

  return (
    <div className="cb-wrap" style={{ width: 150 }}>
      <input
        className="env-picker"
        list={listId}
        value={draft}
        placeholder="All environments"
        aria-label="Environment filter"
        autoComplete="off"
        spellCheck={false}
        onChange={e => handleChange(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter') commit(draft.trim()); }}
        onBlur={() => { if (draft.trim() === '') commit(''); else setDraft(env); }}
        title={
          (truncated
            ? `Showing the ${fetched.length} busiest of ${total} environments — type to search all (last 24h).\n`
            : '') +
          'Filter by deployment environment (deployment.environment.name).\n' +
          'Applies to Traces, Services, Endpoints, Problems and Inbox today; more pages follow in upcoming releases.\n' +
          'On Problems/Inbox the filter is service-scoped: rows whose service runs in the environment.'
        }
      />
      {env && (
        <button className="cb-clear" type="button"
          aria-label="All environments" title="All environments"
          onClick={() => commit('')}
          onMouseDown={e => e.preventDefault()}>
          ✕
        </button>
      )}
      <datalist id={listId}>
        {options.map(o => <option key={o} value={o} />)}
        {truncated && <option value="" disabled>… +{total - fetched.length} more — type to search</option>}
      </datalist>
    </div>
  );
}
