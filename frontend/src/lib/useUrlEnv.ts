import { useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { getRaw, setRaw, removeRaw, STORAGE_KEYS } from './storage';

// useUrlEnv (v0.8.383 — env-separation Phase 1) — the single source of
// truth for the GLOBAL deployment-environment filter: the URL `?env=`
// param, mirrored into localStorage for cross-page continuity.
//
// Deliberately the same precedence contract as useUrlRange (the
// operator's mental model: "env behaves like the time range"):
//   1. `?env=` in the URL  — wins (shareable links, back/forward,
//      saved views; an EXPLICITLY EMPTY `env=` means "all envs" and
//      must not resurrect the local sticky pick — see resolveEnv)
//   2. localStorage        — cross-page continuity ("look at uat"
//      follows the operator across every page)
//   3. ''                  — all environments (the default)
//
// Writes use { replace: true } and copy `prev` so foreign params
// survive (frontend-conventions §4). '' clears BOTH the param and the
// stored pick — "All environments" is the absence of a filter, not a
// value.
//
// `env` is a string primitive, so unlike useUrlRange there is no
// object-identity memo needed: downstream deps ([env]) only change
// when the resolved value actually changes.
//
// Phase 1 consumers: the Topbar EnvPicker (writer) and /traces
// (list + aggregated + volume strip + CSV export). Other pages ignore
// `?env=` until their backend surfaces grow the filter (Phase 2+);
// pages that rebuild their URL from a state list (Traces-style
// State→URL effects) must include ['env', env] or a local state write
// drops the visible param — Traces does, others adopt it per-slice.

const ENV_STORE_KEY = STORAGE_KEYS.env;

// Bounded so a crafted URL can't stuff junk into localStorage / the
// picker; deploy_env is a LowCardinality column — real values are
// short ("int", "uat", "prep", "production").
const MAX_ENV_LEN = 64;

/** normalizeEnv — trim + length-cap a raw env value; '' = no filter. */
export function normalizeEnv(raw: string | null | undefined): string {
  if (!raw) return '';
  const v = raw.trim();
  return v.length > MAX_ENV_LEN ? v.slice(0, MAX_ENV_LEN) : v;
}

/**
 * resolveEnv — precedence: URL value > explicit-empty URL param ("all
 * environments" in a shared link) > stored sticky pick > ''.
 * Pure so the codec is vitest-able without a router.
 */
export function resolveEnv(urlVal: string | null, storedVal: string | null): string {
  const u = normalizeEnv(urlVal);
  if (u) return u;
  // Param present but empty — an explicit "all environments" (e.g. a
  // shared/saved URL where the sender cleared the filter). The local
  // sticky pick must NOT override the sender's intent.
  if (urlVal !== null) return '';
  return normalizeEnv(storedVal);
}

export function useUrlEnv(): [string, (e: string) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const env = resolveEnv(searchParams.get('env'), getRaw(ENV_STORE_KEY));

  const setEnv = useCallback((e: string) => {
    const v = normalizeEnv(e);
    setSearchParams(
      prev => {
        const next = new URLSearchParams(prev);
        if (v) next.set('env', v);
        else next.delete('env');
        return next;
      },
      { replace: true },
    );
    if (v) setRaw(ENV_STORE_KEY, v);
    else removeRaw(ENV_STORE_KEY);
  }, [setSearchParams]);

  return [env, setEnv];
}
