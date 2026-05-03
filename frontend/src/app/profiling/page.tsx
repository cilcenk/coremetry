'use client';
import { useEffect, useState } from 'react';
import Link from 'next/link';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { api } from '@/lib/api';
import { tsShort, timeRangeToNs, fmtNum } from '@/lib/utils';
import type { ProfileRow, TimeRange } from '@/lib/types';

const TYPES = [
  { v: '', label: 'All types' },
  { v: 'cpu', label: 'CPU' },
  { v: 'heap', label: 'Heap' },
  { v: 'goroutine', label: 'Goroutine' },
  { v: 'alloc', label: 'Alloc' },
];

export default function ProfilingPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [service, setService] = useState('');
  const [ptype, setPtype] = useState('');
  const [services, setServices] = useState<string[]>([]);
  const [data, setData] = useState<ProfileRow[] | null | undefined>(undefined);

  useEffect(() => {
    api.services(timeRangeToNs(range))
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => setServices([]));
  }, [range]);

  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.profiles({ service, type: ptype, from, to, limit: 200 })
      .then(p => setData(p ?? []))
      .catch(() => setData(null));
  }, [range, service, ptype]);

  return (
    <>
      <Topbar title="Profiling" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls">
          <Combobox value={service} onChange={setService} options={services}
            placeholder="Service…" width={170} />
          <select value={ptype} onChange={e => setPtype(e.target.value)}>
            {TYPES.map(t => <option key={t.v} value={t.v}>{t.label}</option>)}
          </select>
          <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 'auto' }}>
            Continuous CPU + heap profiles, captured in 5s windows.
          </span>
        </div>

        {data === undefined && <Spinner />}
        {data && data.length === 0 && (
          <Empty icon="🔥" title="No profiles yet">
            The demo pushes profiles every 10s to <code>POST /v1/profiles</code>.
          </Empty>
        )}
        {data && data.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Service</th>
                  <th>Type</th>
                  <th>Window</th>
                  <th>Samples</th>
                  <th>Host</th>
                </tr>
              </thead>
              <tbody>
                {data.map(p => (
                  <tr key={p.profileId} onClick={() => window.location.href = `/profile?id=${p.profileId}`}>
                    <td className="mono">{tsShort(p.startTime)}</td>
                    <td>
                      <span style={{ fontSize: 11, padding: '1px 6px', background: 'var(--bg3)', borderRadius: 3, fontFamily: 'monospace' }}>
                        {p.serviceName}
                      </span>
                    </td>
                    <td><span className="badge b-info">{p.profileType.toUpperCase()}</span></td>
                    <td className="mono">{p.durationMs > 0 ? `${(p.durationMs/1000).toFixed(1)}s` : '—'}</td>
                    <td>{fmtNum(p.sampleCount)}</td>
                    <td className="mono" style={{ color: 'var(--text2)' }}>{p.hostName || '—'}</td>
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
