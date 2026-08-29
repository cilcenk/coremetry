import { PageShell, Badge, Button, Card, PanelTitle, Row, StatTile } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// PageShell = the `#content` container every page body renders into.
// `default` scrolls and pads (20px from globals.css); `full` zeroes the
// padding and hands scrolling to the child (one full-bleed canvas).
// Frame padding is zeroed so the shell's own padding is what you see;
// --bg0 is the app's body ground the content area sits on.

export function Default() {
  return (
    <Frame style={{ padding: 0, background: 'var(--bg0)' }}>
      <PageShell>
        <Row gap={3} style={{ alignItems: 'center', marginBottom: 'var(--sp-6)' }}>
          <span style={{ fontSize: 'var(--fs-xl)', fontWeight: 600 }}>payments-orchestrator</span>
          <Badge tone="danger">P1</Badge>
          <Badge>prod-eu-west</Badge>
          <span style={{ flex: 1 }} />
          <Button variant="secondary" size="sm">Last 1h</Button>
          <Button variant="accent" size="sm" leftIcon={<span aria-hidden>✨</span>}>Explain</Button>
        </Row>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(0, 1fr))', gap: 'var(--sp-5)', marginBottom: 'var(--sp-6)' }}>
          <StatTile label="Requests">41.2k</StatTile>
          <StatTile label="p95" tone="warn">301 ms</StatTile>
          <StatTile label="Error rate" tone="err">3.8%</StatTile>
          <StatTile label="Pods">6 / 6</StatTile>
        </div>
        <Card header={<PanelTitle sub="server + consumer spans · 5m buckets">Top operations</PanelTitle>}>
          <table style={{ width: '100%' }}>
            <thead>
              <tr><th style={{ textAlign: 'left' }}>Operation</th><th style={{ textAlign: 'right' }}>Calls</th><th style={{ textAlign: 'right' }}>p95</th><th style={{ textAlign: 'right' }}>Errors</th></tr>
            </thead>
            <tbody>
              <tr><td>POST /v1/charges</td><td style={{ textAlign: 'right' }}>18 402</td><td style={{ textAlign: 'right' }}>5 010 ms</td><td style={{ textAlign: 'right', color: 'var(--err)' }}>7.0%</td></tr>
              <tr><td>POST /v1/refunds</td><td style={{ textAlign: 'right' }}>2 118</td><td style={{ textAlign: 'right' }}>212 ms</td><td style={{ textAlign: 'right' }}>0.1%</td></tr>
              <tr><td>GET /v1/charges/:id</td><td style={{ textAlign: 'right' }}>20 684</td><td style={{ textAlign: 'right' }}>48 ms</td><td style={{ textAlign: 'right' }}>0.0%</td></tr>
            </tbody>
          </table>
        </Card>
      </PageShell>
    </Frame>
  );
}

// variant="full": padding 0 + overflow hidden — the child owns the whole
// surface (topology canvas, heatmap, trace waterfall).
export function Full() {
  const node = (x: number, y: number, label: string, tone: string) => (
    <g>
      <rect x={x - 62} y={y - 16} width={124} height={32} rx={6} fill="var(--bg1)" stroke={tone} strokeWidth={1.5} />
      <text x={x} y={y + 4} textAnchor="middle" fontSize={11} fill="var(--text)" fontFamily="-apple-system, Segoe UI, sans-serif">{label}</text>
    </g>
  );
  return (
    <Frame style={{ padding: 0, background: 'var(--bg0)' }}>
      <PageShell variant="full">
        <div style={{ height: 300, position: 'relative', background: 'var(--bg0)' }}>
          <div style={{ position: 'absolute', top: 10, left: 12, fontSize: 'var(--fs-2xs)', color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.5 }}>
            Topology · prod-eu-west · last 1h
          </div>
          <svg width="100%" height="300" viewBox="0 0 860 300" preserveAspectRatio="xMidYMid meet" style={{ display: 'block' }}>
            <g stroke="var(--border)" strokeWidth={1.5} fill="none">
              <path d="M 192 150 L 306 150" />
              <path d="M 430 150 C 480 150 480 80 544 80" />
              <path d="M 430 150 C 480 150 480 220 544 220" />
              <path d="M 668 80 L 726 80" stroke="var(--err)" />
            </g>
            {node(130, 150, 'checkout-web', 'var(--ok)')}
            {node(368, 150, 'api-gateway', 'var(--ok)')}
            {node(606, 80, 'payments-orchestrator', 'var(--err)')}
            {node(606, 220, 'inventory-svc', 'var(--ok)')}
            {node(788, 80, 'postgres · orders', 'var(--warn)')}
            <text x={606} y={112} textAnchor="middle" fontSize={10} fill="var(--err)" fontFamily="monospace">3.8% err · p95 301 ms</text>
          </svg>
        </div>
      </PageShell>
    </Frame>
  );
}
