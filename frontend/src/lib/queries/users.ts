import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';

// Admin user management (/users). Mutations on that page are inline
// api.* calls behind confirm() dialogs; they refresh by invalidating
// keys.users.all, which covers both queries below.

export function useUsers() {
  return useQuery({
    queryKey: keys.users.list,
    queryFn: async () => (await api.listUsers()) ?? [],
  });
}

// Custom-role catalog — drives the per-row picker on /users. Fetched
// alongside the user list, refreshed on every change so a role added
// in Settings → Roles appears here without a hard reload.
export function useCustomRoles() {
  return useQuery({
    queryKey: keys.users.customRoles,
    queryFn: async () => (await api.listCustomRoles()).roles ?? [],
  });
}
