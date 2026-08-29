import { Component, type ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { RouteSkeleton } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// RouteSkeleton is the Suspense fallback for a code-split route; it reads
// useLocation() to pick a skeleton SHAPE for the destination route. It needs
// a Router from the SAME react-router copy the shipped bundle inlined —
// cfg.extraEntries: ["react-router-dom"] puts MemoryRouter on the bundle
// global so this import shims to it (see .design-sync/learnings/C.md).
// Until then the boundary below reports the missing-router error honestly.

class RouterBoundary extends Component<{ children: ReactNode }, { err: string | null }> {
  state = { err: null as string | null };
  static getDerivedStateFromError(e: unknown) { return { err: String((e as Error)?.message ?? e) }; }
  render() {
    if (this.state.err) {
      return <div style={{ color: 'var(--err)', fontSize: 'var(--fs-sm)' }}>⚠ {this.state.err} — RouteSkeleton needs a Router from the bundle's own react-router copy (cfg.extraEntries: react-router-dom).</div>;
    }
    return this.props.children;
  }
}

function At({ path }: { path: string }) {
  return (
    <Frame style={{ padding: 0, background: 'var(--bg0)' }}>
      <RouterBoundary>
        <MemoryRouter initialEntries={[path]}>
          <RouteSkeleton />
        </MemoryRouter>
      </RouterBoundary>
    </Frame>
  );
}

// List routes (/services, /traces, /problems …) → header row + 12×6 table skeleton.
export function ListRoute() { return <At path="/services" />; }

// /metrics, /dashboards → 4-column card grid (8 KPI tiles).
export function GridRoute() { return <At path="/dashboards/payments" />; }

// Everything else (detail pages) → 5 KPI tiles + 3 panels.
export function DetailRoute() { return <At path="/service/payments-orchestrator" />; }
