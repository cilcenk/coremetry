import { useEffect, useState } from 'react';
import { ExploreViz, type ExploreVizKind } from './ExploreViz';
import { Spinner } from './Spinner';
import { ServicePicker } from './ServicePicker';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import type { ExploreSeries, TimeRange } from '@/lib/types';

// LogsExplorer is the Logs source on /explore. Filter +
// dimension + viz, results from /api/logs/timeseries which is
// backend-agnostic (CH or external ES). Compare-period overlay
// stacks a faded "previous window" series next to each current
// series so the operator can see "is this surge bigger than the
// previous 15 min?".
export function LogsExplorer({ range, viz, compare }: {
  range: TimeRange;
  viz: ExploreVizKind;
  compare: boolean;
}) {
  const [service, setService] = useState('');
  // Two-state search: `search` is what's typed (instant feedback in
  // the input box), `committedSearch` is what actually flies to the
  // backend. We re-key on the committed value only so a fast typist
  // doesn't trigger one CH histogram-over-1B-rows per keystroke.
  const [search, setSearch]   = useState('');
  const [committedSearch, setCommittedSearch] = useState('');
  const [groupBy, setGroupBy] = useState<'service' | 'severity' | ''>('severity');
  const [bucketSec, setBucketSec] = useState<number>(0); // 0 = auto
  const [series, setSeries]   = useState<ExploreSeries[] | null | undefined>(undefined);

  // 400ms debounce — short enough that the chart feels live as the
  // user pauses, long enough to absorb a typing burst.
  useEffect(() => {
    const t = setTimeout(() => setCommittedSearch(search), 400);
    return () => clearTimeout(t);
  }, [search]);

  useEffect(() => {
    setSeries(undefined);
    const { from, to } = timeRangeToNs(range);
    const windowMs = (to - from) / 1e6;
    // v0.5.259 — sub-15s auto-bucket on short windows so a
    // 1-minute view doesn't smear into 15s slabs. Target ~60
    // buckets; floor at 1s so per-second log spikes still
    // surface. Capped at 1800 for very wide windows.
    const windowSec = windowMs / 1000;
    const auto = Math.max(1, Math.min(1800, Math.round(windowSec / 60)));
    const bs = bucketSec > 0 ? bucketSec : auto;

    const fetchOne = (fromNs: number, toNs: number) => api.logsTimeseries({
      service: service || undefined,
      search: committedSearch || undefined,
      groupBy: groupBy || undefined,
      from: fromNs,
      to: toNs,
      bucketSec: bs,
    });

    Promise.all([
      fetchOne(from, to),
      compare ? fetchOne(from - (to - from), from) : Promise.resolve([]),
    ]).then(([cur, prev]) => {
      const out: ExploreSeries[] = (cur ?? []).map(s => ({
        name: s.name === '_total' ? 'count' : s.name,
        points: s.points.map(p => ({ t: p.t, v: p.v })),
      }));
      if (compare && prev) {
        // Shift prev's timestamps forward by the window size so
        // they overlay the current chart's x-axis.
        const shift = to - from;
        prev.forEach(s => out.push({
          name: `${s.name === '_total' ? 'count' : s.name} (prev)`,
          points: s.points.map(p => ({ t: p.t + shift, v: p.v })),
        }));
      }
      setSeries(out);
    }).catch(() => setSeries(null));
  }, [range, service, committedSearch, groupBy, bucketSec, compare]);

  return (
    <>
      <div className="controls">
        <ServicePicker value={service} onChange={setService}
          placeholder="Service…" width={200} />
        <input placeholder="Body contains…  (Enter to apply)"
          value={search}
          onChange={e => setSearch(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') setCommittedSearch(search); }}
          style={{ width: 240 }} />
        <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>
          Group by:
        </span>
        <select value={groupBy} onChange={e => setGroupBy(e.target.value as never)}>
          <option value="">— total —</option>
          <option value="service">service</option>
          <option value="severity">severity</option>
        </select>
        <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>
          Bucket:
        </span>
        <select value={bucketSec} onChange={e => setBucketSec(Number(e.target.value))}>
          <option value={0}>auto</option>
          {/* v0.5.259 — sub-15s buckets for short-window log
              traffic deep-dives (per-second spike vs. 1m smear). */}
          <option value={1}>1s</option>
          <option value={5}>5s</option>
          <option value={15}>15s</option>
          <option value={60}>1m</option>
          <option value={300}>5m</option>
          <option value={1800}>30m</option>
        </select>
      </div>

      {series === undefined && <Spinner />}
      {series === null && (
        <div style={{ color: 'var(--err)', fontSize: 12, padding: '12px 4px' }}>
          Failed to load — is the configured logs backend reachable?
        </div>
      )}
      {series && <ExploreViz series={series} kind={viz} />}
    </>
  );
}
