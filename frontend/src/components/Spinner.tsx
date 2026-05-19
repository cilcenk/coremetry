export function Spinner() { return <div className="spinner" />; }

// PageLoader — v0.5.262. Full-page centered loader used as the
// Suspense fallback for lazy routes AND as the auth-loading
// state in AppShell. Replaces the tiny top-left spinner that
// landed on every initial page load (because the route bundle
// hadn't been fetched yet) — the inline 14px spinner read as
// "stuck" since it appeared in document-flow corner, not as a
// loading state. Full-page centered OTel mark + ring reads as
// "actively loading" the same way Datadog / Honeycomb / Grafana
// land their splash loaders.
export function PageLoader({ label }: { label?: string }) {
  return (
    <div role="status" aria-busy="true" aria-label={label ?? 'Loading'}
      style={{
        position: 'fixed', inset: 0, zIndex: 30,
        display: 'grid', placeItems: 'center',
        background: 'var(--bg)',
      }}>
      <div style={{
        display: 'flex', flexDirection: 'column',
        alignItems: 'center', gap: 16,
      }}>
        {/* Animated ring sized to wrap the OTel mark — same
            stroke as the inline .spinner so the visual identity
            stays consistent across the app's load surfaces. */}
        <div style={{
          position: 'relative', width: 72, height: 72,
          display: 'grid', placeItems: 'center',
        }}>
          <div style={{
            position: 'absolute', inset: 0,
            border: '2px solid var(--border)',
            borderTopColor: 'var(--accent)',
            borderRadius: '50%',
            animation: 'spin 0.9s linear infinite',
          }} />
          <img src="/opentelemetry.svg" width={40} height={40}
            alt="OpenTelemetry"
            style={{ display: 'block' }} />
        </div>
        <div style={{
          fontSize: 12, color: 'var(--text3)',
          letterSpacing: 0.4, textTransform: 'uppercase', fontWeight: 600,
        }}>
          {label ?? 'Loading'}
        </div>
      </div>
    </div>
  );
}

// Empty state — accepts either a glyph string (◫, ⚠, ⋮ — the
// CLI-style geometric shapes already in use across the app) or an
// SVG icon node from `components/icons`. Using ReactNode keeps the
// callers backward-compatible without forcing a sweep of every
// existing Empty.
export function Empty({ icon, title, children }: {
  icon: React.ReactNode; title: string; children?: React.ReactNode;
}) {
  return (
    <div className="empty">
      <div className="icon">{icon}</div>
      <h3>{title}</h3>
      {children && <p>{children}</p>}
    </div>
  );
}
