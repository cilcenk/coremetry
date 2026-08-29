import { Row, Button, Badge, Chip, LinkButton } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

const label = (t: string) => <span style={{ fontSize: 10, color: 'var(--text3)', width: 70, flex: 'none' }}>{t}</span>;

// justify axis — start (default) / between / end, each on a bordered lane.
export function Justify() {
  const lane = { border: '1px dashed var(--border)', borderRadius: 4, padding: 6 } as const;
  return (
    <Frame>
      <div style={{ display: 'grid', gap: 10 }}>
        <Row gap={2}>
          {label('start')}
          <Row gap={2} style={{ ...lane, flex: 1 }}>
            <Badge tone="danger">P1</Badge>
            <span>api-gateway · 5xx rate 7.9% (2× threshold)</span>
          </Row>
        </Row>
        <Row gap={2}>
          {label('between')}
          <Row gap={2} justify="between" style={{ ...lane, flex: 1 }}>
            <span>3 problems open</span>
            <LinkButton tone="accent">Open inbox →</LinkButton>
          </Row>
        </Row>
        <Row gap={2}>
          {label('end')}
          <Row gap={2} justify="end" style={{ ...lane, flex: 1 }}>
            <Button variant="secondary" size="sm">Ignore</Button>
            <Button variant="primary" size="sm">Acknowledge</Button>
          </Row>
        </Row>
      </div>
    </Frame>
  );
}

// wrap — a chip strip that overflows the line.
export function WrapChips() {
  const services = ['api-gateway', 'payments-orchestrator', 'ledger-writer', 'fraud-scorer', 'notification-hub',
    'customer-profile', 'card-authorizer', 'settlement-batch', 'fx-rates', 'audit-trail'];
  return (
    <Frame style={{ maxWidth: 460 }}>
      <Row gap={1} wrap>
        {services.map((s, i) => (
          <Chip key={s} size="sm" active={i < 2} onRemove={i < 2 ? () => {} : undefined}>{s}</Chip>
        ))}
      </Row>
    </Frame>
  );
}

// grow — the inner Row takes the remaining width; the trailing control keeps its size.
export function Grow() {
  return (
    <Frame>
      <Row gap={2}>
        <Row gap={2} grow justify="between" style={{ border: '1px dashed var(--border)', borderRadius: 4, padding: '4px 8px' }}>
          <span className="mono">SELECT * FROM orders WHERE customer_id = $1</span>
          <Badge tone="warning">p95 1 820 ms</Badge>
        </Row>
        <Button variant="ghost" size="sm">Explain plan</Button>
      </Row>
    </Frame>
  );
}
