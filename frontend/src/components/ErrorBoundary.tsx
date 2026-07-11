import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button } from '@/components/ui';

// ErrorBoundary — last-resort guard around the route tree.
// React doesn't have a hooks-based equivalent, so this remains
// a class component (the only one in the app). When a JSX
// render below this throws, the boundary catches it and shows
// a recoverable error screen instead of the white-page-of-
// death every operator dreads in production observability
// tools.
//
// The fallback gives three escape hatches:
//   • Reload the page entirely (recovers from chunk-load
//     failures + most state corruption).
//   • Go back to home (clears the offending route).
//   • Open the issue tracker (in case the operator wants to
//     report a reproducible crash).
//
// We log the error to console so the developer hitting it
// during local work has the stack; in production a user-side
// reporter (Sentry / our own /api/errors) could be added at
// componentDidCatch.

interface Props { children: ReactNode }
interface State { error: Error | null }

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // v0.5.328 — Operator-reported (prod): "failed to fetch
    // dynamically imported module" landed users on the generic
    // error screen after a deploy. The tab's index.html still
    // references the OLD chunk hashes; CI just pushed NEW
    // chunks. Vite's hashed filenames + immutable cache mean
    // the old chunk URL 404s.
    //
    // Detect that specific signature and reload the page once.
    // window.location.reload() pulls fresh index.html with the
    // new chunk filenames. Guard with sessionStorage so a real
    // bug that mimics the message doesn't trap the user in a
    // reload loop.
    const msg = error?.message || '';
    const isChunkLoadError =
      /Failed to fetch dynamically imported module/i.test(msg) ||
      /Loading chunk \d+ failed/i.test(msg) ||
      /Importing a module script failed/i.test(msg);
    if (isChunkLoadError) {
      const FLAG = 'coremetry_chunk_reload_attempt';
      if (sessionStorage.getItem(FLAG) !== '1') {
        sessionStorage.setItem(FLAG, '1');
        // eslint-disable-next-line no-console
        console.warn('[ErrorBoundary] stale chunk after deploy — reloading');
        window.location.reload();
        return;
      }
      // Already tried once this session; fall through to the
      // visible error screen rather than loop.
    }
    // Console is the floor. A future ship can wire this to
    // POST /api/errors so the SaaS deploy collects breakage
    // signals in production without operators copy-pasting
    // stacks back to GitHub. Intentionally not adding the
    // endpoint right now — the noise / privacy questions are
    // their own ship.
    // eslint-disable-next-line no-console
    console.error('[ErrorBoundary]', error, info.componentStack);
  }

  reset = () => {
    this.setState({ error: null });
  };

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div style={{
        display: 'flex', flexDirection: 'column',
        alignItems: 'center', justifyContent: 'center',
        minHeight: '60vh',
        gap: 14, padding: 24, textAlign: 'center',
      }}>
        <div style={{
          width: 64, height: 64,
          borderRadius: '50%',
          background: 'rgba(232,78,78,0.10)',
          display: 'grid', placeItems: 'center',
          color: 'var(--err)', fontSize: 28, fontWeight: 700,
        }}>!</div>
        <h2 style={{ margin: 0, fontSize: 18 }}>Something went wrong</h2>
        <div style={{ fontSize: 13, color: 'var(--text2)', maxWidth: 540, lineHeight: 1.55 }}>
          A page-render error was caught by the error boundary.
          The rest of Coremetry is fine — try one of the
          recovery options below. If this reproduces reliably
          please open an issue with the steps.
        </div>
        <pre style={{
          margin: 0, padding: '8px 12px',
          background: 'var(--bg2)',
          border: '1px solid var(--border)',
          borderRadius: 6,
          fontSize: 11, color: 'var(--text2)',
          maxWidth: 720, overflow: 'auto',
          textAlign: 'left',
        }}>
{error.name}: {error.message}
        </pre>
        <div style={{ display: 'flex', gap: 8 }}>
          <Button onClick={() => { this.reset(); window.location.reload(); }}>
            Reload page
          </Button>
          <Button variant="secondary"
                  onClick={() => { this.reset(); window.location.assign('/'); }}>
            Go to home
          </Button>
          <a className="sec"
             href="https://github.com/cilcenk/coremetry/issues"
             target="_blank" rel="noopener"
             style={{
               padding: '6px 14px', fontSize: 13,
               textDecoration: 'none',
               border: '1px solid var(--border)',
               borderRadius: 6, color: 'var(--text)',
             }}>
            Report issue ↗
          </a>
        </div>
      </div>
    );
  }
}
