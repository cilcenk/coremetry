import { Card, Badge, Row, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Default density with header + footer slots — the page-level section
// panel (Service → Infrastructure).
export function HeaderAndFooter() {
  return (
    <Frame style={{ maxWidth: 520 }}>
      <Card
        header={<Row justify="between" style={{ alignItems: 'center' }}><span>Infrastructure</span><Badge tone="success">3/3 ready</Badge></Row>}
        footer="Updated 12 s ago · source: k8s entity layer (prod-eu-west)">
        <Stack gap={2}>
          {[
            ['payments-orchestrator-7d9f4-x2k9q', 'node-14', '61%', '1.2 GiB'],
            ['payments-orchestrator-7d9f4-m4tz1', 'node-07', '58%', '1.1 GiB'],
            ['payments-orchestrator-7d9f4-q8w2c', 'node-22', '73%', '1.4 GiB'],
          ].map(([pod, node, cpu, mem]) => (
            <div key={pod} style={{ display: 'grid', gridTemplateColumns: '1fr 70px 50px 70px', gap: 8, fontSize: 12 }}>
              <span style={{ fontFamily: 'ui-monospace, Menlo, monospace', fontSize: 11 }}>{pod}</span>
              <span style={{ color: 'var(--text2)' }}>{node}</span>
              <span style={{ textAlign: 'right' }}>{cpu}</span>
              <span style={{ textAlign: 'right', color: 'var(--text2)' }}>{mem}</span>
            </div>
          ))}
        </Stack>
      </Card>
    </Frame>
  );
}

// Tight density — the inline callout / anomaly card (bg2, 10px, small radius).
export function Tight() {
  return (
    <Frame style={{ maxWidth: 520 }}>
      <Row gap={3} wrap>
        <Card density="tight" style={{ flex: '1 1 220px' }}>
          <Row gap={2} style={{ alignItems: 'center', marginBottom: 6 }}>
            <Badge tone="danger">anomaly</Badge>
            <span style={{ fontWeight: 600 }}>p99 latency</span>
          </Row>
          <div style={{ fontSize: 12, color: 'var(--text2)' }}>
            1 840 ms vs 610 ms baseline (3.0×) since 14:20 · correlates with deploy <code>v2026.08.29-1</code>
          </div>
        </Card>
        <Card density="tight" style={{ flex: '1 1 220px' }}>
          <Row gap={2} style={{ alignItems: 'center', marginBottom: 6 }}>
            <Badge tone="warning">saturation</Badge>
            <span style={{ fontWeight: 600 }}>DB pool</span>
          </Row>
          <div style={{ fontSize: 12, color: 'var(--text2)' }}>
            postgres pool 48/50 in use on payments-orchestrator · 14 waiters
          </div>
        </Card>
      </Row>
    </Frame>
  );
}

// Stat grid — bare cards (no slots) laid out as the service overview tiles.
export function StatGrid() {
  const tiles: Array<[string, string, string, 'success' | 'warning' | 'danger' | 'neutral']> = [
    ['Requests',   '2 410 rps', '+4% vs 1h',   'neutral'],
    ['Error rate', '12.4%',     '2.9× baseline', 'danger'],
    ['p99',        '1 840 ms',  '3.0× baseline', 'warning'],
    ['Apdex',      '0.71',      'target 0.90',   'warning'],
  ];
  return (
    <Frame>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(120px, 1fr))', gap: 10 }}>
        {tiles.map(([label, value, delta, tone]) => (
          <Card key={label}>
            <div style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '.4px' }}>{label}</div>
            <div style={{ fontSize: 20, fontWeight: 600, margin: '4px 0', fontVariantNumeric: 'tabular-nums' }}>{value}</div>
            <Badge tone={tone}>{delta}</Badge>
          </Card>
        ))}
      </div>
    </Frame>
  );
}

// Header only + empty body — what the panel looks like before data lands.
export function EmptyState() {
  return (
    <Frame style={{ maxWidth: 520 }}>
      <Card header="Recent deployments">
        <div style={{ color: 'var(--text3)', fontSize: 12, textAlign: 'center', padding: '18px 0' }}>
          No deploy events in the last 24h — pipeline curl step is still pending (docs/DEPLOY-EVENTS.md)
        </div>
      </Card>
    </Frame>
  );
}
