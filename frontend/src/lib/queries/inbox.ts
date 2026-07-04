import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { InboxItem } from '@/lib/types';

// Unified triage inbox (v0.5.211) — Problems + Exception groups +
// Anomaly events merged server-side with the P1/P2/P3 priority
// blend. Priority/kind chips filter client-side on the page; only
// the server-side filters participate in the key.
export function useInbox(filter: {
  status?: 'open' | 'all'; service?: string;
  ownerTeam?: string; sreTeam?: string;
  limit?: number;
}) {
  return useQuery<InboxItem[]>({
    queryKey: ['inbox', 'list', filter],
    queryFn: async () => (await api.inbox(filter)) ?? [],
  });
}
