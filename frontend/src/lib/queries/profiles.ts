import { useQuery } from '@tanstack/react-query';
import { api, type ProfilesParams } from '@/lib/api';

// Continuous-profiling reads for /profiling. Both hooks are gated
// by `enabled` because the page's two views (per-profile list vs
// aggregated hotspots) fetch mutually exclusively, and hotspots
// additionally require a service (the backend rejects unbounded
// aggregation).

export function useProfiles(params: ProfilesParams, enabled = true) {
  return useQuery({
    queryKey: ['profiles', 'list', params],
    queryFn: async () => (await api.profiles(params)) ?? [],
    enabled,
  });
}

export function useProfileHotspots(
  params: { service: string; type?: string; from: number; to: number; limit?: number; top?: number },
  enabled = true,
) {
  return useQuery({
    queryKey: ['profiles', 'hotspots', params],
    queryFn: () => api.profileHotspots(params),
    enabled,
  });
}
