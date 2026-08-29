import { SelectField, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// SelectField — labelled <select> with hint/error, the Settings-page
// variant of Field. Options are caller-owned children.

// Settings → Storage / Auth: small fixed sets (≤10 values need no picker).
export function Choices() {
  return (
    <Frame style={{ maxWidth: 440 }}>
      <Stack gap={3}>
        <SelectField label="Logs backend" hint="COREMETRY_LOGS_BACKEND — read path only; ClickHouse stays the warm store" defaultValue="elasticsearch">
          <option value="clickhouse">ClickHouse (built-in)</option>
          <option value="elasticsearch">Elasticsearch (external, read-only)</option>
        </SelectField>
        <SelectField label="Auth" defaultValue="bearer">
          <option value="none">None (in-mesh / mTLS)</option>
          <option value="bearer">Bearer token</option>
          <option value="basic">Basic</option>
        </SelectField>
        <SelectField label="Retention preset" hint="Applies to spans + metric_points; 5m MVs keep 13 months" defaultValue="30d">
          <option value="7d">7 days</option>
          <option value="14d">14 days</option>
          <option value="30d">30 days</option>
          <option value="90d">90 days</option>
        </SelectField>
      </Stack>
    </Frame>
  );
}

// Grouped options: <optgroup> children pass straight through.
export function Grouped() {
  return (
    <Frame style={{ maxWidth: 440 }}>
      <Stack gap={3}>
        <SelectField label="Notification channel" hint="Where P1 problems for this service are routed" defaultValue="pd-payments">
          <optgroup label="PagerDuty">
            <option value="pd-payments">payments-sre (24/7)</option>
            <option value="pd-core">core-platform</option>
          </optgroup>
          <optgroup label="Slack">
            <option value="sl-alerts">#alerts-prod</option>
            <option value="sl-checkout">#checkout-oncall</option>
          </optgroup>
          <optgroup label="Email">
            <option value="mail-ops">ops@…</option>
          </optgroup>
        </SelectField>
        <SelectField label="Owner team" defaultValue="core-platform">
          <option value="payments-sre">payments-sre</option>
          <option value="core-platform">core-platform</option>
          <option value="checkout">checkout</option>
          <option value="identity">identity</option>
        </SelectField>
      </Stack>
    </Frame>
  );
}

// Validation failures: the error line replaces the hint and the select is
// aria-invalid.
export function ErrorState() {
  return (
    <Frame style={{ maxWidth: 440 }}>
      <Stack gap={3}>
        <SelectField label="Owner team" defaultValue="growth"
          error='Team "growth" no longer exists in the directory — pick a current owner'>
          <option value="growth">growth (removed)</option>
          <option value="payments-sre">payments-sre</option>
          <option value="core-platform">core-platform</option>
        </SelectField>
        <SelectField label="Poll interval" defaultValue="5" error="Minimum is 10 s — polling budget (CLAUDE.md)">
          <option value="5">5 s</option>
          <option value="10">10 s</option>
          <option value="30">30 s</option>
          <option value="60">60 s</option>
        </SelectField>
      </Stack>
    </Frame>
  );
}

// Disabled: value shown but locked (boot-time env wins over the UI).
export function Disabled() {
  return (
    <Frame style={{ maxWidth: 440 }}>
      <Stack gap={3}>
        <SelectField label="Deployment mode" hint="Locked by COREMETRY_MODE at boot — change the Helm value, not this field" defaultValue="all" disabled>
          <option value="all">all (single binary)</option>
          <option value="ingest">ingest</option>
          <option value="api">api</option>
          <option value="worker">worker</option>
        </SelectField>
        <SelectField label="Trace fallback" hint="Tempo is not configured — enable it under Settings → Tempo first" defaultValue="off" disabled>
          <option value="off">Off</option>
          <option value="tempo">Tempo (read-through)</option>
        </SelectField>
      </Stack>
    </Frame>
  );
}
