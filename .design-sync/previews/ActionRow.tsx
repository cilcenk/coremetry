import { ActionRow, Button, LinkButton } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// secondary + confirm — the plain form footer (right-aligned).
export function SaveCancel() {
  return (
    <Frame>
      <ActionRow secondary={<Button variant="secondary">Cancel</Button>}
                 confirm={<Button variant="primary">Save rule</Button>} />
    </Frame>
  );
}

// destructive pinned far left, the confirm pair far right.
export function WithDestructive() {
  return (
    <Frame>
      <ActionRow destructive={<Button variant="ghost-danger">Delete channel</Button>}
                 secondary={<Button variant="secondary">Cancel</Button>}
                 confirm={<Button variant="primary">Save channel</Button>} />
    </Frame>
  );
}

// several secondaries via Fragment; danger confirm for a destructive dialog.
export function MultipleSecondaries() {
  return (
    <Frame>
      <ActionRow
        secondary={<>
          <LinkButton tone="muted">Preview query</LinkButton>
          <Button variant="secondary">Cancel</Button>
        </>}
        confirm={<Button variant="danger">Drop partition 2026-08-01</Button>} />
    </Frame>
  );
}

// inline — table cell / edit row: no top margin, no stretch, order kept.
export function Inline() {
  return (
    <Frame>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', gap: 8, alignItems: 'center', fontSize: 12 }}>
        <span className="mono">prod-eu-west → thanos-querier.monitoring.svc:10902</span>
        <ActionRow inline
          destructive={<Button variant="ghost-danger" size="xs">Remove</Button>}
          secondary={<Button variant="ghost" size="xs">Test</Button>}
          confirm={<Button variant="secondary" size="xs">Edit</Button>} />
        <span className="mono">prod-us-east → thanos-querier.monitoring.svc:10902</span>
        <ActionRow inline
          destructive={<Button variant="ghost-danger" size="xs">Remove</Button>}
          secondary={<Button variant="ghost" size="xs">Test</Button>}
          confirm={<Button variant="secondary" size="xs">Edit</Button>} />
      </div>
    </Frame>
  );
}

// only a confirm — a disabled primary while the form is invalid.
export function ConfirmOnlyDisabled() {
  return (
    <Frame>
      <ActionRow confirm={<Button variant="primary" disabled>Create maintenance window</Button>} />
    </Frame>
  );
}
