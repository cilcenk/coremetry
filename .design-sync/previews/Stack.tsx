import { Stack, Row, Badge, Field, SelectField, Button, ActionRow } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// The gap scale (1=4px, 2=8px, 3=12px, 4=16px, 6=24px) — one Stack per rung.
export function GapScale() {
  const rung = (gap: 1 | 2 | 3 | 4 | 6) => (
    <div key={gap}>
      <div style={{ fontSize: 10, color: 'var(--text3)', marginBottom: 6 }}>gap={gap}</div>
      <Stack gap={gap}>
        <Badge tone="danger">P1 · api-gateway</Badge>
        <Badge tone="warning">P2 · payments-orchestrator</Badge>
        <Badge tone="neutral">P3 · ledger-writer</Badge>
      </Stack>
    </div>
  );
  return (
    <Frame>
      <Row gap={6} wrap style={{ alignItems: 'flex-start' }}>
        {([1, 2, 3, 4, 6] as const).map(rung)}
      </Row>
    </Frame>
  );
}

// A settings form the operator fills in (Settings → Alert channel).
export function FormStack() {
  return (
    <Frame style={{ maxWidth: 420 }}>
      <Stack gap={3}>
        <Field label="Channel name" defaultValue="#sre-payments" />
        <SelectField label="Kind" defaultValue="slack">
          <option value="slack">Slack webhook</option>
          <option value="teams">Microsoft Teams</option>
          <option value="email">E-mail</option>
        </SelectField>
        <Field label="Webhook URL" placeholder="https://hooks.slack.com/services/…" hint="Stored — leave empty to keep the current value" />
        <ActionRow secondary={<Button variant="secondary">Cancel</Button>}
                   confirm={<Button variant="primary">Save channel</Button>} />
      </Stack>
    </Frame>
  );
}

// Nested: a Stack of key/value Rows — the drawer summary pattern.
export function KeyValueList() {
  const kv = (k: string, v: string) => (
    <Row key={k} gap={4} justify="between" style={{ borderBottom: '1px solid var(--border)', padding: '4px 0' }}>
      <span style={{ color: 'var(--text3)', fontSize: 12 }}>{k}</span>
      <span className="mono" style={{ fontSize: 12 }}>{v}</span>
    </Row>
  );
  return (
    <Frame style={{ maxWidth: 360 }}>
      <Stack gap={1}>
        {kv('service.name', 'payments-orchestrator')}
        {kv('k8s.cluster.name', 'prod-eu-west')}
        {kv('k8s.pod.name', 'payments-orchestrator-7d9f8-x2k4q')}
        {kv('deployment.environment', 'production')}
        {kv('spans (window)', '1 240 318')}
      </Stack>
    </Frame>
  );
}
