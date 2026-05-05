'use client';
import Link from 'next/link';
import { useRouter, usePathname } from 'next/navigation';
import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { ThemeToggle } from './ThemeToggle';
import { TelescopeIcon } from './TelescopeIcon';
import { useAuth } from './AuthProvider';
import { ChangePasswordModal } from './ChangePasswordModal';

const NAV = [
  { href: '/problems',   label: 'Problems',     icon: '⚠' },
  { href: '/errors',     label: 'Exceptions',   icon: '⊗' },
  { href: '/services',   label: 'Services',     icon: '◈' },
  { href: '/traces',     label: 'Traces',       icon: '⋮' },
  { href: '/metrics',    label: 'Metrics',      icon: '∿' },
  { href: '/logs',       label: 'Logs',         icon: '≡' },
  { href: '/explore',    label: 'Explore',      icon: '◎' },
  { href: '/dashboards', label: 'Dashboards',   icon: '◫' },
  { href: '/graph',      label: 'Service Graph', icon: '⬡' },
  { href: '/profiling',  label: 'Profiling',    icon: '🔥' },
  { href: '/alerts',     label: 'Alerts',       icon: '🔔' },
  { href: '/slos',       label: 'SLOs',         icon: '◉' },
  { href: '/status',     label: 'Status',       icon: '◐' },
];

const SIDEBAR_WIDTH_KEY     = 'coremetry-sidebar-w';
const SIDEBAR_COLLAPSED_KEY = 'coremetry-sidebar-collapsed';
const COLLAPSED_W = 56;
const MIN_W = 160;
const MAX_W = 360;
const MOBILE_BP = 768;

export function Sidebar() {
  const pathname = usePathname();
  const router = useRouter();
  const { user, logout } = useAuth();
  const [health, setHealth] = useState<string>('Connecting…');
  const [openProblems, setOpenProblems] = useState(0);
  const [menuOpen, setMenuOpen] = useState(false);
  const [showChangePw, setShowChangePw] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  // Width + collapsed state hydrate from localStorage on mount only;
  // initial render uses safe defaults so SSR + client agree.
  const [width, setWidth] = useState(196);
  const [collapsed, setCollapsed] = useState(false);
  const [isMobile, setIsMobile] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  useEffect(() => {
    const w = parseInt(localStorage.getItem(SIDEBAR_WIDTH_KEY) ?? '', 10);
    if (Number.isFinite(w) && w >= MIN_W && w <= MAX_W) setWidth(w);
    setCollapsed(localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === '1');
  }, []);
  // Track viewport so we can switch to mobile drawer mode below the breakpoint.
  useEffect(() => {
    const apply = () => setIsMobile(window.innerWidth < MOBILE_BP);
    apply();
    window.addEventListener('resize', apply);
    return () => window.removeEventListener('resize', apply);
  }, []);

  // ── Drag-to-resize ────────────────────────────────────────────────────────
  const dragRef = useRef<{ startX: number; startW: number } | null>(null);
  const onResizeStart = (e: React.MouseEvent) => {
    if (collapsed || isMobile) return;
    e.preventDefault();
    dragRef.current = { startX: e.clientX, startW: width };
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  };
  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      if (!dragRef.current) return;
      const next = Math.max(MIN_W, Math.min(MAX_W,
        dragRef.current.startW + (e.clientX - dragRef.current.startX)));
      setWidth(next);
    };
    const onUp = () => {
      if (!dragRef.current) return;
      dragRef.current = null;
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
      localStorage.setItem(SIDEBAR_WIDTH_KEY, String(width));
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, [width]);
  const toggleCollapsed = () => {
    setCollapsed(c => {
      const next = !c;
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, next ? '1' : '0');
      return next;
    });
    setMenuOpen(false);
  };

  useEffect(() => {
    api.health()
      .then(h => setHealth(`Q: spans ${h.spans_queued} · logs ${h.logs_queued}`))
      .catch(() => setHealth('Backend offline'));
  }, []);

  // Poll open-problem count every 30s for the sidebar badge
  useEffect(() => {
    const refresh = () => api.problems({ status: 'open', limit: 200 })
      .then(p => setOpenProblems((p ?? []).length))
      .catch(() => {});
    refresh();
    const t = setInterval(refresh, 30_000);
    return () => clearInterval(t);
  }, []);

  // Close the user menu on outside click.
  useEffect(() => {
    if (!menuOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [menuOpen]);

  // Close mobile drawer on route change.
  useEffect(() => { setDrawerOpen(false); }, [pathname]);

  // ── Effective layout values ──────────────────────────────────────────────
  // On mobile: sidebar is an off-canvas overlay (full label expanded), shown
  // only when the user taps the hamburger. On desktop: in-flow column whose
  // width depends on the collapsed flag and the drag-resized width.
  const showLabels = isMobile || !collapsed;
  const computedWidth = isMobile ? 240 : (collapsed ? COLLAPSED_W : width);

  return (
    <>
      {isMobile && (
        <button onClick={() => setDrawerOpen(true)} aria-label="Open menu"
          style={{
            position: 'fixed', top: 10, left: 10, zIndex: 60,
            width: 36, height: 36, padding: 0,
            background: 'var(--bg2)', color: 'var(--text)',
            border: '1px solid var(--border)', borderRadius: 6,
            fontSize: 18, lineHeight: '32px',
          }}>
          ☰
        </button>
      )}
      {isMobile && drawerOpen && (
        <div onClick={() => setDrawerOpen(false)} style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', zIndex: 40,
        }} />
      )}

      <nav id="sidebar"
        data-collapsed={collapsed && !isMobile ? 'true' : 'false'}
        data-mobile={isMobile ? 'true' : 'false'}
        data-open={isMobile && drawerOpen ? 'true' : 'false'}
        style={{
          width: computedWidth,
          ...(isMobile ? {
            position: 'fixed', top: 0, bottom: 0, left: 0, zIndex: 50,
            transform: drawerOpen ? 'translateX(0)' : 'translateX(-100%)',
            transition: 'transform .2s ease',
            boxShadow: drawerOpen ? '4px 0 20px rgba(0,0,0,0.4)' : 'none',
          } : {}),
        }}>
        <div id="sidebar-header">
          <TelescopeIcon size={22} />
          {showLabels && <span className="title">Coremetry</span>}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
            {showLabels && <ThemeToggle />}
            {!isMobile && (
              <button onClick={toggleCollapsed} title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
                style={{
                  width: 24, height: 24, padding: 0, fontSize: 12,
                  background: 'transparent', color: 'var(--text2)',
                  border: '1px solid var(--border)', borderRadius: 4,
                }}>
                {collapsed ? '»' : '«'}
              </button>
            )}
          </span>
        </div>
        <div id="nav">
          {NAV.map(n => (
            <Link key={n.href} href={n.href}
              className={isActive(pathname, n.href) ? 'active' : ''}
              title={!showLabels ? n.label : undefined}
              style={!showLabels ? { justifyContent: 'center', padding: '10px 0' } : undefined}>
              <span className="icon">{n.icon}</span>
              {showLabels && <span className="nav-label">{n.label}</span>}
              {showLabels && n.href === '/problems' && openProblems > 0 && (
                <span className="nav-badge">{openProblems}</span>
              )}
              {!showLabels && n.href === '/problems' && openProblems > 0 && (
                <span className="nav-dot" title={`${openProblems} open problems`} />
              )}
            </Link>
          ))}
        </div>
        {user && (
          <div ref={menuRef} id="user-menu" style={{
            position: 'relative',
            padding: showLabels ? '8px 14px' : '8px 0',
            borderTop: '1px solid var(--border)',
            display: 'flex', alignItems: 'center',
            justifyContent: showLabels ? 'flex-start' : 'center',
            gap: 8, cursor: 'pointer',
          }} onClick={() => setMenuOpen(o => !o)}>
            <div style={{
              width: 26, height: 26, borderRadius: '50%',
              background: 'var(--accent)', color: '#fff',
              display: 'grid', placeItems: 'center', flexShrink: 0,
              fontSize: 12, fontWeight: 600, textTransform: 'uppercase',
            }} title={!showLabels ? user.email : undefined}>
              {user.email[0]}
            </div>
            {showLabels && (
              <>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{
                    fontSize: 12, color: 'var(--text2)',
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }} title={user.email}>{user.email}</div>
                  <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase' }}>
                    {user.role}
                  </div>
                </div>
                <span style={{ color: 'var(--text3)', fontSize: 10 }}>{menuOpen ? '▾' : '▸'}</span>
              </>
            )}

            {menuOpen && (
              <div onClick={e => e.stopPropagation()} style={{
                position: 'absolute', bottom: '100%', left: showLabels ? 8 : 4,
                right: showLabels ? 8 : 4, marginBottom: 4, minWidth: 180,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.3)',
                padding: 4, zIndex: 50,
              }}>
                {user.role === 'admin' && (
                  <>
                    <MenuItem onClick={() => { setMenuOpen(false); router.push('/users'); }}>
                      ◯ Manage users
                    </MenuItem>
                    <MenuItem onClick={() => { setMenuOpen(false); router.push('/settings'); }}>
                      ⚙ Settings
                    </MenuItem>
                  </>
                )}
                <MenuItem onClick={() => { setMenuOpen(false); setShowChangePw(true); }}>
                  ⚿ Change password
                </MenuItem>
                <MenuItem onClick={() => { setMenuOpen(false); logout(); }}>
                  ⏻ Sign out
                </MenuItem>
              </div>
            )}
          </div>
        )}
        {showLabels && <div id="nav-footer">{health}</div>}

        {!collapsed && !isMobile && (
          <div className="sidebar-resizer"
            title="Drag to resize"
            onMouseDown={onResizeStart} />
        )}

        {showChangePw && (
          <ChangePasswordModal onClose={() => setShowChangePw(false)} />
        )}
      </nav>
    </>
  );
}

function MenuItem({ children, onClick }: { children: React.ReactNode; onClick: () => void }) {
  return (
    <button onClick={onClick} style={{
      display: 'block', width: '100%', textAlign: 'left',
      padding: '7px 10px', border: 'none', background: 'transparent',
      color: 'var(--text2)', fontSize: 13, cursor: 'pointer', borderRadius: 4,
    }}
      onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3, var(--bg))')}
      onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
      {children}
    </button>
  );
}

function isActive(pathname: string | null, href: string): boolean {
  if (!pathname) return false;
  if (href === '/traces'     && pathname.startsWith('/trace'))     return true;
  if (href === '/dashboards' && pathname.startsWith('/dashboard')) return true;
  return pathname === href || pathname === href + '/';
}
