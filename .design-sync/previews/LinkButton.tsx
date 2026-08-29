import { LinkButton, Row, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Tones — accent ("there is an action here") vs muted (dense tables,
// must not compete with the row's own text). Plus disabled.
export function Tones() {
  return (
    <Frame>
      <Row gap={4} wrap style={{ alignItems: 'center' }}>
        <LinkButton>Clear filters</LinkButton>
        <LinkButton tone="muted">Show 14 more</LinkButton>
        <LinkButton disabled>Compare with baseline</LinkButton>
        <LinkButton tone="muted" disabled>Load older</LinkButton>
      </Row>
    </Frame>
  );
}

// Underlines — hover (default), dotted (AdminAudit's "filter by this
// value" shortcut), none.
export function Underlines() {
  return (
    <Frame>
      <Row gap={4} wrap style={{ alignItems: 'center' }}>
        <LinkButton underline="hover">Open in Traces</LinkButton>
        <LinkButton underline="dotted">user.role = admin</LinkButton>
        <LinkButton underline="none">Ask Copilot</LinkButton>
      </Row>
    </Frame>
  );
}

// In context — inside prose and a table row, at the surrounding text size
// (font: inherit). The rows show muted + dotted where they actually live.
export function InContext() {
  return (
    <Frame style={{ maxWidth: 560 }}>
      <Stack gap={3}>
        <p style={{ margin: 0, color: 'var(--text2)' }}>
          200 of 1 284 traces for <b style={{ color: 'var(--text)' }}>payments-orchestrator</b>, last 1h.{' '}
          <span style={{ whiteSpace: 'nowrap' }}><LinkButton>Load more</LinkButton> · <LinkButton tone="muted">Reset range</LinkButton></span>
        </p>
        <div style={{ borderTop: '1px solid var(--border)' }}>
          {[
            ['12:04:31', 'settings.update', 'copilot_model', 'cenk'],
            ['12:02:08', 'cluster.map',     'prod-eu-west',  'cenk'],
            ['11:58:52', 'problem.ack',     'P1 · api-gateway', 'ops-bot'],
          ].map(([ts, kind, res, actor]) => (
            <div key={ts} style={{ display: 'grid', gridTemplateColumns: '70px 130px 1fr 80px', gap: 12, padding: '6px 0', borderBottom: '1px solid var(--border)', fontSize: 12 }}>
              <span style={{ color: 'var(--text3)', fontVariantNumeric: 'tabular-nums' }}>{ts}</span>
              <LinkButton underline="dotted" tone="muted">{kind}</LinkButton>
              <span>{res}</span>
              <LinkButton underline="dotted" tone="muted">{actor}</LinkButton>
            </div>
          ))}
        </div>
      </Stack>
    </Frame>
  );
}
