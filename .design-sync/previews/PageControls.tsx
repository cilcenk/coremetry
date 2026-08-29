import { PageControls, SearchField, Button, Chip, Row } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// The Endpoints list bar: picker + lead search + filters + action.
// (≤640px the bar collapses behind a "Filtreler" DisclosureButton; the
// card viewport is 900px wide so this shows the desktop layout.)
export function ListFilterBar() {
  return (
    <Frame>
      <PageControls style={{ marginBottom: 0 }}>
        <input placeholder="Filter by service…" aria-label="Service" defaultValue="payments-orchestrator" style={{ width: 200 }} />
        <SearchField value="" onChange={() => {}} data-pc-lead
          placeholder="Filter by path (substring)…" aria-label="Filter endpoints by path"
          hint="/" width={280} />
        <select aria-label="Cluster" defaultValue="prod-eu-west">
          <option value="">All clusters</option>
          <option value="prod-eu-west">prod-eu-west</option>
          <option value="prod-us-east">prod-us-east</option>
        </select>
        <select aria-label="Kind" defaultValue="server">
          <option value="server">Server</option>
          <option value="consumer">Consumer</option>
        </select>
        <Button variant="primary" size="sm">Search</Button>
      </PageControls>
    </Frame>
  );
}

// sticky — list pages: padded, border-bottom separates it from the table below.
export function Sticky() {
  return (
    <Frame style={{ paddingTop: 0 }}>
      <PageControls sticky style={{ marginBottom: 0 }}>
        <input placeholder="Filter services…" aria-label="Service" style={{ width: 220 }} />
        <Button variant="primary" size="sm">Search</Button>
        <input placeholder="Min spans" aria-label="Minimum spans" type="number" defaultValue={1000} style={{ width: 100 }} />
        <input placeholder="Min P99 (ms)" aria-label="Minimum P99" type="number" style={{ width: 110 }} />
        <select aria-label="Owner team" defaultValue="">
          <option value="">Any team</option>
          <option value="payments">payments</option>
          <option value="core-banking">core-banking</option>
        </select>
        <Row gap={1} style={{ marginLeft: 'auto' }}>
          <Chip size="sm" active>1h</Chip>
          <Chip size="sm">6h</Chip>
          <Chip size="sm">24h</Chip>
        </Row>
      </PageControls>
      <div style={{ fontSize: 12, color: 'var(--text3)', paddingTop: 10 }}>412 services · sorted by P99 desc</div>
    </Frame>
  );
}

// Wrapping — too many controls for one line wrap onto the next (flex-wrap).
export function Wrapping() {
  return (
    <Frame style={{ maxWidth: 520 }}>
      <PageControls style={{ marginBottom: 0 }}>
        <SearchField value="orders" onChange={() => {}} placeholder="Search logs (KQL)…" aria-label="Search logs" hint="⌘K" width={260} />
        <select aria-label="Severity" defaultValue="error">
          <option value="">Any severity</option>
          <option value="error">Error</option>
          <option value="warn">Warn</option>
        </select>
        <select aria-label="Cluster" defaultValue=""><option value="">All clusters</option></select>
        <input placeholder="trace_id" aria-label="Trace id" style={{ width: 180 }} />
        <Button variant="secondary" size="sm">Patterns</Button>
        <Button variant="primary" size="sm">Search</Button>
      </PageControls>
    </Frame>
  );
}
