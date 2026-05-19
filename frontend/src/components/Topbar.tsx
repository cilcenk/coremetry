import { TimeRangePicker } from './TimeRangePicker';
import { LangToggle } from './LangToggle';
import { DensityToggle } from './DensityToggle';
import { ThemeToggle } from './ThemeToggle';
import { LiveTicker } from './LiveTicker';
import type { TimeRange } from '@/lib/types';

// `range` is optional — pages that aren't time-bound (e.g. /users) omit it
// and the time picker is hidden.
export function Topbar({ title, range, onRangeChange }: {
  title: string;
  range?: TimeRange;
  onRangeChange?: (r: TimeRange) => void;
}) {
  return (
    <div id="topbar">
      <h1>{title}</h1>
      {range && onRangeChange && (
        <TimeRangePicker value={range} onChange={onRangeChange} />
      )}
      {range && <div className="topbar-prefs-sep" />}
      {/* v0.5.280 — live ingest ticker. Visceral feedback that
          spans/logs/metrics are actually flowing; mounts once
          via Topbar so every page carries it. Hidden until the
          second sample lands so we don't show a misleading
          "0 sp/s" on first paint. */}
      <LiveTicker />
      <div className="topbar-prefs">
        <LangToggle />
        <DensityToggle />
        <ThemeToggle />
      </div>
    </div>
  );
}
