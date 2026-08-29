import { MenuItem, Row } from 'coremetry-ui';
import type { CSSProperties, ReactNode } from 'react';
import { Frame } from '../preview-lib/frame';

// The dropdown surface MenuItem rows live in (dashboard panel ⋯ menu).
// The popover box itself is caller-owned in the product; this is the
// house shape: bg1 + border + radius + shadow, 4px inset, 200px wide.
const panel: CSSProperties = {
  display: 'flex', flexDirection: 'column', gap: 2,
  width: 210, padding: 4,
  background: 'var(--bg1)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', boxShadow: 'var(--shadow-md, 0 4px 16px rgba(0,0,0,.35))',
};
function Panel({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return <div role="menu" style={{ ...panel, ...style }}>{children}</div>;
}
const divider = <div style={{ height: 1, background: 'var(--border)', margin: '3px 6px' }} />;

// Panel menu — the dashboard panel's ⋯ menu: icon slot on every row so
// labels align, and the one destructive row separated at the bottom.
export function PanelMenu() {
  return (
    <Frame>
      <Panel>
        <MenuItem icon={<span aria-hidden>✎</span>}>Edit panel</MenuItem>
        <MenuItem icon={<span aria-hidden>⧉</span>}>Duplicate</MenuItem>
        <MenuItem icon={<span aria-hidden>⤢</span>}>View fullscreen</MenuItem>
        <MenuItem icon={<span aria-hidden>↗</span>}>Open in Explore</MenuItem>
        {divider}
        <MenuItem icon={<span aria-hidden>🗑</span>} danger>Delete panel</MenuItem>
      </Panel>
    </Frame>
  );
}

// Mixed rows — some rows with a glyph, some without: the fixed-width icon
// slot means the labels still line up (a row menu on a Problem).
export function MixedIcons() {
  return (
    <Frame>
      <Panel style={{ width: 240 }}>
        <MenuItem icon={<span aria-hidden>✓</span>}>Acknowledge</MenuItem>
        <MenuItem icon={<span aria-hidden>✨</span>}>Explain root cause</MenuItem>
        <MenuItem>Open trace 4f1c…9a2e</MenuItem>
        <MenuItem>Compare with baseline</MenuItem>
        <MenuItem>Copy link</MenuItem>
        {divider}
        <MenuItem danger>Ignore this problem</MenuItem>
      </Panel>
    </Frame>
  );
}

// States — disabled rows (viewer role, no write permission) next to a
// live row and a disabled destructive row, side by side.
export function States() {
  return (
    <Frame>
      <Row gap={4} wrap>
        <Panel>
          <MenuItem icon={<span aria-hidden>✓</span>}>Acknowledge</MenuItem>
          <MenuItem icon={<span aria-hidden>⏸</span>} disabled>Snooze 4h (editor only)</MenuItem>
          <MenuItem icon={<span aria-hidden>⇄</span>} disabled>Reassign team (editor only)</MenuItem>
          {divider}
          <MenuItem danger disabled>Delete (admin only)</MenuItem>
        </Panel>
        <Panel>
          <MenuItem icon={<span aria-hidden>↻</span>}>Restart pod</MenuItem>
          <MenuItem icon={<span aria-hidden>≡</span>}>Show logs</MenuItem>
          <MenuItem icon={<span aria-hidden>⤓</span>}>Export spans (CSV)</MenuItem>
          {divider}
          <MenuItem icon={<span aria-hidden>✕</span>} danger>Evict pod</MenuItem>
        </Panel>
      </Row>
    </Frame>
  );
}
