import { Link } from 'react-router-dom';
import { useNavigate, useLocation } from 'react-router-dom';
import { useEffect, useRef, useState } from 'react';
import { useHealth, useOpenProblemCount } from '@/lib/queries';
import { useT } from '@/lib/i18n';
import { TelescopeIcon } from './TelescopeIcon';
import {
  Inbox, TriangleAlert, CircleAlert, Activity, Boxes, Webhook, Workflow, Database,
  MessageSquare, ListTree, ChartSpline, ScrollText, Flame, Compass, BookText,
  LayoutDashboard, Bell, Gauge, Target, CalendarClock, CircleGauge, Search, Hash,
  Server, Sparkles, LayoutGrid, FileClock, Terminal, Code, Globe, type LucideIcon,
} from 'lucide-react';
import { useAuth } from './AuthProvider';
import { ChangePasswordModal } from './ChangePasswordModal';
import { Wordmark } from './Wordmark';

// adminOnly entries are hidden from non-admin users in the
// sidebar. The pages themselves still enforce admin-role at
// render time AND on the server side — this filter is purely a
// UX cleanup so the link doesn't show up to viewers/editors who
// would only get a "forbidden" page from clicking it.
type NavItem = {
  href: string;
  label: string;
  icon: LucideIcon;
  adminOnly?: boolean;
};

// NavItem labels are i18n keys; the actual label is resolved
// from the t() catalog at render time so a language switch
// surfaces immediately.
type NavGroup = {
  // Group heading i18n key. Empty = ungrouped (no heading line).
  titleKey: string;
  items: NavItem[];
};

// Grouped layout — pre-v0.4.87 the sidebar was 24 flat entries
// which made it hard to scan during a fast triage. Groups
// follow the operator's actual workflow: triage first (left
// of the eye), then the services / signals they investigate
// with, then the meta-operations (alerts, admin).
const NAV_GROUPS: NavGroup[] = [
  // Inbox lives ungrouped above everything else (v0.5.214) —
  // it's the daily landing surface for "anything needing a
  // human", so it shouldn't sit inside the Triage group with
  // the per-source drill-down pages. Empty titleKey suppresses
  // the heading.
  {
    titleKey: '',
    items: [
      { href: '/inbox',     label: 'nav.inbox',     icon: Inbox },
    ],
  },
  {
    titleKey: 'navGroup.triage',
    items: [
      { href: '/incidents', label: 'nav.incidents', icon: TriangleAlert },
      { href: '/problems',  label: 'nav.problems',  icon: CircleAlert },
      { href: '/anomalies', label: 'nav.anomalies', icon: Activity },
    ],
  },
  {
    titleKey: 'navGroup.services',
    items: [
      { href: '/services',    label: 'nav.services',   icon: Boxes },
      { href: '/endpoints',   label: 'nav.endpoints',  icon: Webhook },
      { href: '/service-map', label: 'nav.topology',   icon: Workflow }, // v0.8.219 — /topology retired → /service-map
      { href: '/databases',   label: 'nav.databases',  icon: Database },
      { href: '/messaging',   label: 'nav.messaging',  icon: MessageSquare },
    ],
  },
  {
    titleKey: 'navGroup.signals',
    items: [
      { href: '/traces',     label: 'nav.traces',    icon: ListTree },
      { href: '/metrics',    label: 'nav.metrics',   icon: ChartSpline },
      { href: '/logs',       label: 'nav.logs',      icon: ScrollText },
      { href: '/profiling',  label: 'nav.profiling', icon: Flame },
    ],
  },
  {
    titleKey: 'navGroup.workspaces',
    items: [
      { href: '/explore',    label: 'nav.explore',    icon: Compass },
      { href: '/runbooks',   label: 'nav.runbooks',   icon: BookText },
      { href: '/dashboards', label: 'nav.dashboards', icon: LayoutDashboard },
    ],
  },
  {
    titleKey: 'navGroup.alerting',
    items: [
      { href: '/alerts',   label: 'nav.alerts',   icon: Bell },
      { href: '/monitors', label: 'nav.monitors', icon: Gauge },
      { href: '/slos',     label: 'nav.slos',     icon: Target },
      // v0.6.15 — operator events (deploy / config / incident
      // markers). Editors create them via Cmd-K + manage from
      // this list. Lives in the alerts group because operationally
      // events are the timeline twin of alerts — both annotate
      // when something happened.
      { href: '/events',   label: 'nav.events',   icon: CalendarClock },
    ],
  },
  {
    titleKey: 'navGroup.system',
    items: [
      // v0.8.9 — the ten former /admin/* pages (stats, clickhouse, elastic,
      // cluster, cardinality, catalog, audit, sql, query, status-page) are
      // consolidated into the System area's own left sub-nav, so the global
      // sidebar carries ONE "System" entry. AI stays a sibling (not a System tab).
      { href: '/system', label: 'nav.system', icon: CircleGauge },
      { href: '/ai',     label: 'nav.ai',     icon: Sparkles, adminOnly: true },
    ],
  },
  {
    titleKey: 'navGroup.community',
    items: [
    ],
  },
];

const SIDEBAR_WIDTH_KEY     = 'coremetry-sidebar-w';
const SIDEBAR_COLLAPSED_KEY = 'coremetry-sidebar-collapsed';
const COLLAPSED_W = 56;
const MIN_W = 160;
const MAX_W = 360;
const MOBILE_BP = 768;

export function Sidebar() {
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const { user, logout } = useAuth();
  const t = useT();
  // Both queries auto-poll on their own intervals (5s / 30s) and
  // share their cache with anywhere else that consumes them.
  // The /exceptions page consumes the same cache for its inline
  // Problems section, so the sidebar badge and the page row
  // count never drift.
  const healthQ = useHealth();
  const openProblems = useOpenProblemCount().data ?? 0;
  // Footer only shows when the backend is unreachable — pre-v0.5.0
  // it always rendered the queue depths, which on a quiet
  // deployment read as a permanent "spans: 0 · logs: 0" line
  // that looked like a broken status indicator rather than a
  // live counter. Hide it when healthy; the System page is the
  // canonical place for queue depths anyway.
  const health = healthQ.isError ? 'Backend offline' : '';
  const [menuOpen, setMenuOpen] = useState(false);
  const [showChangePw, setShowChangePw] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  // Width + collapsed state hydrate from localStorage on mount only;
  // initial render uses safe defaults so SSR + client agree.
  const [width, setWidth] = useState(220);
  const [collapsed, setCollapsed] = useState(false);
  const [isMobile, setIsMobile] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  // Expanded nav groups — persists to localStorage so the
  // operator's preferred layout sticks across sessions. The
  // top-three "incident response surface" groups (Triage,
  // Services, Signals) start open by default since they're
  // what an SRE hits during a fast scan; Workspaces / Alerting
  // / System / Management stay collapsed for visual quiet. The
  // active route's group always auto-expands regardless of
  // stored state (see render below).
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(
    () => new Set(['navGroup.triage', 'navGroup.services', 'navGroup.signals']));
  useEffect(() => {
    try {
      const raw = localStorage.getItem('coremetry-sidebar-groups');
      if (raw) {
        const arr = JSON.parse(raw);
        if (Array.isArray(arr)) setExpandedGroups(new Set(arr));
      }
    } catch { /* ignore */ }
  }, []);
  const toggleGroup = (k: string) => {
    setExpandedGroups(prev => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k); else next.add(k);
      try { localStorage.setItem('coremetry-sidebar-groups', JSON.stringify([...next])); }
      catch { /* ignore */ }
      return next;
    });
  };
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
          {showLabels && <span className="title"><Wordmark /></span>}
          {!isMobile && (
            <button onClick={toggleCollapsed}
              className="theme-toggle"
              title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
              style={{ marginLeft: 'auto', fontSize: 12 }}>
              {collapsed ? '»' : '«'}
            </button>
          )}
        </div>
        <div id="nav">
          {NAV_GROUPS.map((group, idx) => {
            // Two-stage filter (v0.5.251):
            //   (1) adminOnly entries hide for non-admins (unchanged).
            //   (2) Custom-role pages list, when present, restricts a
            //       viewer to ONLY the named entries. nil/undefined =
            //       no restriction (default viewer); empty array =
            //       explicit "no nav at all", surfaced as a banner
            //       state but no usable links.
            const allowed = user?.customRolePages;
            const items = group.items.filter(n => {
              if (n.adminOnly && user?.role !== 'admin') return false;
              if (allowed && !allowed.includes(n.href)) return false;
              return true;
            });
            if (items.length === 0) return null;
            // Active route auto-expands its group so the operator
            // never loses the highlight when navigating.
            const groupActive = items.some(n => isActive(pathname, n.href));
            const isOpen = expandedGroups.has(group.titleKey) || groupActive;
            // Ungrouped sections (empty titleKey) reuse the
            // index for the React key so multiple ungrouped
            // blocks don't collide.
            const key = group.titleKey || `_ungrouped:${idx}`;
            return (
              <NavGroupBlock key={key}
                titleKey={group.titleKey}
                items={items}
                isOpen={isOpen}
                onToggle={() => toggleGroup(group.titleKey)}
                showLabels={showLabels}
                pathname={pathname}
                openProblems={openProblems}
                t={t} />
            );
          })}
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
            {/* v0.8.238 — LDAP directory photo when stored; initials
                fallback otherwise (and on a broken image byte-stream). */}
            {user.hasPhoto ? (
              <img src="/api/auth/me/photo" alt=""
                style={{
                  width: 26, height: 26, borderRadius: '50%',
                  objectFit: 'cover', flexShrink: 0,
                }}
                title={!showLabels ? user.email : undefined}
                onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }} />
            ) : (
              <div style={{
                width: 26, height: 26, borderRadius: '50%',
                background: 'var(--accent)', color: '#fff',
                display: 'grid', placeItems: 'center', flexShrink: 0,
                fontSize: 12, fontWeight: 600, textTransform: 'uppercase',
              }} title={!showLabels ? user.email : undefined}>
                {user.email[0]}
              </div>
            )}
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
                    <MenuItem onClick={() => { setMenuOpen(false); navigate('/users'); }}>
                      ◯ {t('user.manageUsers')}
                    </MenuItem>
                    <MenuItem onClick={() => { setMenuOpen(false); navigate('/settings'); }}>
                      ⚙ {t('user.settings')}
                    </MenuItem>
                  </>
                )}
                <MenuItem onClick={() => { setMenuOpen(false); setShowChangePw(true); }}>
                  ⚿ {t('user.changePassword')}
                </MenuItem>
                <MenuItem onClick={() => { setMenuOpen(false); logout(); }}>
                  ⏻ {t('user.signOut')}
                </MenuItem>
              </div>
            )}
          </div>
        )}
        {showLabels && health && <div id="nav-footer">{health}</div>}

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

// NavGroupBlock renders one collapsible group — a small header
// row with chevron + group name, then the child Link rows when
// expanded. When the sidebar is in collapsed (icon-only) mode
// we hide the header line and just render every group's
// children stacked since the chevron interaction makes no
// sense at 56px wide.
function NavGroupBlock({
  titleKey, items, isOpen, onToggle, showLabels, pathname, openProblems, t,
}: {
  titleKey: string;
  items: NavItem[];
  isOpen: boolean;
  onToggle: () => void;
  showLabels: boolean;
  pathname: string;
  openProblems: number;
  t: (key: string) => string;
}) {
  // Icon-only sidebar: skip the group header (no place for it),
  // render every link inline. Operator still navigates by icon
  // memory in this mode.
  if (!showLabels) {
    return (
      <>
        {items.map(n => (
          <Link key={n.href} to={n.href}
            className={isActive(pathname, n.href) ? 'active' : ''}
            title={t(n.label)}
            style={{ justifyContent: 'center', padding: '10px 0' }}>
            <span className="icon"><n.icon size={16} strokeWidth={1.75} /></span>
            {n.href === '/problems' && openProblems > 0 && (
              <span className="nav-dot" title={`${openProblems} open problems`} />
            )}
          </Link>
        ))}
      </>
    );
  }
  // Empty titleKey = ungrouped (no header, no chevron, always
  // expanded). v0.5.214 lifts Inbox out of Triage to top-level.
  if (titleKey === '') {
    return (
      <>
        {items.map(n => (
          <Link key={n.href} to={n.href}
            className={isActive(pathname, n.href) ? 'active' : ''}>
            <span className="icon"><n.icon size={16} strokeWidth={1.75} /></span>
            <span className="nav-label">{t(n.label)}</span>
            {n.href === '/problems' && openProblems > 0 && (
              <span className="nav-badge">{openProblems}</span>
            )}
          </Link>
        ))}
      </>
    );
  }
  return (
    <div className="nav-group">
      <button type="button"
        onClick={onToggle}
        className="nav-group-header"
        aria-expanded={isOpen}
        style={{
          display: 'flex', alignItems: 'center', width: '100%',
          // Mirror the link rows' geometry so the header
          // title lines up with the labels below — same
          // 16px left padding, same 10px gap, same 16px
          // icon column. Without this the header text sat
          // 14px left of every link label and the eye read
          // it as a misalignment during fast triage scans.
          padding: '10px 16px 4px',
          gap: 10,
          background: 'transparent', border: 'none',
          color: 'var(--text2)', fontSize: 12, fontWeight: 700,
          letterSpacing: '0.5px', textTransform: 'uppercase',
          cursor: 'pointer', textAlign: 'left',
        }}>
        <span style={{
          width: 16, display: 'inline-block', textAlign: 'center',
          color: 'var(--text3)',
          flexShrink: 0,
        }}>
          {isOpen ? '▾' : '▸'}
        </span>
        <span>{t(titleKey)}</span>
      </button>
      {isOpen && items.map(n => (
        <Link key={n.href} to={n.href}
          className={isActive(pathname, n.href) ? 'active' : ''}>
          <span className="icon"><n.icon size={16} strokeWidth={1.75} /></span>
          <span className="nav-label">{t(n.label)}</span>
          {n.href === '/problems' && openProblems > 0 && (
            <span className="nav-badge">{openProblems}</span>
          )}
        </Link>
      ))}
    </div>
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
  // v0.8.9 — the System entry stays active across all its sub-nav tabs.
  if (href === '/system'     && pathname.startsWith('/system'))    return true;
  return pathname === href || pathname === href + '/';
}
