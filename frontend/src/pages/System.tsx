import { lazy, Suspense, type ComponentType } from 'react';
import { useParams, Navigate, NavLink } from 'react-router-dom';
import { useAuth } from '@/components/AuthProvider';
import { Spinner } from '@/components/Spinner';

// System — the consolidated admin area (v0.8.9). The ten former /admin/*
// pages are re-homed here as tabbed sub-views behind a single left vertical
// sub-nav, so the global sidebar carries ONE "System" entry instead of ten.
// Each page's body is unchanged — it's lazy-loaded and mounted inside the
// shell, exactly as App.tsx used to route it. Deep links live at
// /system/<slug>; old /admin/<slug> URLs redirect here (see App.tsx).

// Lazy per-tab so the System chunk stays small and each admin page still
// code-splits (same dynamic-import specifiers App.tsx used → same chunks).
const AdminStats       = lazy(() => import('./AdminStats'));
const AdminClickhouse  = lazy(() => import('./AdminClickhouse'));
const AdminElastic     = lazy(() => import('./AdminElastic'));
const AdminCluster     = lazy(() => import('./AdminCluster'));
const AdminCardinality = lazy(() => import('./AdminCardinality'));
const AdminCatalog     = lazy(() => import('./AdminCatalog'));
const AdminAudit       = lazy(() => import('./AdminAudit'));
const AdminSql         = lazy(() => import('./AdminSql'));
const AdminQuery       = lazy(() => import('./AdminQuery'));
const AdminStatusPage  = lazy(() => import('./AdminStatusPage'));

interface SysTab {
  slug: string;
  label: string;
  Comp: ComponentType;
  // adminOnly tabs are hidden in the sub-nav for non-admins (the System
  // overview/stats tab stays visible to everyone, matching the prior sidebar).
  adminOnly: boolean;
}

const TABS: SysTab[] = [
  { slug: 'stats',       label: 'Overview',      Comp: AdminStats,       adminOnly: false },
  { slug: 'clickhouse',  label: 'ClickHouse',    Comp: AdminClickhouse,  adminOnly: true },
  { slug: 'elastic',     label: 'Elasticsearch', Comp: AdminElastic,     adminOnly: true },
  { slug: 'cluster',     label: 'Cluster',       Comp: AdminCluster,     adminOnly: true },
  { slug: 'cardinality', label: 'Cardinality',   Comp: AdminCardinality, adminOnly: true },
  { slug: 'catalog',     label: 'Catalog',       Comp: AdminCatalog,     adminOnly: true },
  { slug: 'audit',       label: 'Audit Log',     Comp: AdminAudit,       adminOnly: true },
  { slug: 'sql',         label: 'SQL Console',   Comp: AdminSql,         adminOnly: true },
  { slug: 'query',       label: 'Query',         Comp: AdminQuery,       adminOnly: true },
  { slug: 'status-page', label: 'Status Page',   Comp: AdminStatusPage,  adminOnly: true },
];

export default function System() {
  const { tab } = useParams<{ tab: string }>();
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const visible = TABS.filter(t => !t.adminOnly || isAdmin);

  // Bare /system, an unknown slug, or an admin-only slug for a non-admin →
  // redirect to the first tab the user can see.
  const active = visible.find(t => t.slug === tab);
  if (!active) {
    const fallback = visible[0];
    return fallback ? <Navigate to={`/system/${fallback.slug}`} replace /> : <Navigate to="/" replace />;
  }

  const Body = active.Comp;
  return (
    <div className="sys-layout">
      <nav className="sys-subnav" aria-label="System sections">
        <div className="sys-subnav-title">System</div>
        {visible.map(t => (
          <NavLink
            key={t.slug}
            to={`/system/${t.slug}`}
            className={({ isActive }) => 'sys-subnav-item' + (isActive ? ' active' : '')}>
            {t.label}
          </NavLink>
        ))}
      </nav>
      <div className="sys-content">
        <Suspense fallback={<Spinner />}>
          <Body />
        </Suspense>
      </div>
    </div>
  );
}
