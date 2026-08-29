import { Chip, Row, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Filter chips — the Traces / Inbox filter strip: pill chips, one of
// them active (aria-pressed) because the list is currently filtered by it.
export function FilterChips() {
  return (
    <Frame>
      <Row gap={2} wrap style={{ alignItems: 'center' }}>
        <Chip pill>All services</Chip>
        <Chip pill active>api-gateway</Chip>
        <Chip pill>payments-orchestrator</Chip>
        <Chip pill>inventory-sync</Chip>
        <Chip pill disabled>checkout-legacy (no data)</Chip>
      </Row>
    </Frame>
  );
}

// Tones and sizes — neutral vs accent at rest, xs vs sm, pill vs
// squared (the two radius rungs: 999 and --radius-sm).
export function TonesAndSizes() {
  return (
    <Frame>
      <Stack gap={3}>
        <Row gap={2} wrap style={{ alignItems: 'center' }}>
          <Chip size="sm">sm · env=prod</Chip>
          <Chip size="sm" pill>sm pill · status ≥ 500</Chip>
          <Chip size="xs">xs · k8s.namespace=payments</Chip>
          <Chip size="xs" pill>xs pill · GET /v1/orders</Chip>
        </Row>
        <Row gap={2} wrap style={{ alignItems: 'center' }}>
          <Chip tone="accent" pill>Why is p99 up since 14:20?</Chip>
          <Chip tone="accent" pill>Explain this trace</Chip>
          <Chip tone="accent" size="xs">team: core-payments</Chip>
          <Chip tone="neutral" active size="xs">error.type = TimeoutException</Chip>
        </Row>
      </Stack>
    </Frame>
  );
}

// Removable — the wrapper form (onRemove): label + built-in × as separate
// buttons. First two labels are clickable (onClick → "edit this filter"),
// the rest are static labels with only the × live.
export function Removable() {
  return (
    <Frame>
      <Row gap={2} wrap style={{ alignItems: 'center' }}>
        <Chip pill onClick={() => {}} onRemove={() => {}} removeLabel="Remove service filter">service = api-gateway</Chip>
        <Chip pill active onClick={() => {}} onRemove={() => {}} removeLabel="Remove status filter">http.status_code ≥ 500</Chip>
        <Chip pill onRemove={() => {}} removeLabel="Remove cluster filter">cluster = prod-eu-west</Chip>
        <Chip pill onRemove={() => {}} removeLabel="Remove duration filter">duration &gt; 2 s</Chip>
        <Chip pill tone="accent" onRemove={() => {}} removeLabel="Remove tag">watcher: spool-drain</Chip>
      </Row>
    </Frame>
  );
}
