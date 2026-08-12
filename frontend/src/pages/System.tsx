import { lazy, Suspense, type ComponentType } from 'react';
import { useParams, Navigate, NavLink } from 'react-router-dom';
import { useAuth } from '@/components/AuthProvider';
import { Spinner } from '@/components/Spinner';
import { Topbar } from '@/components/Topbar';
import { useUrlRange } from '@/lib/useUrlRange';

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
  // v0.9.982 — bu sekme topbar'da zaman aralığı seçicisi istiyor mu.
  // Kabuk yukarı taşınırken (D4) sekme gövdeleri kendi `<Topbar>`larını
  // bıraktı; Query sekmesininki TEK İŞLEVSEL olanıydı (range taşıyordu).
  // Prop geçmeye gerek yok: `useUrlRange` URL'i kaynak alıyor, yani kabuk
  // ve sekme aynı `?range=` değerini okuyor — iki ayrı çağrı, tek gerçek.
  needsRange?: boolean;
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
  { slug: 'query',       label: 'Query',         Comp: AdminQuery,       adminOnly: true, needsRange: true },
  { slug: 'status-page', label: 'Status Page',   Comp: AdminStatusPage,  adminOnly: true },
];

export default function System() {
  const { tab } = useParams<{ tab: string }>();
  const { user } = useAuth();
  // Koşulsuz çağrı (hook kuralı); yalnız KULLANIMI koşullu. Varsayılan
  // AdminQuery'nin kendi varsayılanıyla aynı olmak ZORUNDA, aksi hâlde
  // picker sekmenin gerçekte sorguladığı pencereden farklı görünür.
  const [range, setRange] = useUrlRange('30m');
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
  // v0.9.982 (denetim D4) — KABUK ÇATALLANMASI KAPANDI.
  //
  // Buraya kadar `/system/*` depodaki TEK rota ailesiydi ki `#topbar` +
  // `#content` çiftini SAYFA seviyesinde basmıyordu; onu on ayrı sekme
  // gövdesi kendi içinde basıyordu. Dört somut sonucu vardı:
  //   · `#topbar` `.sys-content`in İÇİNDE blok eleman → içerikle KAYIYOR
  //     (diğer 40+ rotada `#main`in doğrudan flex çocuğu, yani sabit).
  //   · `#content`in `flex:1` + `overflow:auto`su etkisiz (ebeveyn blok)
  //     → sekme hiç kaydırmıyor, scrollport `#main`e çıkıyor.
  //   · `#topbar` yalnız sağ kolon genişliğinde (188px subnav + 18px gap
  //     kadar dar).
  //   · `.sys-layout` `#main`in doğrudan çocuğu olduğu için `#content`in
  //     20px padding'i yok → subnav sol kenara yapışık, sekme gövdesi
  //     20px içeride (hizasız).
  // `/settings/:section` bunu ZATEN doğru yapıyordu (Settings.tsx) —
  // şablon oradan alındı.
  return (
    <>
      <Topbar title="System"
        {...(active.needsRange ? { range, onRangeChange: setRange } : {})} />
      <div id="content">
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
      </div>
    </>
  );
}
