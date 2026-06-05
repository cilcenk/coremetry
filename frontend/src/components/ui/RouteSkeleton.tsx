import { useLocation } from 'react-router-dom';
import { Skeleton, TableSkeleton, CardSkeleton } from '../Skeleton';

// RouteSkeleton — the Suspense fallback shown while a code-split route chunk
// loads (v0.8.6 Phase 0). Location-aware: it picks a skeleton SHAPE matching
// the destination route so the loading state already resembles the page (list
// routes → table; metrics/dashboards → card grid; detail routes → KPI row +
// panels) instead of a centered spinner. That cuts layout shift (better CLS)
// and makes the nav feel instant. Tokens only — reuses the shared Skeleton
// atoms. It renders inside BrowserRouter (from main.tsx) so useLocation is safe
// even though it sits outside <Routes>.

const TABLE_ROUTES = [
  '/services', '/traces', '/endpoints', '/databases', '/messaging', '/logs',
  '/incidents', '/inbox', '/problems', '/anomalies', '/events', '/alerts',
  '/monitors', '/slos', '/runbooks', '/users', '/admin',
];
const GRID_ROUTES = ['/metrics', '/dashboards', '/dashboard'];

export function RouteSkeleton() {
  const { pathname } = useLocation();
  const isTable = TABLE_ROUTES.some(p => pathname === p || pathname.startsWith(p + '/'));
  const isGrid = GRID_ROUTES.some(p => pathname === p || pathname.startsWith(p));

  return (
    <div style={{ padding: 22 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Skeleton width={200} height={20} />
        <Skeleton width={120} height={28} />
      </div>
      {isGrid ? (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 16 }}>
          {Array.from({ length: 8 }).map((_, i) => <CardSkeleton key={i} height={140} />)}
        </div>
      ) : isTable ? (
        <TableSkeleton rows={12} cols={6} />
      ) : (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(5, 1fr)', gap: 16, marginBottom: 16 }}>
            {Array.from({ length: 5 }).map((_, i) => <CardSkeleton key={i} height={96} />)}
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16 }}>
            {Array.from({ length: 3 }).map((_, i) => <CardSkeleton key={i} height={160} />)}
          </div>
        </>
      )}
    </div>
  );
}
