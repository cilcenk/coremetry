import { SearchField, Row } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Empty with placeholder + shortcut hint; the ✕ clear button only appears with a value.
export function EmptyWithHint() {
  return (
    <Frame>
      <Row gap={3} wrap>
        <SearchField value="" onChange={() => {}} placeholder="Filter by path (substring)…" aria-label="Filter endpoints" hint="/" width={280} />
        <SearchField value="" onChange={() => {}} placeholder="Search logs (KQL)…" aria-label="Search logs" hint="⌘K" width={260} />
      </Row>
    </Frame>
  );
}

// With a value — the ✕ clear button appears on the right. (No `hint` here:
// unfocused + valued, the kbd hint and the ✕ share the same right edge.)
export function WithValue() {
  return (
    <Frame>
      <Row gap={3} wrap>
        <SearchField value="/api/v2/orders" onChange={() => {}} aria-label="Filter endpoints" width={280} />
        <SearchField value="service.name:payments-orchestrator AND level:error" onChange={() => {}} aria-label="Search logs" width={360} />
      </Row>
    </Frame>
  );
}

// Width axis — number = px, string passes through (e.g. 100%); no hint, no value.
export function Widths() {
  return (
    <Frame style={{ maxWidth: 520 }}>
      <div style={{ display: 'grid', gap: 8 }}>
        <SearchField value="" onChange={() => {}} placeholder="Trace id…" aria-label="Trace id" width={160} />
        <SearchField value="" onChange={() => {}} placeholder="Filter pods…" aria-label="Filter pods" width={240} />
        <SearchField value="" onChange={() => {}} placeholder="Filter metric names…" aria-label="Filter metric names" width="100%" />
      </div>
    </Frame>
  );
}

// Disabled — while the catalogue is still loading.
export function Disabled() {
  return (
    <Frame>
      <SearchField value="" onChange={() => {}} disabled placeholder="Loading service catalogue…" aria-label="Filter services" width={280} />
    </Frame>
  );
}
