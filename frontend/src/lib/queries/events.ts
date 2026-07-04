import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';

const EVENTS_KEY = ['events'] as const;

type EventRow = NonNullable<Awaited<ReturnType<typeof api.listEvents>>>[number];

// Operator events (v0.5.476) — the vertical markers operators drop
// on every time-series chart. /events lists + deletes them; the
// filter object is part of the key so each (window × service × kind)
// combination caches separately.
export function useOperatorEvents(filter: {
  from: number; to: number; service?: string; kind?: string; limit?: number;
}) {
  return useQuery({
    queryKey: [...EVENTS_KEY, 'list', filter],
    queryFn: async () => (await api.listEvents(filter)) ?? [],
  });
}

// Sent-notification log (v0.8.263) — the /events Notifications tab.
// 30s poll keeps the tab near-live without breaching the ≥10s
// polling budget; RQ pauses interval refetches while the tab is
// hidden (refetchIntervalInBackground defaults false), which
// satisfies the document.hidden rule.
export function useNotificationLog(filter: {
  from?: number; to?: number; kind?: string; limit?: number;
}) {
  return useQuery({
    queryKey: [...EVENTS_KEY, 'notifications', filter],
    queryFn: async () => (await api.notificationLog(filter)) ?? [],
    staleTime: 15_000,
    refetchInterval: 30_000,
  });
}

// Delete drops the row from every cached list in place — the page
// previously did setData(filter) rather than refetching, so we keep
// that no-refetch behaviour with a cache write instead.
export function useDeleteOperatorEvent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteEvent(id),
    onSuccess: (_data, id) => {
      qc.setQueriesData<EventRow[]>(
        { queryKey: EVENTS_KEY },
        prev => prev?.filter(e => e.id !== id) ?? prev,
      );
    },
  });
}
