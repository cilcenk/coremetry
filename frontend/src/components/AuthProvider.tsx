import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { api, setUnauthorizedHandler, type AuthUser } from '@/lib/api';
import { isPublicPath, normalizePath } from '@/lib/auth-paths';
import { savePostLoginRedirect, consumePostLoginRedirect } from '@/lib/postLoginRedirect';

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
  const navigate = useNavigate();
  const { pathname, search, hash } = useLocation();
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);

  // 401 from any api call drops the local user and pushes to /login.
  // The handler is registered once for the whole app.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setUser(null);
      if (!isPublicPath(window.location.pathname)) {
        // Keep the full deep link (path + query + hash) so signing
        // back in lands where the expired session was (v0.8.367).
        savePostLoginRedirect(window.location.pathname + window.location.search + window.location.hash);
        navigate('/login');
      }
    });
    return () => setUnauthorizedHandler(null);
  }, [navigate]);

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
      // Capture the pasted deep link before bouncing (v0.8.367,
      // operator-reported — Dynatrace-style restore after login).
      savePostLoginRedirect((pathname ?? '') + (search ?? '') + (hash ?? ''));
      navigate('/login');
    }
    // Authed on /login (fresh local login) or on the default landing
    // ('/', where the OIDC callback drops us): restore the captured
    // deep link when one exists; plain logins keep going to '/'.
    if (user && path === '/login') {
      navigate(consumePostLoginRedirect() ?? '/');
    } else if (user && path === '/') {
      const target = consumePostLoginRedirect();
      if (target) navigate(target);
    }
  }, [loading, user, pathname, search, hash, navigate]);

  const login = useCallback(async (email: string, password: string) => {
    const res = await api.login(email, password);
    setUser(res.user);
  }, []);

  const logout = useCallback(async () => {
    try { await api.logout(); } catch { /* ignore */ }
    setUser(null);
    navigate('/login');
  }, [navigate]);

  // v0.7.79 — memoise the context value. AuthProvider re-renders on
  // EVERY route change (it reads pathname via useLocation), so an
  // inline object literal here handed every useAuth() consumer a new
  // reference per navigation and re-rendered all of them. login/logout
  // are useCallback-stable, so the value now only changes when the
  // session actually changes (user/loading).
  const value = useMemo(
    () => ({ user, loading, login, logout }),
    [user, loading, login, logout],
  );

  return (
    <Ctx.Provider value={value}>
      {children}
    </Ctx.Provider>
  );
}
