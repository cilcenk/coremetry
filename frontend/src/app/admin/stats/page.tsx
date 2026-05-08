'use client';
import { useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import type { SystemStats, TimeRange } from '@/lib/types';

// Coremetry meta-observability page — what's actually inside the
// system: counts, sizes, ingest rates, a 30-day history bar chart,
// per-table storage breakdown. Pulls a single cached payload from
// /api/admin/system-stats so the page is essentially free to load.
export default function AdminStatsPage() {
  // Topbar wants a TimeRange even though this page doesn't use it.
  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [data, setData] = useState<SystemStats | null | undefined>(undefined);
  const [refreshTick, setRefreshTick] = useState(0);

  useEffect(() => {
    setData(undefined);
    api.systemStats().then(setData).catch(() => setData(null));
  }, [refreshTick]);

  const histMax = useMemo(() => {
    if (!data?.history?.length) return 0;
    return Math.max(...data.history.map(d => d.spans));
  }, [data]);

  return (
    <>
      <Topbar title="System Stats" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
          <h2 style={{ margin: 0, fontSize: 16, color: 'var(--text)' }}>
            Coremetry — what's inside
          </h2>
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            cached 60s · all numbers from ClickHouse system.parts + service_summary_5m MV
          </span>
          <span style={{ flex: 1 }} />
          <button className="sec" onClick={() => setRefreshTick(t => t + 1)}
            title="Force a fresh recompute">↻ Refresh</button>
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load system stats" />}
        {data && (
          <>
            {/* ── Volume KPIs ──────────────────────────────────────── */}
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))',
              gap: 12, marginBottom: 18,
            }}>
              <KPI label="Spans · 24h" value={fmtNum(data.snapshot.spans24h)}
                   sub={`${fmtRate(data.ingest.spansPerSec)} now`} />
              <KPI label="Spans · 7d"  value={fmtNum(data.snapshot.spans7d)} />
              <KPI label="Spans total" value={fmtNum(data.snapshot.spansAllTime)} />
              <KPI label="Errors · 24h"
                   value={fmtNum(data.snapshot.errors24h)}
                   cls={data.snapshot.errors24h > 0 ? 'warn' : 'ok'} />
              <KPI label="Logs · 24h" value={fmtNum(data.snapshot.logs24h)}
                   sub={`${fmtRate(data.ingest.logsPerSec)} now`} />
              <KPI label="Logs total" value={fmtNum(data.snapshot.logsAllTime)} />
              <KPI label="Metrics · 24h" value={fmtNum(data.snapshot.metrics24h)}
                   sub={`${fmtRate(data.ingest.metricsPerSec)} now`} />
              <KPI label="Metrics total" value={fmtNum(data.snapshot.metricsAllTime)} />
              <KPI label="Profiles · 24h" value={fmtNum(data.snapshot.profiles24h)} />
              <KPI label="Services · 24h" value={fmtNum(data.snapshot.services24h)} />
              <KPI label="Operations · 24h" value={fmtNum(data.snapshot.operations24h)} />
              <KPI label="Disk total" value={fmtBytes(data.snapshot.totalDiskBytes)} />
            </div>

            {/* ── 30-day history ──────────────────────────────────── */}
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14, marginBottom: 18,
            }}>
              <div style={{
                display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 10,
              }}>
                <span style={{ fontSize: 12, fontWeight: 600 }}>
                  Spans / day · last 30 days
                </span>
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  bars scaled to peak day · errors overlay in red
                </span>
              </div>
              {data.history.length === 0 ? (
                <div style={{ color: 'var(--text3)', fontSize: 12, fontStyle: 'italic' }}>
                  No history yet. The 5-minute aggregate MV needs at least one bucket to populate.
                </div>
              ) : (
                <div style={{
                  display: 'flex', alignItems: 'flex-end', gap: 2,
                  height: 140, paddingTop: 8,
                }}>
                  {data.history.map(d => {
                    const h = histMax > 0 ? Math.max(2, (d.spans / histMax) * 130) : 2;
                    const errH = d.spans > 0
                      ? Math.max(0, (d.errors / d.spans) * h)
                      : 0;
                    return (
                      <div key={d.day} style={{
                        flex: 1, minWidth: 6, display: 'flex',
                        flexDirection: 'column', alignItems: 'center',
                        position: 'relative',
                      }}
                        title={
                          `${d.day}\n` +
                          `${fmtNum(d.spans)} spans\n` +
                          `${fmtNum(d.errors)} errors\n` +
                          `${d.services} service${d.services === 1 ? '' : 's'}`
                        }>
                        <div style={{ width: '100%', height: h, position: 'relative',
                                      background: 'var(--accent2)', borderRadius: '2px 2px 0 0' }}>
                          {errH > 0 && (
                            <div style={{
                              position: 'absolute', bottom: 0, left: 0, right: 0,
                              height: errH, background: 'var(--err)',
                              borderRadius: '0 0 0 0',
                            }} />
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
              {/* X-axis label endpoints */}
              {data.history.length >= 2 && (
                <div style={{
                  display: 'flex', justifyContent: 'space-between',
                  fontSize: 10, color: 'var(--text3)', marginTop: 6,
                  fontFamily: 'ui-monospace, monospace',
                }}>
                  <span>{data.history[0].day}</span>
                  <span>{data.history[data.history.length - 1].day}</span>
                </div>
              )}
            </div>

            {/* ── Per-table storage ───────────────────────────────── */}
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14, marginBottom: 18,
            }}>
              <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
                ClickHouse storage · {data.tables.length} table{data.tables.length === 1 ? '' : 's'}
              </div>
              <div className="table-wrap">
                <table>
                  <thead><tr>
                    <th>Table</th>
                    <th className="num">Rows</th>
                    <th className="num">On disk</th>
                    <th className="num">Compression</th>
                    <th className="num">Parts</th>
                    <th>Oldest</th>
                    <th>Newest</th>
                  </tr></thead>
                  <tbody>
                    {data.tables.map(t => {
                      const ratio = t.uncompressedBytes > 0
                        ? t.compressedBytes / t.uncompressedBytes
                        : 0;
                      return (
                        <tr key={t.table}>
                          <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>{t.table}</td>
                          <td className="num">{fmtNum(t.rows)}</td>
                          <td className="num">{fmtBytes(t.bytesOnDisk)}</td>
                          <td className="num" style={{ color: 'var(--text3)' }}>
                            {ratio > 0
                              ? `${(ratio * 100).toFixed(1)}% (${fmtBytes(t.uncompressedBytes)} raw)`
                              : '—'}
                          </td>
                          <td className="num">{t.parts}</td>
                          <td style={{ fontSize: 11, color: 'var(--text2)' }}>
                            {t.oldestNs ? tsLong(t.oldestNs) : '—'}
                          </td>
                          <td style={{ fontSize: 11, color: 'var(--text2)' }}>
                            {t.newestNs ? tsLong(t.newestNs) : '—'}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>

            {/* ── 30-day history table ────────────────────────────── */}
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14, marginBottom: 18,
            }}>
              <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
                Daily history
              </div>
              <div className="table-wrap" style={{ maxHeight: 360, overflowY: 'auto' }}>
                <table>
                  <thead><tr>
                    <th>Day</th>
                    <th className="num">Spans</th>
                    <th className="num">Errors</th>
                    <th className="num">Err %</th>
                    <th className="num">Services</th>
                  </tr></thead>
                  <tbody>
                    {[...data.history].reverse().map(d => {
                      const errPct = d.spans > 0 ? (d.errors / d.spans) * 100 : 0;
                      return (
                        <tr key={d.day}>
                          <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>{d.day}</td>
                          <td className="num">{fmtNum(d.spans)}</td>
                          <td className="num">{fmtNum(d.errors)}</td>
                          <td className={`num ${errPct >= 5 ? 'err' : errPct > 0 ? 'warn' : ''}`}>
                            {errPct.toFixed(2)}%
                          </td>
                          <td className="num">{d.services}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>

            <div style={{ fontSize: 11, color: 'var(--text3)' }}>
              Tip: <Link href="/services" style={{ color: 'var(--accent2)' }}>/services</Link>
              {' '}lists all live services; <Link href="/alerts" style={{ color: 'var(--accent2)' }}>/alerts</Link>
              {' '}shows the rules driving Problems / Incidents.
            </div>
          </>
        )}
      </div>
    </>
  );
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: 12, border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg2)',
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
      }}>{label}</div>
      <div className={cls} style={{ fontSize: 20, fontWeight: 700, marginTop: 4 }}>{value}</div>
      {sub && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
          {sub}
        </div>
      )}
    </div>
  );
}

function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0)} ${units[i]}`;
}

function fmtRate(perSec: number): string {
  if (!perSec || perSec < 0) return '0 /s';
  if (perSec >= 1000) return `${(perSec / 1000).toFixed(1)}k /s`;
  if (perSec >= 1) return `${perSec.toFixed(0)} /s`;
  return `${perSec.toFixed(2)} /s`;
}
