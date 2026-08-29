import { useEffect, useRef, useState } from 'react';
import { FacetMultiSelect, Row } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// FacetMultiSelect is the counted multi-select on the Inbox filter bar
// (Öncelik / Tür / Owner). The button SUMMARISES the selection; the panel
// opens on click, so open-state cells click the trigger on mount.

type Opt = { value: string; label: string; count: number };

const KIND: Opt[] = [
  { value: 'problem',   label: 'Problem',    count: 42 },
  { value: 'exception', label: 'Exception',  count: 17 },
  { value: 'anomaly',   label: 'Anomali',    count: 9 },
  { value: 'slo',       label: 'SLO ihlali', count: 3 },
  { value: 'deploy',    label: 'Deploy',     count: 6 },
];
const PRIO: Opt[] = [
  { value: 'P1', label: 'P1', count: 4 },
  { value: 'P2', label: 'P2', count: 11 },
  { value: 'P3', label: 'P3', count: 27 },
];
const OWNER: Opt[] = [
  { value: 'payments-sre',  label: 'payments-sre',  count: 19 },
  { value: 'core-platform', label: 'core-platform', count: 23 },
  { value: 'checkout',      label: 'checkout',      count: 8 },
  { value: 'identity',      label: 'identity',      count: 5 },
];

// Stateful host: owns the Set the way Inbox does, so toggling in the card
// actually works. `open` clicks the trigger after mount; `focusRow` then
// keyboard-focuses one panel row (reveals the "sadece" isolate button via
// :focus-within and the :focus-visible ring).
function Facet({ label, options, initial, open, focusRow }: {
  label: string; options: Opt[]; initial: string[]; open?: boolean; focusRow?: number;
}) {
  const [sel, setSel] = useState(() => new Set(initial));
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    ref.current?.querySelector<HTMLButtonElement>('.fsel-btn')?.click();
    if (focusRow == null) return;
    const t = setTimeout(() => {
      ref.current?.querySelectorAll<HTMLElement>('.fsel-row')[focusRow]?.focus();
    }, 60);
    return () => clearTimeout(t);
  }, [open, focusRow]);
  return (
    <div ref={ref} style={{ display: 'inline-block' }}>
      <FacetMultiSelect label={label} options={options} selected={sel}
        onToggle={v => setSel(s => { const n = new Set(s); if (n.has(v)) n.delete(v); else n.add(v); return n; })}
        onSolo={v => setSel(new Set([v]))}
        onAll={() => setSel(new Set(options.map(o => o.value)))} />
    </div>
  );
}

// Closed triggers — the three summary forms facetSummary() produces:
// all on → "tümü"; ≤2 on → labels joined; ≥3 on → "N seçili +M kapalı".
export function Summaries() {
  return (
    <Frame>
      <Row gap={2} wrap>
        <Facet label="Öncelik" options={PRIO} initial={['P1', 'P2', 'P3']} />
        <Facet label="Tür" options={KIND} initial={['problem', 'exception']} />
        <Facet label="Owner" options={OWNER} initial={['payments-sre', 'core-platform', 'checkout']} />
      </Row>
    </Frame>
  );
}

// Panel open, 3 of 5 kinds on: checkbox rows with counts, "tümünü seç"
// footer (some are off), and the Exception row keyboard-focused so its
// "sadece" isolate affordance is visible.
export function OpenPanel() {
  return (
    <Frame style={{ minHeight: 260 }}>
      <Facet label="Tür" options={KIND} initial={['problem', 'exception', 'anomaly']} open focusRow={1} />
    </Frame>
  );
}

// Everything selected: summary reads "tümü" and the footer disappears —
// there is nothing left to select.
export function OpenAllSelected() {
  return (
    <Frame style={{ minHeight: 200 }}>
      <Facet label="Öncelik" options={PRIO} initial={['P1', 'P2', 'P3']} open />
    </Frame>
  );
}

// Only one kind left on: that row is locked (no "sadece", not toggleable —
// the bar never reaches an empty selection). Focus lands on it to show the
// lock: ring, but no isolate button.
export function SingleLeft() {
  return (
    <Frame style={{ minHeight: 260 }}>
      <Facet label="Tür" options={KIND} initial={['problem']} open focusRow={0} />
    </Frame>
  );
}
