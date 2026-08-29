import { useEffect } from 'react';
import { ConfirmProvider, useConfirm, Button } from 'coremetry-ui';
import type { ConfirmOptions } from 'coremetry-ui';

// useConfirm is a hook, not a component: each cell mounts the provider
// around a tiny action site that calls confirm() on mount, so the dialog
// (a size="sm" Modal portaled to document.body) renders open, full-bleed.
// No Frame — overlay precedent (Modal.tsx).

function ActionSite({ opts, label, variant }: { opts: ConfirmOptions; label: string; variant: 'ghost-danger' | 'secondary' }) {
  const confirm = useConfirm();
  useEffect(() => { void confirm(opts); }, [confirm]); // eslint-disable-line react-hooks/exhaustive-deps
  return <Button variant={variant} size="sm" onClick={() => void confirm(opts)}>{label}</Button>;
}

// danger → confirm button is `danger`; focus lands on Cancel, never on the
// destructive path.
export function Destructive() {
  return (
    <ConfirmProvider>
      <ActionSite label="Delete runbook" variant="ghost-danger" opts={{
        title: 'Delete runbook?',
        body: <>“<b>Spool drain</b>” will be removed from the 12 services that pin it. Past executions stay in the history.</>,
        confirmLabel: 'Delete runbook',
        danger: true,
      }} />
    </ConfirmProvider>
  );
}

// Non-destructive → confirm button is `primary`; custom cancel label.
export function Neutral() {
  return (
    <ConfirmProvider>
      <ActionSite label="Acknowledge all" variant="secondary" opts={{
        title: 'Acknowledge 14 problems?',
        body: <>All open P2/P3 problems on <b>prod-eu-west</b> will be marked acknowledged by you. They keep firing notifications if they escalate to P1.</>,
        confirmLabel: 'Acknowledge all',
        cancelLabel: 'Keep open',
      }} />
    </ConfirmProvider>
  );
}
