'use client';
import { createContext, useCallback, useContext, useEffect, useState } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { api, setUnauthorizedHandler, type AuthUser } from '@/lib/api';
import { isPublicPath, normalizePath } from '@/lib/auth-paths';

interface AuthState {
  user: AuthUser | null;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const Ctx = createContext<AuthState | null>(null);

export function useAuth(): AuthState {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>');
  return ctx;
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);

  // 401 from any api call drops the local user and pushes to /login.
  // The handler is registered once for the whole app.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setUser(null);
      if (!isPublicPath(window.location.pathname)) {
        router.replace('/login');
      }
    });
    return () => setUnauthorizedHandler(null);
  }, [router]);

  // On mount + on every route change, verify the cookie session.
  useEffect(() => {
    let cancelled = false;
    api.me()
      .then(u => { if (!cancelled) setUser(u); })
      .catch(() => { if (!cancelled) setUser(null); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, []);

  // Redirect away from protected routes once we know we're not authed.
  useEffect(() => {
    if (loading) return;
    const path = normalizePath(pathname ?? '');
    if (!user && !isPublicPath(path)) {
      router.replace('/login');
    }
    if (user && path === '/login') {
      router.replace('/');
    }
  }, [loading, user, pathname, router]);

  const login = useCallback(async (email: string, password: string) => {
    const res = await api.login(email, password);
    setUser(res.user);
  }, []);

  const logout = useCallback(async () => {
    try { await api.logout(); } catch { /* ignore */ }
    setUser(null);
    router.replace('/login');
  }, [router]);

  return (
    <Ctx.Provider value={{ user, loading, login, logout }}>
      {children}
    </Ctx.Provider>
  );
}
