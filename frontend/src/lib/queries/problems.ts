import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type { Problem } from '@/lib/types';

// /api/problems — the open-incident inbox feeding /problems,
// /anomalies, the sidebar badge, and several deep-link
// drill-downs. With React Query the same data is shared across
// all consumers — when the sidebar's 30s poll fetches, the
// /problems page that's also mounted gets the new data without
// its own request. Single source of truth, single network call.
//
// `service` filter is part of the key, so /problems?service=foo
// caches separately from the global list — switching back and
// forth between the two doesn't refetch.
export function useProblems(filter: {
  status?: 'open' | 'all' | 'resolved';
  service?: string;
  priority?: string[];
  limit?: number;
}) {
  return useQuery<Problem[]>({
    queryKey: keys.problems.list(filter),
    // queryFn returns Problem[] always — api.problems can
    // return null on error but we map to [] in the component
    // layer. Here we let the error bubble to React Query so the
    // hook can surface isError / error to the caller.
    queryFn: async () => {
      const res = await api.problems(filter);
      return res ?? [];
    },
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}

// Open-problem count for the sidebar badge. v0.5.398 — switched
// from fetching limit=200 rows + counting the array to a
// dedicated /api/problems/count endpoint. The old approach
// capped the displayed badge at 200 silently on installs with
// >200 open problems; the new path returns the true count via
// a single COUNT(*) on the server.
export function useOpenProblemCount() {
  return useQuery<{ count: number }, Error, number>({
    queryKey: ['problems', 'count', { status: 'open' }],
    queryFn: async () => (await api.problemsCount({ status: 'open' })) ?? { count: 0 },
    select: (r) => r.count,
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}
