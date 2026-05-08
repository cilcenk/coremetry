// OpenTelemetry brand mark — renders the actual OpenTelemetry SVG
// shipped with the app at /opentelemetry.svg (sourced from the OTel
// brand assets and copied into frontend/public/). Using the file
// directly preserves the official two-tone composition without
// reapproximating it inline.
//
// `color` is kept as a no-op prop for backward compatibility with
// the previous inline-path version — the SVG asset is multi-coloured
// and overriding it on the fly would mean re-fetching / inlining.
// Callers that need a mono override can pass their own component
// or use CSS filters.
export function TelescopeIcon({ size = 22, title = 'OpenTelemetry' }: {
  size?: number;
  /** Kept for backward-compat; ignored — SVG asset is fixed-colour. */
  color?: string;
  title?: string;
}) {
  return (
    <img
      src="/opentelemetry.svg"
      width={size}
      height={size}
      alt={title}
      title={title}
      style={{ display: 'inline-block', verticalAlign: 'middle' }}
    />
  );
}
