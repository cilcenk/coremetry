// endpointParam — v0.8.360 (Stage-2 slice E2). Pure helpers for the
// /endpoints detail drawer:
//
//   • encode/decodeEndpointParam — the `?endpoint=` URL codec. URL is
//     the source of truth for the open drawer (house rule §4): the
//     param encodes (service, path, sig) as
//     `<enc(service)>|<enc(path)>[|sig]` — each field is
//     URI-component-encoded FIRST so a literal `|` inside a path can
//     never forge a field boundary. `sig` marks the path as an
//     ID-collapsed signature (/orders/:id — the table's "group by
//     shape" mode), baked into the param so a copied link reproduces
//     the same drill-down regardless of the viewer's toggle state.
//   • trimHistogram — strips the all-zero leading/trailing bins off
//     the backend's fixed 28-bin log-scale distribution so the chart
//     zooms on the populated latency range.

export interface EndpointRef {
  service: string;
  path: string;
  sig: boolean;
}

export function encodeEndpointParam(ref: EndpointRef): string {
  return (
    encodeURIComponent(ref.service) +
    '|' +
    encodeURIComponent(ref.path) +
    (ref.sig ? '|sig' : '')
  );
}

// decodeEndpointParam parses a raw `?endpoint=` value. Returns null for
// anything malformed (missing field, bad escape) — the drawer simply
// stays closed on a garbage deep-link instead of crashing.
export function decodeEndpointParam(raw: string | null): EndpointRef | null {
  if (!raw) return null;
  const parts = raw.split('|');
  if (parts.length < 2 || parts.length > 3 || !parts[0] || !parts[1]) return null;
  if (parts.length === 3 && parts[2] !== 'sig') return null;
  try {
    return {
      service: decodeURIComponent(parts[0]),
      path: decodeURIComponent(parts[1]),
      sig: parts[2] === 'sig',
    };
  } catch {
    return null; // malformed %-escape
  }
}

// trimHistogram — returns the [bins, counts] slice between the first
// and last non-zero count (inclusive), so the fixed 0.1ms→1Ms grid
// renders only its populated range. All-zero input → empty arrays
// (caller shows the section's empty state).
export function trimHistogram(
  bins: number[],
  counts: number[],
): { bins: number[]; counts: number[] } {
  const n = Math.min(bins.length, counts.length);
  let first = -1;
  let last = -1;
  for (let i = 0; i < n; i++) {
    if (counts[i] > 0) {
      if (first === -1) first = i;
      last = i;
    }
  }
  if (first === -1) return { bins: [], counts: [] };
  return { bins: bins.slice(first, last + 1), counts: counts.slice(first, last + 1) };
}
