import { lazy, Suspense } from 'react';
import { Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider } from './components/AuthProvider';
import { AppShell } from './components/AppShell';
import { ErrorBoundary } from './components/ErrorBoundary';
import { Spinner } from './components/Spinner';

// All route components are code-split via React.lazy so the
// initial bundle stays small. The loader fallback is the
// existing Spinner — same UX as Next.js's automatic per-route
// chunking. Each `import('./pages/Foo')` becomes its own chunk
// in dist/assets/.
const Home              = lazy(() => import('./pages/Home'));
const Login             = lazy(() => import('./pages/Login'));
const Services          = lazy(() => import('./pages/Services'));
const Service           = lazy(() => import('./pages/Service'));
const ServiceBacktrace  = lazy(() => import('./pages/ServiceBacktrace'));
const Topology          = lazy(() => import('./pages/Topology'));
const Traces            = lazy(() => import('./pages/Traces'));
const Trace             = lazy(() => import('./pages/Trace'));
const TraceCompare      = lazy(() => import('./pages/TraceCompare'));
const Logs              = lazy(() => import('./pages/Logs'));
const Metrics           = lazy(() => import('./pages/Metrics'));
const Explore           = lazy(() => import('./pages/Explore'));
const Notebook          = lazy(() => import('./pages/Notebook'));
const Databases         = lazy(() => import('./pages/Databases'));
const SlowQueries       = lazy(() => import('./pages/SlowQueries'));
const Messaging         = lazy(() => import('./pages/Messaging'));
const Dashboards        = lazy(() => import('./pages/Dashboards'));
const Dashboard         = lazy(() => import('./pages/Dashboard'));
const Incidents         = lazy(() => import('./pages/Incidents'));
const Incident          = lazy(() => import('./pages/Incident'));
// Problems page: assignable exception inbox at top, alert-rule
// firings beneath. /exceptions is kept as a silent redirect so
// older shared links don't 404. /anomalies is its own
// observation-only page (live streams + 24h history).
const Problems          = lazy(() => import('./features/anomalies'));
const Anomalies         = lazy(() => import('./features/anomalies/AnomalyStreamsPage'));
const Inbox             = lazy(() => import('./pages/Inbox'));
const Alerts            = lazy(() => import('./pages/Alerts'));
const Slos              = lazy(() => import('./pages/Slos'));
const Monitors          = lazy(() => import('./pages/Monitors'));
const Profiling         = lazy(() => import('./pages/Profiling'));
const AIObservability   = lazy(() => import('./pages/AIObservability'));
const Profile           = lazy(() => import('./pages/Profile'));
const Settings          = lazy(() => import('./pages/Settings'));
const Users             = lazy(() => import('./pages/Users'));
const ErrorsPage        = lazy(() => import('./pages/Errors'));
const Status            = lazy(() => import('./pages/Status'));
const PublicStatus      = lazy(() => import('./pages/PublicStatus'));
const PublicTrace       = lazy(() => import('./pages/PublicTrace'));
const AdminAudit        = lazy(() => import('./pages/AdminAudit'));
const AdminCardinality  = lazy(() => import('./pages/AdminCardinality'));
const AdminCatalog      = lazy(() => import('./pages/AdminCatalog'));
const AdminSql          = lazy(() => import('./pages/AdminSql'));
const AdminStats        = lazy(() => import('./pages/AdminStats'));
const AdminStatusPage   = lazy(() => import('./pages/AdminStatusPage'));
const AdminContracts    = lazy(() => import('./pages/AdminContracts'));
const AdminCluster      = lazy(() => import('./pages/AdminCluster'));

// Each lazy module's default export is the page component.
// React Router doesn't enforce any naming convention beyond
// "default export is a React component", so the convention is
// preserved verbatim from the Next.js app router structure —
// just the file paths changed.
export default function App() {
  return (
    <ErrorBoundary>
    <AuthProvider>
      <Suspense fallback={<Spinner />}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/"               element={<Home />} />
            <Route path="/login"          element={<Login />} />
            <Route path="/services"       element={<Services />} />
            <Route path="/service"        element={<Service />} />
            <Route path="/service/backtrace" element={<ServiceBacktrace />} />
            <Route path="/service-map"    element={<Topology />} />
            <Route path="/topology"       element={<Topology />} />
            <Route path="/traces"         element={<Traces />} />
            <Route path="/trace"          element={<Trace />} />
            <Route path="/trace/compare"  element={<TraceCompare />} />
            <Route path="/logs"           element={<Logs />} />
            <Route path="/metrics"        element={<Metrics />} />
            <Route path="/explore"        element={<Explore />} />
            <Route path="/notebook"       element={<Notebook />} />
            <Route path="/databases"      element={<Databases />} />
            <Route path="/databases/slow-queries" element={<SlowQueries />} />
            <Route path="/messaging"      element={<Messaging />} />
            <Route path="/dashboards"     element={<Dashboards />} />
            <Route path="/dashboard"      element={<Dashboard />} />
            <Route path="/incidents"      element={<Incidents />} />
            <Route path="/incident"       element={<Incident />} />
            <Route path="/inbox"          element={<Inbox />} />
            <Route path="/problems"       element={<Problems />} />
            <Route path="/anomalies"      element={<Anomalies />} />
            <Route path="/exceptions"     element={<Navigate to="/problems" replace />} />
            <Route path="/alerts"         element={<Alerts />} />
            <Route path="/slos"           element={<Slos />} />
            <Route path="/monitors"       element={<Monitors />} />
            <Route path="/profiling"      element={<Profiling />} />
            <Route path="/ai"             element={<AIObservability />} />
            <Route path="/profile"        element={<Profile />} />
            <Route path="/settings"       element={<Settings />} />
            <Route path="/users"          element={<Users />} />
            <Route path="/errors"         element={<ErrorsPage />} />
            <Route path="/status"         element={<Status />} />
            <Route path="/public-status"  element={<PublicStatus />} />
            <Route path="/public/trace"   element={<PublicTrace />} />
            <Route path="/admin/audit"        element={<AdminAudit />} />
            <Route path="/admin/cardinality"  element={<AdminCardinality />} />
            <Route path="/admin/catalog"      element={<AdminCatalog />} />
            <Route path="/admin/sql"          element={<AdminSql />} />
            <Route path="/admin/stats"        element={<AdminStats />} />
            <Route path="/admin/status-page"  element={<AdminStatusPage />} />
            <Route path="/admin/contracts"    element={<AdminContracts />} />
            <Route path="/admin/cluster"      element={<AdminCluster />} />
            {/* Unknown path → bounce to Home so navigate() never
                hits a dead route. The AuthProvider gates whether
                the user actually sees Home or gets redirected to
                /login. */}
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        </Routes>
      </Suspense>
    </AuthProvider>
    </ErrorBoundary>
  );
}
