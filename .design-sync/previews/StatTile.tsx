import { StatTile, Row } from 'coremetry-ui';
import { TrendDelta } from '@/components/TrendDelta';
import { Frame } from '../preview-lib/frame';

// The detail-page tile grid (EndpointDetail / databases / statement detail):
// auto-fit columns, window totals for one route.
const grid = { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(104px, 1fr))', gap: 8 } as const;

export function WindowTotals() {
  return (
    <Frame>
      <div style={grid}>
        <StatTile label="Calls">1.24M</StatTile>
        <StatTile label="Errors">3 812</StatTile>
        <StatTile label="Err rate">0.31%</StatTile>
        <StatTile label="Req/min">861.4</StatTile>
        <StatTile label="Avg">42.7 ms</StatTile>
        <StatTile label="P50">18 ms</StatTile>
        <StatTile label="P95">180 ms</StatTile>
        <StatTile label="P99">412 ms</StatTile>
      </div>
    </Frame>
  );
}

// tone recolours the VALUE only — the frame stays neutral (err ≥ 5%, warn ≥ 1%).
export function Tones() {
  return (
    <Frame>
      <Row gap={2} wrap style={{ alignItems: 'stretch' }}>
        <StatTile label="Err rate · api-gateway">0.31%</StatTile>
        <StatTile label="Err rate · payments-orchestrator" tone="warn">2.40%</StatTile>
        <StatTile label="Err rate · ledger-writer" tone="err">7.92%</StatTile>
        <StatTile label="P99 · ledger-writer" tone="err">2 310 ms</StatTile>
      </Row>
    </Frame>
  );
}

// children (not value: string) is the contract — value + compare-window delta.
export function WithTrendDelta() {
  return (
    <Frame>
      <div style={grid}>
        <StatTile label="Calls">
          1.24M
          <TrendDelta cur={1240000} prior={1010000} kind="neutral" />
        </StatTile>
        <StatTile label="Errors" tone="warn">
          3 812
          <TrendDelta cur={3812} prior={2650} kind="lowerBetter" />
        </StatTile>
        <StatTile label="Avg">
          42.7 ms
          <TrendDelta cur={42.7} prior={43.1} kind="lowerBetter" />
        </StatTile>
        <StatTile label="P99">
          412 ms
          <TrendDelta cur={412} prior={530} kind="lowerBetter" />
        </StatTile>
        <StatTile label="Total time">
          14.7 h
          <TrendDelta cur={14.7} prior={0} kind="neutral" />
        </StatTile>
      </div>
    </Frame>
  );
}
