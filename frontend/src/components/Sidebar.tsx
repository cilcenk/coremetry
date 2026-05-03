'use client';
import Link from 'next/link';
import { useRouter, usePathname } from 'next/navigation';
import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { ThemeToggle } from './ThemeToggle';
import { useAuth } from './AuthProvider';
import { ChangePasswordModal } from './ChangePasswordModal';

const NAV = [
  { href: '/problems',   label: 'Problems',     icon: '⚠' },
  { href: '/errors',     label: 'Errors',       icon: '⊗' },
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
];

export function Sidebar() {
  const pathname = usePathname();
  const router = useRouter();
  const { user, logout } = useAuth();
  const [health, setHealth] = useState<string>('Connecting…');
  const [openProblems, setOpenProblems] = useState(0);
  const [menuOpen, setMenuOpen] = useState(false);
  const [showChangePw, setShowChangePw] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

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

  return (
    <nav id="sidebar">
      <div id="sidebar-header">
        <span className="logo">⬡</span>
        <span className="title">Qmetry</span>
        <span style={{ marginLeft: 'auto' }}><ThemeToggle /></span>
      </div>
      <div id="nav">
        {NAV.map(n => (
          <Link key={n.href} href={n.href} className={isActive(pathname, n.href) ? 'active' : ''}>
            <span className="icon">{n.icon}</span> {n.label}
            {n.href === '/problems' && openProblems > 0 && (
              <span className="nav-badge">{openProblems}</span>
            )}
          </Link>
        ))}
      </div>
      {user && (
        <div ref={menuRef} id="user-menu" style={{
          position: 'relative',
          padding: '8px 14px', borderTop: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 8,
          cursor: 'pointer',
        }} onClick={() => setMenuOpen(o => !o)}>
          <div style={{
            width: 26, height: 26, borderRadius: '50%',
            background: 'var(--accent)', color: '#fff',
            display: 'grid', placeItems: 'center',
            fontSize: 12, fontWeight: 600, textTransform: 'uppercase',
          }}>
            {user.email[0]}
          </div>
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

          {menuOpen && (
            <div onClick={e => e.stopPropagation()} style={{
              position: 'absolute', bottom: '100%', left: 8, right: 8, marginBottom: 4,
              background: 'var(--bg2)', border: '1px solid var(--border)',
              borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.3)',
              padding: 4, zIndex: 50,
            }}>
              {user.role === 'admin' && (
                <MenuItem onClick={() => { setMenuOpen(false); router.push('/users'); }}>
                  ◯ Manage users
                </MenuItem>
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
      <div id="nav-footer">{health}</div>

      {showChangePw && (
        <ChangePasswordModal onClose={() => setShowChangePw(false)} />
      )}
    </nav>
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
  // Special cases for "list/detail" pairs that share a base name.
  if (href === '/traces'     && pathname.startsWith('/trace'))     return true;
  if (href === '/dashboards' && pathname.startsWith('/dashboard')) return true;
  return pathname === href || pathname === href + '/';
}
