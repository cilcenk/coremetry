import { Modal, Button, ActionRow, Field, Stack } from 'coremetry-ui';

// Modal portals to document.body: the card renders it open, full-bleed.
export function Confirm() {
  return (
    <Modal open onClose={() => {}} title="Delete runbook?" size="sm"
      footer={<ActionRow secondary={<Button variant="secondary">Cancel</Button>}
                         confirm={<Button variant="danger">Delete</Button>} />}>
      <p style={{ margin: 0 }}>“Spool drain” will be removed from every service that pins it. Past executions stay in the history.</p>
    </Modal>
  );
}

export function FormDialog() {
  return (
    <Modal open onClose={() => {}} title="New maintenance window" size="md"
      footer={<ActionRow secondary={<Button variant="secondary">Cancel</Button>}
                         confirm={<Button variant="primary">Create window</Button>} />}>
      <Stack gap={3}>
        <Field label="Title" defaultValue="Core banking release 2026.09" />
        <Field label="Starts" type="datetime-local" defaultValue="2026-09-02T22:00" />
        <Field label="Duration" hint="Alerts are suppressed for the affected services" defaultValue="4h" />
      </Stack>
    </Modal>
  );
}
