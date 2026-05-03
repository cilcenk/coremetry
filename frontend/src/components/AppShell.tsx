'use client';
import { usePathname } from 'next/navigation';
import { Sidebar } from './Sidebar';
import { useAuth } from './AuthProvider';
import { isPublicPath } from '@/lib/auth-paths';

// AppShell decides whether to render the full sidebar+main layout or just
// pass children through (login page, loading splash). Keeps the root layout
// itself a server component so the theme boot script still runs on the
// initial HTML.
export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname() ?? '';
  const { user, loading } = useAuth();
  const isPublic = isPublicPath(pathname);

  if (isPublic) {
    return <>{children}</>;
  }
  if (loading) {
    return (
      <div style={{
        position: 'fixed', inset: 0, display: 'grid', placeItems: 'center',
        color: 'var(--text3)', fontSize: 13,
      }}>
        Loading…
      </div>
    );
  }
  if (!user) {
    // AuthProvider is in the middle of redirecting to /login.
    return null;
  }
  return (
    <div id="app">
      <Sidebar />
      <div id="main">{children}</div>
    </div>
  );
}
