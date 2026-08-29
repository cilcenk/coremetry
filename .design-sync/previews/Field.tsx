import { Field, SelectField, TextareaField, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// A settings form the operator actually fills in (Settings → Remote clusters).
export function TextInputs() {
  return (
    <Frame style={{ maxWidth: 420 }}>
      <Stack gap={3}>
        <Field label="Cluster name (join key)" hint="Must match k8s.cluster.name in telemetry" defaultValue="prod-eu-west" />
        <Field label="Thanos Querier URL" placeholder="https://thanos-querier.example/api/v1" />
        <Field label="Namespace filter" hint="PromQL regex — cardinality shield" placeholder="^(app-|payments-)" />
      </Stack>
    </Frame>
  );
}

export function ErrorState() {
  return (
    <Frame style={{ maxWidth: 420 }}>
      <Stack gap={3}>
        <Field label="Thanos label value" defaultValue="prod-eu-west"
          error='Value "prod-eu-west" is already bound to cluster "fake-ocp" — one value binds to one record' />
        <Field label="Poll interval (s)" type="number" defaultValue={5} error="Minimum is 10 seconds" />
      </Stack>
    </Frame>
  );
}

export function SelectAndTextarea() {
  return (
    <Frame style={{ maxWidth: 420 }}>
      <Stack gap={3}>
        <SelectField label="Auth" hint="ServiceAccount token bound to cluster-monitoring-view" defaultValue="bearer">
          <option value="none">None (in-mesh / mTLS)</option>
          <option value="bearer">Bearer token</option>
          <option value="basic">Basic</option>
        </SelectField>
        <TextareaField label="Runbook notes" rows={3}
          defaultValue={'1. Check the Distributed spool size\n2. If > 2 000 files, FLUSH DISTRIBUTED on the busiest table'} />
      </Stack>
    </Frame>
  );
}
