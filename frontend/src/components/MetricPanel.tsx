import { useEffect, useRef, useState } from 'react';
import type { ReactNode, KeyboardEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { CopyButton } from '@/components/CopyButton';
import { Button } from '@/components/ui';
import {
  describeMetricQuery,
  encodeMetricQuery,
  metricExploreHref,
  type MetricQuery,
} from '@/lib/metricQuery';

// MetricPanel — "every metric is a doorway." The ONE reusable affordance every
// metric panel/KPI opts into so there are zero per-page click handlers: a panel
// hands its MetricQuery descriptor, the wrapper turns it into the deep-link
// menu (Explore / Edit / View query / Copy link / Add to dashboard / Create
// alert). The same object that draws the chart is the object the explorer
// re-opens (see lib/metricQuery.ts). Grafana-grade unobtrusive: a hover cursor
// + the ⋮ overflow button only appearing on hover; the chart itself is NOT
// restyled — children render verbatim.
//
// Interactions:
//   • header title click / body click  → Explore (metricExploreHref)
//   • ⋮ menu                            → all six actions, all driven by `mq`
//   • keyboard `e` while hovered/focused → Explore

export interface MetricPanelProps {
  title: string;
  metricQuery: MetricQuery;
  children: ReactNode;
  // className/style pass through to the outer wrapper so a caller can size it
  // inside a grid without MetricPanel imposing layout.
  className?: string;
  style?: React.CSSProperties;
  // compact (v0.8.19, Phase C) — the affordance for panels that ALREADY own a
  // title (a KPI tile's .ov-lab, a RED chart's .ov-card-h). Instead of the
  // stacked title-header row, the ⋮ floats top-right over the panel
  // (hover-revealed) and the body stays byte-identical — no extra label, no
  // pushed-down layout. Body-click / `e` / the ⋮ menu still open Explore from
  // the same descriptor. Default (false) keeps the full header used by
  // Metrics.tsx.
  compact?: boolean;
  // menuOnly (v0.8.69, D5) — for INTERACTIVE charts (drag-zoom, bucket-click)
  // where a whole-body click would collide with the chart's own click handler.
  // Suppresses body-click → Explore; the doorway is the hover ⋮ menu + the `e`
  // shortcut only. Non-interactive panels (KPI tiles, previews) keep body-click.
  menuOnly?: boolean;
}

type MenuAction =
  | 'explore' | 'edit' | 'view' | 'copy' | 'dashboard' | 'alert';

export function MetricPanel({ title, metricQuery: mq, children, className, style, compact = false, menuOnly = false }: MetricPanelProps) {
  const navigate = useNavigate();
  const [menuOpen, setMenuOpen] = useState(false);
  const [viewOpen, setViewOpen] = useState(false);
  const [linkCopied, setLinkCopied] = useState(false);
  const [hovered, setHovered] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  const href = metricExploreHref(mq);

  // Close the overflow menu on outside click — mirrors the Sidebar user-menu.
  useEffect(() => {
    if (!menuOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [menuOpen]);

  // Esc closes the "View query" popover.
  useEffect(() => {
    if (!viewOpen) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') setViewOpen(false);
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [viewOpen]);

  const explore = () => navigate(href);

  // Body click → Explore, BUT never hijack a real interactive child (a panel
  // toolbar may carry selects / buttons / inputs). If the click landed on or
  // inside an interactive element, let it do its own thing. This keeps the
  // affordance reusable on ANY panel without the panel having to stopPropagation.
  const onBodyClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const interactive = (e.target as HTMLElement).closest(
      'button, a, input, select, textarea, label, [role="button"], [contenteditable="true"]',
    );
    if (interactive) return;
    explore();
  };

  const runAction = (a: MenuAction) => {
    setMenuOpen(false);
    switch (a) {
      case 'explore':
        navigate(href);
        break;
      case 'edit':
        // Explore IS the builder; &edit=1 is a hint the operator landed here to
        // tweak the query (Explore decodes ?m= into its existing builder state).
        navigate(href + '&edit=1');
        break;
      case 'view':
        setViewOpen(true);
        break;
      case 'copy':
        // Absolute link so it survives a paste into Slack / a runbook.
        void navigator.clipboard?.writeText(window.location.origin + href);
        setLinkCopied(true);
        setTimeout(() => setLinkCopied(false), 1500);
        break;
      case 'dashboard':
        // Best-effort: hand the descriptor to /dashboards; full consume (turn
        // ?m= into a seeded panel) is a later phase.
        navigate('/dashboards?m=' + encodeMetricQuery(mq));
        break;
      case 'alert':
        // Best-effort: hand the descriptor to /alerts; full consume is later.
        navigate('/alerts?m=' + encodeMetricQuery(mq));
        break;
    }
  };

  // `e` → Explore when the panel is hovered or focus is inside it. Ignore when
  // typing into a field that happens to be inside the body, and when a modifier
  // is held (so it doesn't steal Cmd/Ctrl shortcuts).
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'e' || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target as HTMLElement;
    const tag = t.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t.isContentEditable) return;
    e.preventDefault();
    explore();
  };

  // The ⋮ overflow button + its dropdown. Shared by both layouts (header vs
  // compact overlay) so the menu reads identically; only the positioning of the
  // enclosing wrapper differs.
  const overflow = (
    <div ref={menuRef} style={{ position: 'relative' }}>
      <button
        type="button"
        aria-label="Panel menu"
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        onClick={() => setMenuOpen(o => !o)}
        className="sec"
        style={{
          // Reveal on hover / focus-within / when open — Grafana-style.
          opacity: hovered || menuOpen ? 1 : 0,
          transition: 'opacity .12s ease',
          width: 26, height: 24, padding: 0, lineHeight: '20px',
          fontSize: 15, borderRadius: 'var(--radius-sm)',
        }}
        title="Panel actions"
      >
        ⋮
      </button>

      {menuOpen && (
        <div
          role="menu"
          onClick={e => e.stopPropagation()}
          style={{
            position: 'absolute', top: '100%', right: 0, marginTop: 4,
            minWidth: 188, background: 'var(--bg2)',
            border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
            boxShadow: 'var(--shadow-pop)', padding: 4, zIndex: 60,
          }}
        >
          <PanelMenuItem onClick={() => runAction('explore')}>⤢ Explore</PanelMenuItem>
          <PanelMenuItem onClick={() => runAction('edit')}>✎ Edit</PanelMenuItem>
          <PanelMenuItem onClick={() => runAction('view')}>⟨⟩ View query</PanelMenuItem>
          <PanelMenuItem onClick={() => runAction('copy')}>⧉ Copy link</PanelMenuItem>
          <div style={{ height: 1, background: 'var(--border)', margin: '4px 2px' }} />
          <PanelMenuItem onClick={() => runAction('dashboard')}>▦ Add to dashboard</PanelMenuItem>
          <PanelMenuItem onClick={() => runAction('alert')}>◔ Create alert</PanelMenuItem>
        </div>
      )}
    </div>
  );

  return (
    <div
      ref={rootRef}
      className={className}
      style={{ position: 'relative', ...style }}
      tabIndex={0}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onFocus={() => setHovered(true)}
      onBlur={(e) => {
        // Only un-hover when focus actually left the panel subtree.
        if (!rootRef.current?.contains(e.relatedTarget as Node)) setHovered(false);
      }}
      onKeyDown={onKeyDown}
    >
      {/* Header row — title (click → Explore) + ⋮ overflow (hover-revealed).
          Suppressed in compact mode: the wrapped panel already owns its title,
          so the doorway is just the floating ⋮ below + the body click. */}
      {!compact && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
          <button
            type="button"
            onClick={explore}
            title={`Explore — ${describeMetricQuery(mq)} (press e)`}
            style={{
              background: 'transparent', border: 'none', padding: 0,
              font: 'inherit', color: 'var(--text)', fontWeight: 600,
              fontSize: 13, cursor: 'pointer', textAlign: 'left',
              display: 'inline-flex', alignItems: 'center', gap: 6,
            }}
            className="metric-panel-title"
          >
            {title}
          </button>
          <span style={{ flex: 1 }} />
          {linkCopied && (
            <span style={{ fontSize: 11, color: 'var(--ok)' }}>Copied</span>
          )}
          {overflow}
        </div>
      )}

      {/* Compact overlay ⋮ — floats top-right OVER the panel so the wrapped
          tile/chart keeps its own layout (no title row pushes it down). */}
      {compact && (
        <div style={{ position: 'absolute', top: 6, right: 6, zIndex: 4, display: 'flex', alignItems: 'center', gap: 6 }}>
          {linkCopied && (
            <span style={{ fontSize: 11, color: 'var(--ok)' }}>Copied</span>
          )}
          {overflow}
        </div>
      )}

      {/* Body — the chart/stat, rendered verbatim. Clicking it also Explores;
          children stop propagation themselves if they have own click targets. */}
      <div
        onClick={menuOnly ? undefined : onBodyClick}
        style={{ cursor: menuOnly ? 'default' : 'pointer' }}
        title={menuOnly ? undefined : 'Open in Explore (press e)'}
      >
        {children}
      </div>

      {/* View-query popover — read-only PromQL-style projection + Copy. */}
      {viewOpen && (
        <div
          role="dialog"
          aria-label="Metric query"
          onClick={e => e.stopPropagation()}
          style={{
            position: 'absolute', top: 30, right: 0, zIndex: 70,
            minWidth: 280, maxWidth: 460,
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 'var(--radius-sm)', boxShadow: 'var(--shadow-pop)',
            padding: 12,
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
            <span style={{
              fontSize: 10.5, fontWeight: 700, letterSpacing: '.5px',
              color: 'var(--text3)', textTransform: 'uppercase',
            }}>
              Query
            </span>
            <span style={{ flex: 1 }} />
            <CopyButton value={describeMetricQuery(mq)} title="Copy query" />
            <Button
              variant="secondary"
              size="sm"
              aria-label="Close"
              onClick={() => setViewOpen(false)}
            >
              ✕
            </Button>
          </div>
          <pre
            className="mono"
            style={{
              margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              fontSize: 12, color: 'var(--text)', background: 'var(--bg1)',
              border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
              padding: '8px 10px',
            }}
          >
            {describeMetricQuery(mq)}
          </pre>
        </div>
      )}
    </div>
  );
}

// PanelMenuItem — same shape as the Sidebar user-menu MenuItem (tokens +
// hover background) so the overflow menu reads identically to the rest of the
// app's dropdowns.
function PanelMenuItem({ children, onClick }: { children: ReactNode; onClick: () => void }) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      style={{
        display: 'block', width: '100%', textAlign: 'left',
        padding: '7px 10px', border: 'none', background: 'transparent',
        color: 'var(--text2)', fontSize: 13, cursor: 'pointer',
        borderRadius: 4, whiteSpace: 'nowrap',
      }}
      onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3, var(--bg))')}
      onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
    >
      {children}
    </button>
  );
}
