import { DrawerTrendRow, DrawerSection, Stack } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// DrawerTrendRow = 60px label + 420×34 Sparkline. One row per signal.

const REQ = [420, 398, 371, 352, 340, 336, 355, 402, 468, 551, 640, 712, 758, 781, 790, 774, 752, 735, 710, 668, 612, 560, 505, 462];
const P95 = [182, 176, 171, 169, 168, 170, 174, 183, 197, 214, 238, 262, 289, 301, 296, 288, 279, 271, 260, 249, 232, 215, 201, 190];
const ERR = [0.3, 0.2, 0.2, 0.3, 0.2, 0.2, 0.3, 0.4, 0.4, 0.5, 0.7, 1.1, 2.4, 3.8, 3.1, 1.9, 1.2, 0.8, 0.6, 0.5, 0.4, 0.3, 0.3, 0.3];
const SAT = [41, 42, 42, 43, 44, 44, 46, 49, 53, 58, 63, 69, 74, 78, 81, 80, 78, 76, 72, 68, 62, 57, 52, 47];

// The RED trio a service drawer shows for the last hour.
export function RateLatencyErrors() {
  return (
    <Frame>
      <DrawerSection title="Trends · last 1h · payments-orchestrator">
        <Stack gap={2}>
          <DrawerTrendRow label="Requests" values={REQ} color="var(--accent2)" />
          <DrawerTrendRow label="p95 ms" values={P95} color="var(--warn)" />
          <DrawerTrendRow label="Errors %" values={ERR} color="var(--err)" />
        </Stack>
      </DrawerSection>
    </Frame>
  );
}

// onClick set → the row is an affordance (cursor:pointer + "click to expand"
// title); the caller renders the expanded uPlot chart itself.
export function Expandable() {
  return (
    <Frame>
      <DrawerSection title="Host · ip-10-42-7-118 · click a row to expand">
        <Stack gap={2}>
          <DrawerTrendRow label="CPU %" values={SAT} color="var(--accent2)" onClick={() => {}} />
          <DrawerTrendRow label="Mem %" values={SAT.map(v => Math.round(v * 0.8 + 12))} color="var(--ok)" onClick={() => {}} />
        </Stack>
      </DrawerSection>
    </Frame>
  );
}

// "Measured, all zero" (flat baseline) vs "not measured" (— placeholder):
// the two empty-ish states are deliberately distinct.
export function ZeroAndNoData() {
  return (
    <Frame>
      <DrawerSection title="Trends · last 1h · inventory-svc (fresh deploy)">
        <Stack gap={2}>
          <DrawerTrendRow label="Requests" values={REQ.slice(18)} color="var(--accent2)" />
          <DrawerTrendRow label="Errors %" values={Array.from({ length: 24 }, () => 0)} color="var(--err)" />
          <DrawerTrendRow label="p95 ms" values={[]} color="var(--warn)" />
        </Stack>
      </DrawerSection>
    </Frame>
  );
}
