import { PanelTitle, Card, LinkButton, Chip, Row } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Bare title — uppercase, letter-spaced, --text2.
export function Plain() {
  return (
    <Frame>
      <PanelTitle>Status breakdown</PanelTitle>
    </Frame>
  );
}

// sub = the honesty qualifier (sample / scope / axis) in --text3.
export function WithSub() {
  return (
    <Frame>
      <div style={{ display: 'grid', gap: 14 }}>
        <PanelTitle sub="log-bin histogram">Latency distribution</PanelTitle>
        <PanelTitle sub="on this route's spans">Related exception groups</PanelTitle>
        <PanelTitle sub="top 10 values, whitelisted dimensions">Break down by</PanelTitle>
      </div>
    </Frame>
  );
}

// right slot — a pivot link or a toggle, pushed to the far edge.
export function WithRightSlot() {
  return (
    <Frame>
      <div style={{ display: 'grid', gap: 14 }}>
        <PanelTitle sub="worst first" right={
          <span style={{ fontSize: 11 }}>
            <a href="#" onClick={e => e.preventDefault()} style={{ color: 'var(--warn)', marginRight: 10 }}>⚡ slowest trace →</a>
            <a href="#" onClick={e => e.preventDefault()} style={{ color: 'var(--err)' }}>✖ worst error →</a>
          </span>
        }>Slowest traces</PanelTitle>
        <PanelTitle sub="service · pod, by time share" right={
          <Row gap={1}>
            <Chip size="xs" active>time share</Chip>
            <Chip size="xs">calls</Chip>
          </Row>
        }>Who calls this</PanelTitle>
        <PanelTitle right={<LinkButton tone="muted">Open in Traces →</LinkButton>}>Recent errors</PanelTitle>
      </div>
    </Frame>
  );
}

// As a Card header — the canonical call site on the detail pages.
export function InCardHeader() {
  return (
    <Frame>
      <Card header={<PanelTitle sub="on this route's spans">Related exception groups</PanelTitle>}>
        <div style={{ display: 'grid', gap: 6, fontSize: 12 }}>
          <Row gap={2} justify="between"><span className="mono">java.net.SocketTimeoutException</span><span style={{ color: 'var(--text3)' }}>142 · 3 pods</span></Row>
          <Row gap={2} justify="between"><span className="mono">org.postgresql.util.PSQLException</span><span style={{ color: 'var(--text3)' }}>37 · 1 pod</span></Row>
          <Row gap={2} justify="between"><span className="mono">io.grpc.StatusRuntimeException</span><span style={{ color: 'var(--text3)' }}>9 · 2 pods</span></Row>
        </div>
      </Card>
    </Frame>
  );
}
