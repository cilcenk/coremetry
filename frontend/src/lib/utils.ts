import type { MouseEvent as ReactMouseEvent } from 'react';
import type { TimeRange } from './types';

// Quick-range presets in seconds, ordered for the dropdown panel.
export const PRESET_SECONDS: Record<string, number> = {
  '5m':  300,    '15m': 900,    '30m': 1800,
  '1h':  3600,   '3h':  10800,  '6h':  21600,   '12h': 43200,
  '24h': 86400,  '2d':  172800, '7d':  604800,  '30d': 2592000,
};

export const PRESET_LABELS: Record<string, string> = {
  '5m':  'Last 5 minutes',   '15m': 'Last 15 minutes', '30m': 'Last 30 minutes',
  '1h':  'Last 1 hour',      '3h':  'Last 3 hours',    '6h':  'Last 6 hours',
  '12h': 'Last 12 hours',    '24h': 'Last 24 hours',   '2d':  'Last 2 days',
  '7d':  'Last 7 days',      '30d': 'Last 30 days',
};

// Converts a TimeRange to absolute nanosecond bounds for API queries.
export function timeRangeToNs(range: TimeRange): { from: number; to: number } {
  if (range.preset === 'custom' && range.fromMs && range.toMs) {
    return { from: range.fromMs * 1_000_000, to: range.toMs * 1_000_000 };
  }
  const secs = PRESET_SECONDS[range.preset] ?? 86400;
  const now = Date.now();
  return {
    from: Math.floor((now - secs * 1000) * 1_000_000),
    to: now * 1_000_000,
  };
}

// Compact label for the picker button.
export function timeRangeLabel(r: TimeRange): string {
  if (r.preset === 'custom' && r.fromMs && r.toMs) {
    const fmt = (ms: number) => new Date(ms).toLocaleString('en-GB',
      { dateStyle: 'short', timeStyle: 'short' });
    return `${fmt(r.fromMs)} → ${fmt(r.toMs)}`;
  }
  return PRESET_LABELS[r.preset] ?? r.preset;
}

export function fmtNum(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}

// escapeHTML — minimal HTML-entity escape for safely
// interpolating untrusted strings into innerHTML template
// literals (chart tooltips, etc.). Covers the five chars
// that change the parse tree (& < > " '). Span attributes
// are operator-controllable but ultimately a malicious
// ingester could ship `service.name = "<script>"` and trip
// XSS at render time; wrapping any interpolation site keeps
// the rendered string inert.
export function escapeHTML(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// Binary unit formatter — KiB / MiB / GiB / TiB. Hoisted from
// ServiceInfra.tsx where it was duplicated locally; the
// cardinality page also needs it for column-bytes columns.
export function fmtBytes(n: number): string {
  if (!n || n < 0) return '0';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0)} ${units[i]}`;
}

export function fmtNs(ns: number): string {
  const us = ns / 1e3, ms = ns / 1e6, s = ns / 1e9;
  if (s >= 1) return s.toFixed(2) + 's';
  if (ms >= 1) return ms.toFixed(2) + 'ms';
  if (us >= 1) return us.toFixed(0) + 'µs';
  return ns + 'ns';
}

export function tsShort(ns: number): string {
  if (!ns) return '—';
  const d = new Date(ns / 1e6);
  return d.toLocaleTimeString('en', { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0');
}

// v0.5.321 — Operator-reported: Traces page's Time column showed
// only HH:MM:SS.mmm (tsShort) — at wide windows the operator
// couldn't tell whether two rows were from the same day. Render
// the full ISO-ish stamp with date: "2026-05-21 10:23:45.123".
// Sortable string format, locale-stable, no DateStyle quirks.
export function tsDateTime(ns: number): string {
  if (!ns) return '—';
  const d = new Date(ns / 1e6);
  const yyyy = d.getFullYear();
  const mm   = String(d.getMonth() + 1).padStart(2, '0');
  const dd   = String(d.getDate()).padStart(2, '0');
  const hh   = String(d.getHours()).padStart(2, '0');
  const mi   = String(d.getMinutes()).padStart(2, '0');
  const ss   = String(d.getSeconds()).padStart(2, '0');
  const ms   = String(d.getMilliseconds()).padStart(3, '0');
  return `${yyyy}-${mm}-${dd} ${hh}:${mi}:${ss}.${ms}`;
}

export function tsLong(ns: number): string {
  if (!ns) return '—';
  return new Date(ns / 1e6).toLocaleString('en', { dateStyle: 'short', timeStyle: 'medium' });
}

// tsRel renders a unix-ns timestamp as a coarse relative duration
// ("in 12h", "3d ago"). Used for expiry indicators where the
// absolute date is less load-bearing than "how much longer". For
// past timestamps the suffix flips to " ago". Rounds to the
// largest unit that fits to keep the label tight.
export function tsRel(ns: number): string {
  if (!ns) return '—';
  const diffMs = ns / 1e6 - Date.now();
  const abs = Math.abs(diffMs);
  const past = diffMs < 0;
  const min = abs / 60_000;
  const h = abs / 3_600_000;
  const d = abs / 86_400_000;
  let body: string;
  if (d >= 1) body = Math.round(d) + 'd';
  else if (h >= 1) body = Math.round(h) + 'h';
  else if (min >= 1) body = Math.round(min) + 'm';
  else body = '<1m';
  return past ? body + ' ago' : 'in ' + body;
}

// Hand-picked palette tuned for modern APM dashboards. Replaces the
// previous Tailwind-default rainbow (indigo/violet/pink/orange/yellow/
// emerald/cyan…) which read as "candy crayons" on a dark UI. Every
// entry sits roughly in the same low-to-mid saturation band (35-50%)
// and similar perceptual lightness, so a trace with 6 services
// looks like a designer's palette rather than a child's crayon box.
//
// Order is hue-stepped so consecutive services in a hash collision
// chain don't end up neighbouring on the colour wheel — you still
// get visually distinct services even on small traces.
// Tempo-inspired hand-tuned palette. Slightly desaturated relative
// to the raw Grafana-classic set so the dark UI feels refined
// rather than vibrant — same hue coverage (cool / green / warm /
// purple) Tempo uses, just polished. Red and pink stay out (red is
// the error overlay; pink is a standing user no-go).
// v0.5.249 — modernised palette aligned to the Tailwind v3
// -500 shade family (the de-facto "Datadog 2023+ / Grafana 11
// / Honeycomb" hue stack). Higher saturation than the prior
// Tempo-classic muted set; calibrated lightness so each colour
// reads cleanly on dark UI backgrounds without WCAG contrast
// failures. Red (#EF4444) is reserved for the error overlay;
// pink is operator-no-go.
const COLORS = [
  // Cool — indigo / blue / sky / cyan / teal
  '#6366F1',  // indigo-500
  '#3B82F6',  // blue-500
  '#0EA5E9',  // sky-500
  '#06B6D4',  // cyan-500
  '#14B8A6',  // teal-500

  // Greens — emerald / green / lime
  '#10B981',  // emerald-500
  '#22C55E',  // green-500
  '#84CC16',  // lime-500

  // Warm — amber / orange
  '#F59E0B',  // amber-500
  '#F97316',  // orange-500

  // Purple — violet / purple
  '#8B5CF6',  // violet-500
  '#A855F7',  // purple-500
];
export function hashColor(s: string): string {
  let h = 5381;
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h) ^ s.charCodeAt(i);
  return COLORS[Math.abs(h) % COLORS.length];
}

const SEV = ['', 'TRACE', 'TRACE2', 'TRACE3', 'TRACE4', 'DEBUG', 'DEBUG2', 'DEBUG3', 'DEBUG4',
             'INFO', 'INFO2', 'INFO3', 'INFO4', 'WARN', 'WARN2', 'WARN3', 'WARN4',
             'ERROR', 'ERROR2', 'ERROR3', 'ERROR4', 'FATAL', 'FATAL2', 'FATAL3', 'FATAL4'];
export function sevName(n: number): string { return SEV[n] || String(n); }
export function sevClass(n: number): string {
  if (n >= 21) return 's-fatal';
  if (n >= 17) return 's-error';
  if (n >= 13) return 's-warn';
  if (n >= 9)  return 's-info';
  if (n >= 5)  return 's-debug';
  return 's-trace';
}

// substituteVars expands ${name} references against a variables map.
// Lines whose ALL ${name} references resolve to empty strings are
// dropped — that's how an "all services" filter degrades cleanly:
// ${service} = '' makes `service.name = "${service}"` collapse to
// nothing instead of becoming a literal empty-string predicate.
//
// Used by the dashboard renderer to inject Grafana-style variables
// into spanmetric DSLs and other line-based config fields.
export function substituteVars(s: string, vars: Record<string, string>): string {
  if (!s) return s;
  // Multi-line case: drop a line if every variable on it is empty
  // (not "if any are empty" — partially-resolved lines may still be
  // useful, e.g. when the DSL combines a fixed predicate with a
  // variable one). For our presets the rule "all empty → drop" is
  // the right one.
  const lines = s.split('\n');
  const out: string[] = [];
  for (const line of lines) {
    const refs = [...line.matchAll(/\$\{([^}]+)\}/g)].map(m => m[1]);
    if (refs.length > 0 && refs.every(name => !vars[name])) continue;
    out.push(line.replace(/\$\{([^}]+)\}/g, (_, name) => vars[name] ?? ''));
  }
  return out.join('\n');
}

// rowClickHandlers: spread onto a row element to make it behave like a
// real anchor for `href` — left-click navigates SPA-style via `navigate`,
// middle-click or Cmd/Ctrl/Shift+click opens in a new tab. Plain
// router.push doesn't honor middle-click because the table row isn't
// an <a>; this restores the browser convention without restructuring the
// table layout.
//
//   <tr {...rowClickHandlers(`/trace?id=${t.traceId}`,
//                             () => router.push(`/trace?id=${t.traceId}`))}>
export function rowClickHandlers(href: string, navigate: () => void) {
  return {
    onClick: (e: ReactMouseEvent) => {
      // Left-click with a modifier → new tab. Match what an <a href> does.
      if (e.metaKey || e.ctrlKey || e.shiftKey) {
        window.open(href, '_blank', 'noopener,noreferrer');
        return;
      }
      navigate();
    },
    // Middle button. preventDefault on mousedown stops Chrome's
    // auto-scroll widget; auxclick fires after and is what we react to.
    onAuxClick: (e: ReactMouseEvent) => {
      if (e.button === 1) {
        e.preventDefault();
        window.open(href, '_blank', 'noopener,noreferrer');
      }
    },
    onMouseDown: (e: ReactMouseEvent) => {
      if (e.button === 1) e.preventDefault();
    },
  };
}

// displaySpanName builds the string shown in the waterfall + tooltips.
// Most OTel SDKs name spans well (e.g. "GET /api/orders", "SELECT users")
// but gRPC instrumentations often emit useless generic names — "grpc",
// "grpc command", or just the rpc.method on its own — which is unhelpful
// when 5 services are calling each other. When the raw name looks
// generic, build a richer label from the rpc.* / peer.service attributes.
//
// Rule of thumb:
//   - Names containing rpc.service + rpc.method already → leave alone.
//   - Otherwise, if rpc.service is set: "<rpc.service>/<rpc.method>".
//   - If only peer.service is set:        "<service.name> → <peer.service>".
//   - Else fall back to the raw name.
export function displaySpanName(s: {
  name: string;
  serviceName?: string;
  attributes?: Record<string, string>;
}): string {
  const a = s.attributes ?? {};
  const raw = (s.name ?? '').trim();
  const lc  = raw.toLowerCase();

  const rpcService = a['rpc.service'];
  const rpcMethod  = a['rpc.method'];
  const peer       = a['peer.service'];

  // OTel HTTP semconv: server.address (new) or http.host (old) carries
  // the target hostname; url.path / http.target / http.route carries
  // the path. Both new + old conventions are honoured because in-the-
  // wild SDKs are mid-migration.
  const serverAddr = a['server.address'] || a['http.host'] || a['net.peer.name'];
  const urlPath    = a['url.path']       || a['http.target'] || a['http.route'];

  // Bare HTTP verb? Some SDKs (especially the older Node + Python
  // instrumentations) name client spans literally "GET" / "POST" /
  // etc. Useless on its own — enrich with server.address + path
  // when available.
  const httpVerbs = new Set(['get', 'post', 'put', 'delete', 'patch', 'head', 'options']);
  if (httpVerbs.has(lc)) {
    if (serverAddr && urlPath) return `${raw} ${serverAddr}${urlPath}`;
    if (serverAddr)            return `${raw} ${serverAddr}`;
    if (urlPath)               return `${raw} ${urlPath}`;
    return raw;
  }

  // Bail when the existing name is already informative — anything that
  // contains a `/`, `.`, or whitespace and isn't the bare keywords below
  // is treated as descriptive (matches HTTP, DB, message-broker patterns).
  const generic = lc === 'grpc' || lc === 'grpc command' || lc === 'rpc' ||
                  lc === 'call' || (rpcMethod ? lc === rpcMethod.toLowerCase() : false);

  if (!generic) return raw;

  if (rpcService && rpcMethod) {
    if (peer && peer !== rpcService) {
      return `${peer}/${rpcService}/${rpcMethod}`;
    }
    return `${rpcService}/${rpcMethod}`;
  }
  if (rpcMethod && peer) {
    return `${peer}.${rpcMethod}`;
  }
  if (peer && s.serviceName) {
    return `${s.serviceName} → ${peer}`;
  }
  return raw;
}
