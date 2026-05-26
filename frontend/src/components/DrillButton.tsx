import { Link } from 'react-router-dom';
import type { TimeRange } from '@/lib/types';
import { encodeRange } from '@/lib/urlState';

// DrillButton (v0.5.463) — uniform "view in X" cross-page navigation
// chip. The ad-hoc <Link to={`/traces?service=...`}> + hand-rolled
// styling pattern duplicated in 8+ places now flows through this
// component. Two concrete wins:
//
//   1. Intent continuity — params (service, range, filters) get
//      URL-encoded consistently; the operator's "I'm looking at X"
//      context survives the navigation hop.
//
//   2. Visual consistency — the same chip shape on every drill
//      target. Operators recognise "this is a navigation chip" by
//      sight instead of having to re-read every page's variant.
//
// `range` is special-cased because every nav-aware page in
// Coremetry uses the urlState.encodeRange encoder; pass the raw
// TimeRange and we do the right thing.
//
// Generic `params` covers everything else (service, traceId,
// problemId, filter strings, etc.) — undefined/empty entries are
// dropped so an operator-less call site doesn't pollute the URL.

interface DrillButtonProps {
  to: string;
  label: React.ReactNode;
  // Shorthand: pass the current TimeRange and we encode it under
  // ?range=. Pass nothing to skip.
  range?: TimeRange;
  // Free-form additional params (service, filters, ids).
  params?: Record<string, string | number | boolean | undefined | null>;
  title?: string;
  // Variant — primary uses accent2 colour; subtle uses --text. Most
  // drill chips are primary (call-to-action style); back-style
  // navigation uses subtle.
  variant?: 'primary' | 'subtle';
}

function buildSearch(params: Record<string, string | number | boolean | undefined | null> | undefined): string {
  if (!params) return '';
  const parts: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
  }
  return parts.join('&');
}

export function DrillButton({ to, label, range, params, title, variant = 'primary' }: DrillButtonProps) {
  const merged: Record<string, string | number | boolean | undefined | null> = { ...(params ?? {}) };
  if (range) merged.range = encodeRange(range);
  const search = buildSearch(merged);
  const href = search ? `${to}?${search}` : to;
  return (
    <Link to={href} title={title}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 4,
        fontSize: 12, padding: '5px 12px',
        background: 'var(--bg3)', border: '1px solid var(--border)',
        borderRadius: 6,
        color: variant === 'subtle' ? 'var(--text)' : 'var(--accent2)',
        textDecoration: 'none', whiteSpace: 'nowrap',
      }}>
      {label}
    </Link>
  );
}
