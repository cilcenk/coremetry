import { Badge, Row, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Tones — the whole enum, each with the label it actually carries in
// the product (severity column, status pill, chrome tag).
export function Tones() {
  return (
    <Frame>
      <Row gap={2} wrap style={{ alignItems: 'center' }}>
        <Badge tone="neutral">3 pods</Badge>
        <Badge tone="info">anomaly</Badge>
        <Badge tone="success">resolved</Badge>
        <Badge tone="warning">warning</Badge>
        <Badge tone="danger">critical</Badge>
        <Badge tone="accent">sampled</Badge>
      </Row>
    </Frame>
  );
}

// Problem rows — how the badge sits in an Inbox list: priority + state
// per row, next to the service name and the reason string.
export function ProblemRows() {
  type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger';
  const rows: Array<{ prio: string; prioTone: Tone; svc: string; state: string; stateTone: Tone; reason: string }> = [
    { prio: 'P1', prioTone: 'danger',  svc: 'payments-orchestrator', state: 'open',     stateTone: 'danger',  reason: 'error rate 12.4% (2.9× baseline)' },
    { prio: 'P2', prioTone: 'warning', svc: 'api-gateway',           state: 'acked',    stateTone: 'neutral', reason: 'p99 1 840 ms vs 610 ms baseline' },
    { prio: 'P3', prioTone: 'info',    svc: 'inventory-sync',        state: 'resolved', stateTone: 'success', reason: 'chronic exception drip · NullPointerException' },
  ];
  return (
    <Frame style={{ minWidth: 520 }}>
      <Stack gap={2}>
        {rows.map(r => (
          <div key={r.svc} style={{ display: 'grid', gridTemplateColumns: '36px 170px 74px 1fr', gap: 10, alignItems: 'center' }}>
            <Badge tone={r.prioTone}>{r.prio}</Badge>
            <span style={{ fontWeight: 600 }}>{r.svc}</span>
            <Badge tone={r.stateTone}>{r.state}</Badge>
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>{r.reason}</span>
          </div>
        ))}
      </Stack>
    </Frame>
  );
}

// Counts — tabular-nums badges as they appear on section headers and
// table cells (counts, rates, durations).
export function Counts() {
  return (
    <Frame>
      <Stack gap={2}>
        <Row gap={2} wrap style={{ alignItems: 'center' }}>
          <span style={{ fontWeight: 600 }}>Slow queries</span>
          <Badge>1 284</Badge>
          <span style={{ fontWeight: 600, marginLeft: 12 }}>Exceptions</span>
          <Badge tone="danger">47 new</Badge>
          <span style={{ fontWeight: 600, marginLeft: 12 }}>Pods</span>
          <Badge tone="success">12/12 ready</Badge>
        </Row>
        <Row gap={2} wrap style={{ alignItems: 'center' }}>
          <Badge tone="info">p99 812 ms</Badge>
          <Badge tone="warning">5xx 0.8%</Badge>
          <Badge tone="success">99.97% ok</Badge>
          <Badge tone="neutral">HTTP</Badge>
          <Badge tone="neutral">kafka.consumer</Badge>
          <Badge tone="neutral">prod-eu-west</Badge>
        </Row>
      </Stack>
    </Frame>
  );
}
