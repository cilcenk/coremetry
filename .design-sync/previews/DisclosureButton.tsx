import { DisclosureButton, Card, Row, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Row anatomy — the inline chevron toggle (legend headings, "Latency
// distribution", the Sidebar nav group). Collapsed / expanded / disabled.
export function RowAnatomy() {
  return (
    <Frame>
      <Stack gap={2}>
        <Row gap={4} wrap style={{ alignItems: 'center' }}>
          <DisclosureButton expanded={false}>Latency distribution</DisclosureButton>
          <DisclosureButton expanded>12 downstream dependencies</DisclosureButton>
          <DisclosureButton expanded={false} disabled>Exemplars (none in range)</DisclosureButton>
        </Row>
        <div style={{ fontSize: 12, color: 'var(--text2)', paddingLeft: 18 }}>
          <div>api-gateway → payments-orchestrator · 2 410 rps · p99 812 ms</div>
          <div>api-gateway → inventory-sync · 640 rps · p99 210 ms</div>
          <div>api-gateway → redis (cache) · 9 800 rps · p99 3 ms</div>
        </div>
      </Stack>
    </Frame>
  );
}

// Section anatomy — the full-width card heading: fixed-width glyph
// column keeps labels aligned; the divider appears only while open
// (bound to aria-expanded, not a second class).
export function SectionAnatomy() {
  return (
    <Frame style={{ maxWidth: 480 }}>
      <Stack gap={3}>
        <Card style={{ padding: 0 }}>
          <DisclosureButton anatomy="section" expanded>Pods · 3 running</DisclosureButton>
          <div style={{ padding: '10px 14px', fontSize: 12 }}>
            {[
              ['payments-orchestrator-7d9f4-x2k9q', 'Running', '61% cpu'],
              ['payments-orchestrator-7d9f4-m4tz1', 'Running', '58% cpu'],
              ['payments-orchestrator-7d9f4-q8w2c', 'CrashLoopBackOff', '—'],
            ].map(([pod, phase, cpu]) => (
              <div key={pod} style={{ display: 'grid', gridTemplateColumns: '1fr 120px 60px', gap: 8, padding: '4px 0' }}>
                <span style={{ fontFamily: 'ui-monospace, Menlo, monospace', fontSize: 11 }}>{pod}</span>
                <span style={{ color: phase === 'Running' ? 'var(--ok)' : 'var(--err)' }}>{phase}</span>
                <span style={{ color: 'var(--text2)', textAlign: 'right' }}>{cpu}</span>
              </div>
            ))}
          </div>
        </Card>
        <Card style={{ padding: 0 }}>
          <DisclosureButton anatomy="section" expanded={false}>Database calls · postgres, redis</DisclosureButton>
        </Card>
        <Card style={{ padding: 0 }}>
          <DisclosureButton anatomy="section" expanded={false} className="dsc-caps">Latency distribution</DisclosureButton>
        </Card>
      </Stack>
    </Frame>
  );
}
