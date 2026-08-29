import { IconButton, Row, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Variants — each in the spot it lives: ghost (the panel ⋯ menu
// trigger in a chart title bar), secondary (bordered toolbar buttons),
// bare (no chrome, inline after a trace id), danger (the drawer ✕ /
// remove-row). Ghost and bare are chrome-less at rest by design.
export function Variants() {
  const label = (t: string) => <span style={{ color: 'var(--text3)', fontSize: 11, width: 74, flex: 'none' }}>{t}</span>;
  return (
    <Frame style={{ maxWidth: 520 }}>
      <Stack gap={3}>
        <Row gap={3} style={{ alignItems: 'center' }}>
          {label('ghost')}
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 10px', background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
            <span style={{ fontWeight: 600, fontSize: 12 }}>Requests / s · api-gateway</span>
            <IconButton aria-label="Panel menu" icon={<span aria-hidden>⋯</span>} variant="ghost" />
          </div>
        </Row>
        <Row gap={3} style={{ alignItems: 'center' }}>
          {label('secondary')}
          <Row gap={1} style={{ alignItems: 'center' }}>
            <IconButton aria-label="Refresh" icon={<span aria-hidden>⟳</span>} variant="secondary" />
            <IconButton aria-label="Zoom out" icon={<span aria-hidden>−</span>} variant="secondary" />
            <IconButton aria-label="Zoom in" icon={<span aria-hidden>+</span>} variant="secondary" />
            <IconButton aria-label="Fullscreen" icon={<span aria-hidden>⤢</span>} variant="secondary" />
          </Row>
        </Row>
        <Row gap={3} style={{ alignItems: 'center' }}>
          {label('bare')}
          <Row gap={1} style={{ alignItems: 'center', fontSize: 12 }}>
            <span style={{ color: 'var(--text2)' }}>trace</span>
            <code style={{ fontSize: 11 }}>4f1c9e2a7b0d…9a2e</code>
            <IconButton aria-label="Copy trace id" icon={<span aria-hidden>⧉</span>} variant="bare" size="xs" />
          </Row>
        </Row>
        <Row gap={3} style={{ alignItems: 'center' }}>
          {label('danger')}
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 10px', background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
            <span style={{ fontSize: 12 }}>prod-eu-west · Thanos querier</span>
            <IconButton aria-label="Remove cluster" icon={<span aria-hidden>✕</span>} variant="danger" />
          </div>
        </Row>
      </Stack>
    </Frame>
  );
}

// Sizes — square rungs xs 20 / sm 24 / md 28 px.
export function Sizes() {
  return (
    <Frame>
      <Row gap={3} wrap style={{ alignItems: 'center' }}>
        <IconButton aria-label="Open trace" icon={<span aria-hidden>↗</span>} variant="secondary" size="xs" />
        <IconButton aria-label="Open trace" icon={<span aria-hidden>↗</span>} variant="secondary" size="sm" />
        <IconButton aria-label="Open trace" icon={<span aria-hidden>↗</span>} variant="secondary" size="md" />
        <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 8 }}>xs · sm · md</span>
      </Row>
    </Frame>
  );
}

// States — active (pinned/starred toggles press aria-pressed and take the
// accent tint) and disabled, each next to its resting twin.
export function States() {
  return (
    <Frame>
      <Stack gap={3}>
        <Row gap={2} wrap style={{ alignItems: 'center' }}>
          <IconButton aria-label="Pin service" icon={<span aria-hidden>☆</span>} />
          <IconButton aria-label="Unpin service" icon={<span aria-hidden>★</span>} active />
          <IconButton aria-label="Negate filter" icon={<span aria-hidden>≠</span>} variant="secondary" />
          <IconButton aria-label="Negate filter" icon={<span aria-hidden>≠</span>} variant="secondary" active />
          <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 8 }}>rest · active</span>
        </Row>
        <Row gap={2} wrap style={{ alignItems: 'center' }}>
          <IconButton aria-label="Panel menu" icon={<span aria-hidden>⋯</span>} disabled />
          <IconButton aria-label="Refresh" icon={<span aria-hidden>⟳</span>} variant="secondary" disabled />
          <IconButton aria-label="Remove cluster" icon={<span aria-hidden>✕</span>} variant="danger" disabled />
          <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 8 }}>disabled</span>
        </Row>
      </Stack>
    </Frame>
  );
}
