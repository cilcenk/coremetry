import { DrawerSection, Badge, Button, Row, StatTile } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// DrawerSection = uppercase micro-title + body; the drawer's content unit.
// Rendered inside the dark Frame at the drawer's own width (560 − padding).

export function SummaryTiles() {
  return (
    <Frame style={{ width: 528 }}>
      <DrawerSection title="Last 1h · api-gateway">
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(0, 1fr))', gap: 'var(--sp-4)' }}>
          <StatTile label="Requests">128.4k</StatTile>
          <StatTile label="p95">96 ms</StatTile>
          <StatTile label="Error rate" tone="warn">0.9%</StatTile>
          <StatTile label="Apdex">0.97</StatTile>
        </div>
      </DrawerSection>
      <DrawerSection title="Dependencies">
        <Row gap={2} wrap>
          <Badge tone="success">payments-orchestrator</Badge>
          <Badge tone="success">inventory-svc</Badge>
          <Badge tone="warning">redis · cart</Badge>
          <Badge tone="danger">postgres · orders</Badge>
        </Row>
      </DrawerSection>
    </Frame>
  );
}

export function AttributeTable() {
  return (
    <Frame style={{ width: 528 }}>
      <DrawerSection title="Resource attributes">
        <table className="kv-table">
          <tbody>
            <tr><td>service.name</td><td>api-gateway</td></tr>
            <tr><td>service.version</td><td>2026.08.28-1</td></tr>
            <tr><td>k8s.cluster.name</td><td>prod-eu-west</td></tr>
            <tr><td>k8s.pod.name</td><td>api-gateway-6c8b9d7f4-q2n7x</td></tr>
            <tr><td>host.name</td><td>ip-10-42-7-118.eu-west-1.compute.internal</td></tr>
          </tbody>
        </table>
      </DrawerSection>
    </Frame>
  );
}

export function Actions() {
  return (
    <Frame style={{ width: 528 }}>
      <DrawerSection title="Problem · P1 · open 42 min">
        <div style={{ fontSize: 'var(--fs-sm)', color: 'var(--text2)', marginBottom: 'var(--sp-5)', lineHeight: 1.5 }}>
          Error rate 3.8% is 12× the 7-day baseline on payments-orchestrator; 1 284 TimeoutExceptions since 14:02.
        </div>
        <Row gap={2} wrap>
          <Button variant="primary" size="sm">Acknowledge</Button>
          <Button variant="secondary" size="sm">Open traces</Button>
          <Button variant="accent" size="sm" leftIcon={<span aria-hidden>✨</span>}>Explain root cause</Button>
          <Button variant="ghost-danger" size="sm">Ignore</Button>
        </Row>
      </DrawerSection>
    </Frame>
  );
}
