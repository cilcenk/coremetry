'use client';
import { useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { api } from '@/lib/api';
import { fmtNum, tsLong, timeRangeToNs } from '@/lib/utils';
import type { Exception, TimeRange } from '@/lib/types';

type GroupBy = 'type' | 'type-service' | 'full';
type SortKey = 'type' | 'message' | 'service' | 'count' | 'lastSeen';
type SortDir = 'asc' | 'desc';

// Default direction when a column is selected for the first time.
const NATURAL_DIR: Record<SortKey, SortDir> = {
  type: 'asc', message: 'asc', service: 'asc',
  count: 'desc', lastSeen: 'desc',
};

const GROUP_OPTIONS: { value: GroupBy; label: string }[] = [
  { value: 'type',         label: 'Type' },
  { value: 'type-service', label: 'Type + Service' },
  { value: 'full',         label: 'Type + Message + Service' },
];

export default function ErrorsPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [service, setService] = useState('');
  const [search, setSearch] = useState('');
  const [groupBy, setGroupBy] = useState<GroupBy>('type-service');
  const [services, setServices] = useState<string[]>([]);
  const [data, setData] = useState<Exception[] | null | undefined>(undefined);
  const [sortBy, setSortBy] = useState<SortKey>('count');
  const [sortDir, setSortDir] = useState<SortDir>('desc');

  useEffect(() => {
    api.services(timeRangeToNs(range))
      .then(s => setServices((s ?? []).map(x => x.name))).catch(() => {});
  }, [range]);

  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.exceptions({ service: service || undefined, groupBy, from, to, limit: 200 })
      .then(d => setData(d ?? [])).catch(() => setData(null));
  }, [range, service, groupBy]);

  const filtered = useMemo(() => {
    const list = (data ?? []).filter(e => {
      if (!search) return true;
      const q = search.toLowerCase();
      return e.type.toLowerCase().includes(q)
          || e.message.toLowerCase().includes(q)
          || e.service.toLowerCase().includes(q);
    });
    const cmp = (a: Exception, b: Exception): number => {
      switch (sortBy) {
        case 'type':     return a.type.localeCompare(b.type);
        case 'message':  return a.message.localeCompare(b.message);
        case 'service':  return a.service.localeCompare(b.service);
        case 'count':    return a.count - b.count;
        case 'lastSeen': return a.lastSeen - b.lastSeen;
      }
    };
    const sorted = [...list].sort(cmp);
    return sortDir === 'desc' ? sorted.reverse() : sorted;
  }, [data, search, sortBy, sortDir]);

  const toggleSort = (col: SortKey) => {
    if (sortBy === col) setSortDir(sortDir === 'desc' ? 'asc' : 'desc');
    else { setSortBy(col); setSortDir(NATURAL_DIR[col]); }
  };

  const showService = groupBy !== 'type';
  const messageHeader = groupBy === 'full' ? 'Message' : 'Sample message';

  return (
    <>
      <Topbar title="Errors" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls">
          <Combobox value={service} onChange={setService} options={services}
            placeholder="Service…" width={170} />
          <input value={search} onChange={e => setSearch(e.target.value)}
            placeholder="Search type/message…" style={{ width: 260 }} />
          <label style={{ display: 'flex', alignItems: 'center', gap: 6,
                          color: 'var(--text2)', fontSize: 12 }}>
            Group by
            <select value={groupBy} onChange={e => setGroupBy(e.target.value as GroupBy)}>
              {GROUP_OPTIONS.map(o => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {filtered.length} unique exception groups · {fmtNum(filtered.reduce((n, e) => n + e.count, 0))} occurrences
          </span>
        </div>

        {data === undefined && <Spinner />}
        {data && filtered.length === 0 && (
          <Empty icon="✓" title="No errors in this window">
            Exception events captured by the OTel SDK appear here, grouped by type / message.
          </Empty>
        )}
        {data && filtered.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <SortTh col="type"     label="Type"          sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  <SortTh col="message"  label={messageHeader} sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  {showService && (
                    <SortTh col="service" label="Service"      sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  )}
                  <SortTh col="count"    label="Count"         sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                  <SortTh col="lastSeen" label="Last seen"     sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  <th>Sample</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((e, i) => (
                  <tr key={`${e.type}|${e.message}|${e.service}|${i}`}>
                    <td><b style={{ color: 'var(--err)' }}>{e.type}</b></td>
                    <td style={{ maxWidth: 480 }} title={e.message}>{e.message || '—'}</td>
                    {showService && (
                      <td>
                        <Link href={`/service?name=${encodeURIComponent(e.service)}`}
                          style={{ fontFamily: 'monospace', fontSize: 11 }}>
                          {e.service}
                        </Link>
                      </td>
                    )}
                    <td className="mono" style={{ textAlign: 'right' }}>
                      <span className="badge b-err">{fmtNum(e.count)}</span>
                    </td>
                    <td className="mono">{tsLong(e.lastSeen)}</td>
                    <td className="mono">
                      {e.sampleTraceId
                        ? <Link href={`/trace?id=${e.sampleTraceId}`}>{e.sampleTraceId.slice(0, 8)}…</Link>
                        : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

function SortTh({ col, label, sort, dir, onSort, align }: {
  col: SortKey; label: string;
  sort: SortKey; dir: SortDir;
  onSort: (c: SortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}
      <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}
