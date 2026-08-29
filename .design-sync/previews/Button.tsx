import { Button, Row, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Variants — the enum axis that most changes appearance.
export function Variants() {
  return (
    <Frame>
      <Row gap={2} wrap>
        <Button variant="primary">Save rule</Button>
        <Button variant="secondary">Cancel</Button>
        <Button variant="accent">Explain root cause</Button>
        <Button variant="ghost">Compare trace</Button>
        <Button variant="danger">Delete runbook</Button>
        <Button variant="ghost-danger">Remove cluster</Button>
      </Row>
    </Frame>
  );
}

export function Sizes() {
  return (
    <Frame>
      <Row gap={2} wrap style={{ alignItems: 'center' }}>
        <Button variant="primary" size="xs">xs · Ack</Button>
        <Button variant="primary" size="sm">sm · Acknowledge</Button>
        <Button variant="primary" size="md">md · Acknowledge</Button>
        <Button variant="primary" size="lg">lg · Acknowledge all</Button>
      </Row>
    </Frame>
  );
}

export function States() {
  return (
    <Frame>
      <Stack gap={2}>
        <Row gap={2} wrap>
          <Button variant="primary" loading>Saving…</Button>
          <Button variant="secondary" disabled>Disabled</Button>
          <Button variant="accent" leftIcon={<span aria-hidden>✨</span>}>AI explain</Button>
          <Button variant="secondary" rightIcon={<span aria-hidden>→</span>}>Open trace</Button>
        </Row>
      </Stack>
    </Frame>
  );
}
