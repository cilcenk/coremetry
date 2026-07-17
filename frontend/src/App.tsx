import { lazy, Suspense, useEffect } from 'react';
import { Routes, Route, Navigate, useParams, useLocation } from 'react-router-dom';
import { AuthProvider } from './components/AuthProvider';
import { AppShell } from './components/AppShell';
import { ErrorBoundary } from './components/ErrorBoundary';
import { RouteSkeleton } from './components/ui/RouteSkeleton';
import { PerfMeter } from './components/perf/PerfMeter';
import { usePrefetchOnHover, prefetchRoute } from './lib/perf/routePrefetch';

// All route components are code-split via React.lazy so the
// initial bundle stays small. The loader fallback is the
// existing Spinner — same UX as Next.js's automatic per-route
// chunking. Each `import('./pages/Foo')` becomes its own chunk
// in dist/assets/.
const Login             = lazy(() => import('./pages/Login'));
const Services          = lazy(() => import('./pages/Services'));
const Service           = lazy(() => import('./pages/Service'));
const ServiceBacktrace  = lazy(() => import('./pages/ServiceBacktrace'));
// v0.8.219 — /topology retired; /service-map is the single topology surface
// (it carries the focus deep-link via ?focus=). /topology redirects to it.
const ServiceMap        = lazy(() => import('./pages/ServiceMap'));
const Traces            = lazy(() => import('./pages/Traces'));
const Trace             = lazy(() => import('./pages/Trace'));
const TraceCompare      = lazy(() => import('./pages/TraceCompare'));
const Logs              = lazy(() => import('./pages/Logs'));
const Metrics           = lazy(() => import('./pages/Metrics'));
const Endpoints         = lazy(() => import('./pages/Endpoints'));
const Explore           = lazy(() => import('./pages/Explore'));
const Runbooks          = lazy(() => import('./pages/Runbooks'));
const Runbook           = lazy(() => import('./pages/Runbook'));
const RunbookExecution  = lazy(() => import('./pages/RunbookExecution'));
const Databases         = lazy(() => import('./pages/Databases'));
const External          = lazy(() => import('./pages/External'));
const Hosts             = lazy(() => import('./pages/Hosts'));
const Clusters          = lazy(() => import('./pages/Clusters'));
const SlowQueries       = lazy(() => import('./pages/SlowQueries'));
const Messaging         = lazy(() => import('./pages/Messaging'));
const Dashboards        = lazy(() => import('./pages/Dashboards'));
const Dashboard         = lazy(() => import('./pages/Dashboard'));
const Events            = lazy(() => import('./pages/Events'));
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
const PublicStatus      = lazy(() => import('./pages/PublicStatus'));
const PublicTrace       = lazy(() => import('./pages/PublicTrace'));
// v0.8.9 — the ten /admin/* pages are consolidated into one System area
// (lazy-loaded per-tab inside pages/System.tsx, not routed individually here).
const System            = lazy(() => import('./pages/System'));

// Each lazy module's default export is the page component.
// React Router doesn't enforce any naming convention beyond
// "default export is a React component", so the convention is
// preserved verbatim from the Next.js app router structure —
// just the file paths changed.
// AdminRedirect bounces an old /admin/<slug> deep link to its new
// /system/<slug> home so nothing 404s after the v0.8.9 consolidation. The
// slug is 1:1 (clickhouse, stats, audit, status-page, …).
function AdminRedirect() {
  const { tab } = useParams<{ tab: string }>();
  return <Navigate to={`/system/${tab ?? 'stats'}`} replace />;
}

// v0.8.219 — /topology was retired in favour of /service-map. Redirect preserves
// the query string so /topology?focus=<svc> lands focused on the new surface.
function TopologyRedirect() {
  const loc = useLocation();
  return <Navigate to={`/service-map${loc.search}`} replace />;
}

// v0.8.477 (perf dalga-2) — '/' açılışı: eski lazy Home chunk'ı 203
// bayt için 1 RTT'lik SERİ zincir ödetiyordu (entry → Home fetch →
// Navigate → Services chunk'ları). Statik redirect zinciri koparır;
// mount'ta Services chunk'ı idle'da ısıtılır ki yönlendirme anında
// koda çarpsın.
function HomeRedirect() {
  useEffect(() => { prefetchRoute('/services'); }, []);
  return <Navigate to="/services" replace />;
}

export default function App() {
  // Intent-prefetch: warm a route's code-split chunk when the operator hovers
  // any internal link (one delegated listener; no nav markup touched). v0.8.6.
  usePrefetchOnHover();
  return (
    <ErrorBoundary>
    <AuthProvider>
      <PerfMeter />
      <Suspense fallback={<RouteSkeleton />}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/"               element={<HomeRedirect />} />
            <Route path="/login"          element={<Login />} />
            <Route path="/services"       element={<Services />} />
            <Route path="/service"        element={<Service />} />
            <Route path="/service/backtrace" element={<ServiceBacktrace />} />
            <Route path="/service-map"    element={<ServiceMap />} />
            {/* v0.8.219 — /topology retired; redirect to the single /service-map
                surface, preserving ?focus= so deep-links land focused. */}
            <Route path="/topology"       element={<TopologyRedirect />} />
            <Route path="/traces"         element={<Traces />} />
            <Route path="/trace"          element={<Trace />} />
            <Route path="/trace/compare"  element={<TraceCompare />} />
            <Route path="/logs"           element={<Logs />} />
            <Route path="/metrics"        element={<Metrics />} />
            <Route path="/endpoints"      element={<Endpoints />} />
            <Route path="/explore"        element={<Explore />} />
            <Route path="/runbooks"      element={<Runbooks />} />
            <Route path="/runbook"       element={<Runbook />} />
            <Route path="/runbook-exec"  element={<RunbookExecution />} />
            <Route path="/databases"      element={<Databases />} />
            <Route path="/databases/slow-queries" element={<SlowQueries />} />
            <Route path="/external"       element={<External />} />
            <Route path="/hosts"          element={<Hosts />} />
            <Route path="/clusters"       element={<Clusters />} />
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
            <Route path="/events"         element={<Events />} />
            <Route path="/profiling"      element={<Profiling />} />
            <Route path="/ai"             element={<AIObservability />} />
            <Route path="/profile"        element={<Profile />} />
            {/* v0.8.13 — Settings decomposed into a /settings/:section area. */}
            <Route path="/settings"          element={<Navigate to="/settings/smtp" replace />} />
            <Route path="/settings/:section" element={<Settings />} />
            <Route path="/users"          element={<Users />} />
            {/* v0.8.482 — /errors eski linkleri tek atlamada /problems'a iner
                (önceden 13 satırlık Spinner'lı redirect sayfası + ayrı chunk'tı;
                /status v457 emsali). */}
            <Route path="/errors"         element={<Navigate to="/problems" replace />} />
            {/* v0.8.457 — eski /status yer imleri tek atlamada /system/stats'e
                iner (önceden Status.tsx → /admin/stats → AdminRedirect üç duraktı). */}
            <Route path="/status"         element={<Navigate to="/system/stats" replace />} />
            <Route path="/public-status"  element={<PublicStatus />} />
            <Route path="/public/trace"   element={<PublicTrace />} />
            {/* Consolidated System area (v0.8.9). Ten former /admin/*
                pages are now tabs inside <System>; old links redirect. */}
            <Route path="/system"      element={<Navigate to="/system/stats" replace />} />
            <Route path="/system/:tab" element={<System />} />
            <Route path="/admin/:tab"  element={<AdminRedirect />} />
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
