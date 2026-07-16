// ClusterChips — small reusable component that surfaces the
// k8s/openshift cluster names a Problem / Anomaly / Incident's
// firing service was active in. Read-time enrichment from the
// backend (no DB column), so this works retroactively across
// the entire problems / incidents history.
//
// Each chip click-throughs to /services?cluster=<name> so an
// oncall triaging "did this fire on prod-eu-west or
// prod-us-east?" can pivot to the rest of the affected
// cluster's services in one click.
//
// Rendered as a same-line trailing element next to the
// service name in the list rows, so existing layouts don't
// reflow. Renders nothing when no clusters are attached
// (single-cluster deployments or services without cluster
// resource attrs).

export function ClusterChips({ clusters }: { clusters?: string[] }) {
  if (!clusters || clusters.length === 0) return null;
  return (
    <span style={{
      display: 'inline-flex', flexWrap: 'wrap', gap: 4,
      marginLeft: 6, verticalAlign: 'middle',
    }}>
      {clusters.map(c => (
        <a key={c} href={`/services?cluster=${encodeURIComponent(c)}`}
          title={`Show /services scoped to ${c}`}
          style={{
            fontSize: 10, padding: '1px 6px', borderRadius: 3, fontWeight: 600,
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
            background: 'color-mix(in srgb, var(--accent) 15%, transparent)',
            color: 'var(--accent2)',
            border: '1px solid color-mix(in srgb, var(--accent) 30%, transparent)',
            textDecoration: 'none',
            textTransform: 'uppercase', letterSpacing: '.3px',
          }}>{c}</a>
      ))}
    </span>
  );
}
