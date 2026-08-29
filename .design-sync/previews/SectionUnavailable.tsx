import { SectionUnavailable, DrawerSection, StatTile } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// The one-line "this section has no answer for this window" note. It lives
// INSIDE a section whose neighbours still render — the null-tolerance
// contract of the detail drawers (a missing payload never blanks the shell).

export function BesidePopulatedSection() {
  return (
    <Frame style={{ width: 528 }}>
      <DrawerSection title="Latency · last 15m">
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 'var(--sp-4)' }}>
          <StatTile label="p50">38 ms</StatTile>
          <StatTile label="p95">96 ms</StatTile>
          <StatTile label="p99" tone="warn">412 ms</StatTile>
        </div>
      </DrawerSection>
      <DrawerSection title="Exemplars">
        <SectionUnavailable what="Exemplars" />
      </DrawerSection>
      <DrawerSection title="Deploy markers">
        <SectionUnavailable what="Deploy history" />
      </DrawerSection>
    </Frame>
  );
}

export function Alone() {
  return (
    <Frame style={{ width: 360 }}>
      <SectionUnavailable what="Slow queries" />
    </Frame>
  );
}
