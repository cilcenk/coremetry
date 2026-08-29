import { TextareaField, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// TextareaField — labelled <textarea> with hint/error; rows defaults to 4.
// Rendered exactly as the in-app callers do (Incidents summary, Watcher
// import): no extra className/style, so the card shows what the DS ships.

// /incident postmortem summary (rows=3, the heaviest caller).
export function PostmortemSummary() {
  return (
    <Frame style={{ maxWidth: 460 }}>
      <TextareaField label="Summary (optional)" rows={3}
        hint="Shown at the top of the incident page and in the weekly digest"
        defaultValue="Payments p99 crossed 2× baseline at 09:14 after the ledger-7d9f rollout; rolled back 09:41. 3 400 checkouts delayed, none lost." />
    </Frame>
  );
}

// Alerts → Watcher import: a pasted Elasticsearch watch body (rows=8).
export function WatchJson() {
  return (
    <Frame style={{ maxWidth: 460 }}>
      <TextareaField label="Watch JSON (PUT _watcher/watch body)" rows={8} spellCheck={false}
        hint="Trigger, input.search and condition are mapped; actions become notification channels"
        defaultValue={`{
  "trigger": { "schedule": { "interval": "1m" } },
  "input": { "search": { "request": {
    "indices": ["logs-payments-*"],
    "body": { "query": { "match": { "level": "ERROR" } } } } } },
  "condition": { "compare": { "ctx.payload.hits.total": { "gte": 25 } } }
}`} />
    </Frame>
  );
}

// Invalid content: the error line replaces the hint, textarea is aria-invalid.
export function ErrorState() {
  return (
    <Frame style={{ maxWidth: 460 }}>
      <Stack gap={3}>
        <TextareaField label="PromQL expression" rows={3} spellCheck={false}
          defaultValue={'sum by (route) (rate(http_server_duration_count{service="api-gateway"}[5m])'}
          error='parse error at char 78: unexpected end of input, expected ")"' />
        <TextareaField label="Runbook notes" rows={2} defaultValue=""
          error="Required when the rule severity is critical" />
      </Stack>
    </Frame>
  );
}

// Disabled after a successful import; placeholder-only empty field beside it.
export function DisabledAndEmpty() {
  return (
    <Frame style={{ maxWidth: 460 }}>
      <Stack gap={3}>
        <TextareaField label="Watch JSON (PUT _watcher/watch body)" rows={3} disabled
          hint="Imported as rule alr-3f2 — re-open the rule to edit it"
          defaultValue={'{ "trigger": { "schedule": { "interval": "1m" } }, … }'} />
        <TextareaField label="Maintenance note" rows={3}
          placeholder="Why alerts are suppressed, and who to page if something still breaks"
          hint="Visible on every suppressed problem during the window" />
      </Stack>
    </Frame>
  );
}
