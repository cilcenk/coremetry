// destinationParam — v0.8.364 (Stage-2 slice M1). Pure helpers for
// the /messaging topic detail drawer's `?destination=` URL param —
// URL is the source of truth for the open drawer (house rule §4).
// Mirrors the /endpoints `?endpoint=` codec (endpointParam.ts,
// v0.8.360): the param encodes the row's full identity
// (system, cluster, destination) as
// `<enc(system)>|<enc(cluster)>|<enc(destination)>` — each field is
// URI-component-encoded FIRST so a literal `|` inside a topic name
// (or a hostile deep-link) can never forge a field boundary.
// Cluster is always non-empty server-side ("(default)" for
// single-cluster installs), so all three fields are mandatory.

export interface DestinationRef {
  system: string;
  cluster: string;
  destination: string;
}

export function encodeDestinationParam(ref: DestinationRef): string {
  return (
    encodeURIComponent(ref.system) +
    '|' +
    encodeURIComponent(ref.cluster) +
    '|' +
    encodeURIComponent(ref.destination)
  );
}

// decodeDestinationParam parses a raw `?destination=` value. Returns
// null for anything malformed (wrong field count, empty field, bad
// %-escape) — the drawer simply stays closed on a garbage deep-link
// instead of crashing the page.
export function decodeDestinationParam(raw: string | null): DestinationRef | null {
  if (!raw) return null;
  const parts = raw.split('|');
  if (parts.length !== 3 || !parts[0] || !parts[1] || !parts[2]) return null;
  try {
    return {
      system: decodeURIComponent(parts[0]),
      cluster: decodeURIComponent(parts[1]),
      destination: decodeURIComponent(parts[2]),
    };
  } catch {
    return null; // malformed %-escape
  }
}
