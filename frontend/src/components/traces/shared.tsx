// shared.tsx — Phase 1 Task B (TRACES) common atoms.
//
// One per-service colour map for every trace surface (table badge, duration
// bar, scatter dot, mini-waterfall, full waterfall, RED chart). It reuses the
// SAME hash the topology graph + metrics charts use (chartFmt.seriesColor) so a
// service keeps ONE colour across the whole product — the operator's eye never
// recalibrates. Everything here is CSS-var-only (light + dark safe).

import type { ReactNode } from 'react';
import { seriesColor } from '@/lib/chartFmt';

// svcColor — the shared per-service hue. Empty / unknown collapses to a stable
// 'unknown' bucket so blank service rows still get a deterministic colour.
export const svcColor = (name: string): string => seriesColor(name || 'unknown');

// svcBadgeBg — a faint, theme-safe tint of the service hue for badge fills.
export const svcBadgeBg = (name: string): string =>
  `color-mix(in srgb, ${svcColor(name)} 16%, transparent)`;

// durColor — duration → token colour. Errors are always red; otherwise a
// green/amber/red ramp by absolute latency. Mirrors the APM convention
// (Datadog/Honeycomb): sub-400ms green, sub-1s amber, ≥1s red.
export function durColor(ms: number, err: boolean): string {
  if (err) return 'var(--err)';
  if (ms > 1000) return 'var(--err)';
  if (ms > 400) return 'var(--warn)';
  return 'var(--ok)';
}

// fmtDur — compact duration label (ms under 1s, s above). Two decimals so
// sub-millisecond spans don't read as "0".
export function fmtDur(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
  return `${ms.toFixed(2)}ms`;
}

// SvcBadge — service-coloured monospace pill. Same hue as the waterfall +
// topology, so service identity is colour-stable everywhere.
export function SvcBadge({ name }: { name: string }) {
  return (
    <span
      title={name || 'unknown'}
      style={{
        fontSize: 11,
        padding: '1px 7px',
        borderRadius: 4,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        background: svcBadgeBg(name),
        color: svcColor(name),
        whiteSpace: 'nowrap',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        maxWidth: '100%',
        display: 'inline-block',
        verticalAlign: 'bottom',
      }}>
      {name || 'unknown'}
    </span>
  );
}

// DurationBar — value label + a track-bar scaled to the slowest visible row,
// coloured by latency (red if the trace/span errored). Reuses the .ov-minibar
// token track from globals.css.
export function DurationBar({ ms, err, max }: { ms: number; err: boolean; max: number }) {
  const pct = max > 0 ? Math.max(2, Math.min(100, (ms / max) * 100)) : 0;
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
      <span className="mono" style={{ minWidth: 58, color: err ? 'var(--err)' : 'var(--text)' }}>
        {fmtDur(ms)}
      </span>
      <span className="ov-minibar" style={{ maxWidth: 110, flex: 1 }}>
        <i style={{ width: `${pct}%`, background: durColor(ms, err) }} />
      </span>
    </div>
  );
}

// QuickChip — a clickable pill for the quick-filter row. `dot` paints a leading
// service-colour swatch; `tone="err"` reads the label red (the error count).
// Deliberately NOT the shared <Button> atom or .segmented (U1 batch 2
// judgement): this is the facet-chip anatomy (.facet/.on in globals.css).
// It stays hand-rolled because it's a real <button> for keyboard semantics,
// and the global `button:hover` rule out-specifies `.facet:hover` — the
// existing .facet chips are all <span role="button">s for that reason.
export function QuickChip({
  active, onClick, children, dot, tone,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
  dot?: string;
  tone?: 'err';
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        padding: '3px 10px',
        borderRadius: 20,
        fontSize: 11.5,
        cursor: 'pointer',
        border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
        background: active ? 'color-mix(in srgb, var(--accent) 14%, transparent)' : 'var(--bg2)',
        color: active ? 'var(--accent2)' : tone === 'err' ? 'var(--err)' : 'var(--text2)',
        fontWeight: 600,
        whiteSpace: 'nowrap',
      }}>
      {dot && <span style={{ width: 7, height: 7, borderRadius: 7, background: dot, flex: 'none' }} />}
      {children}
    </button>
  );
}

// SpanKindChip — a compact, normalised span-kind label (server/client/
// producer/consumer/internal). Tokenised so it reads in both themes.
export function SpanKindChip({ kind }: { kind: string }) {
  const k = normKindLabel(kind);
  if (!k) return null;
  return (
    <span
      style={{
        fontSize: 9.5,
        fontWeight: 700,
        letterSpacing: '0.4px',
        textTransform: 'uppercase',
        padding: '1px 5px',
        borderRadius: 3,
        background: 'var(--bg3)',
        color: 'var(--text3)',
        border: '1px solid var(--border)',
        whiteSpace: 'nowrap',
      }}>
      {k}
    </span>
  );
}

function normKindLabel(kind: string | undefined): string {
  const k = (kind ?? '').toLowerCase().replace(/^span_kind_/, '');
  switch (k) {
    case 'server': case '2': return 'SRV';
    case 'client': case '3': return 'CLI';
    case 'producer': case '4': return 'PROD';
    case 'consumer': case '5': return 'CONS';
    case 'internal': case '1': return 'INT';
    default: return '';
  }
}
