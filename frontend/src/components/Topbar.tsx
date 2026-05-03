'use client';
import { TimeRangePicker } from './TimeRangePicker';
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
    </div>
  );
}
