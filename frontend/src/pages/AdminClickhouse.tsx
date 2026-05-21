import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// AdminClickhouse — v0.5.329. Datadog-style CH self-stats:
// slow queries, in-flight merges, part hotspots, replication lag.
// Reads from /api/admin/clickhouse (server caches 5s, polls every
// 10s here so the operator can watch merge pressure ease/spike in
// near-real-time). Pauses on document.hidden per CLAUDE.md.

type Slow = {
  query: string; elapsedMs: number; memoryMb: number;
  readRows: number; resultRows: number; eventTimeNs: number; user: string;
};
type Merge = {
  database: string; table: string;
  elapsedSec: number; progressPct: number;
  rowsRead: number; mergedSizeBytes: number;
};
type PartHot = {
  database: string; table: string;
  parts: number; rowsTotal: number; bytesTotal: number;
};
type RepLag = {
  database: string; table: string;
  queueSize: number; absoluteDelaySec: number;
};
type CHHealth = {
  slowQueries: Slow[] | null;
  merges: Merge[] | null;
  partHotspots: PartHot[] | null;
  replicationLag?: RepLag[] | null;
  generatedAt: number;
};

export default function AdminClickhousePage() {
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [data, setData] = useState<CHHealth | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    const fetchOnce = () => {
      api.clickhouseHealth()
        .then(d => { if (!cancelled) setData(d as CHHealth ?? null); })
        .catch(() => { if (!cancelled) setData(null); });
    };
    fetchOnce();
    const id = setInterval(() => { if (!document.hidden) fetchOnce(); }, 10_000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  // Highest-volume merge — surfaced in the page header so the
  // operator sees pressure without reading the table.
  const peakMergeSec = data?.merges?.reduce((m, x) => Math.max(m, x.elapsedSec), 0) ?? 0;
  const peakParts = data?.partHotspots?.reduce((m, x) => Math.max(m, x.parts), 0) ?? 0;

  return (
    <>
      <Topbar title="ClickHouse" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
          gap: 12, marginBottom: 18,
        }}>
          <KPI label="Slow queries · 1h" value={fmtNum(data?.slowQueries?.length ?? 0)}
               sub=">500ms" />
          <KPI label="Active merges" value={fmtNum(data?.merges?.length ?? 0)}
               sub={peakMergeSec > 0 ? `peak ${peakMergeSec.toFixed(0)}s` : ''} />
          <KPI label="Part hotspots" value={fmtNum(data?.partHotspots?.length ?? 0)}
               sub={peakParts > 0 ? `max ${peakParts} parts` : ''}
               cls={peakParts > 300 ? 'warn' : peakParts > 600 ? 'err' : ''} />
          <KPI label="Replication lag rows"
               value={fmtNum(data?.replicationLag?.length ?? 0)}
               sub="cluster only" />
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load ClickHouse health" />}

        {data && (
          <>
            <Section title="Slow queries (>500ms, last 1h)">
              {(!data.slowQueries || data.slowQueries.length === 0)
                ? <EmptyNote text="No slow queries in the last hour" />
                : (
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Time</th>
                          <th>User</th>
                          <th className="num">Elapsed</th>
                          <th className="num">Memory</th>
                          <th className="num">Read rows</th>
                          <th>Query</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.slowQueries.map((q, i) => (
                          <tr key={i}>
                            <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                              {new Date(q.eventTimeNs / 1e6).toLocaleTimeString()}
                            </td>
                            <td className="mono" style={{ fontSize: 11 }}>{q.user || '—'}</td>
                            <td className="num mono">{q.elapsedMs.toFixed(0)} ms</td>
                            <td className="num mono">{q.memoryMb.toFixed(0)} MB</td>
                            <td className="num mono">{fmtNum(q.readRows)}</td>
                            <td className="mono" style={{
                              fontSize: 11, maxWidth: 540,
                              overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                            }} title={q.query}>
                              {q.query.replace(/\s+/g, ' ').slice(0, 200)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            <Section title="In-flight merges">
              {(!data.merges || data.merges.length === 0)
                ? <EmptyNote text="No merges in flight — CH idle or up-to-date" />
                : (
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Database</th><th>Table</th>
                          <th className="num">Elapsed</th>
                          <th className="num">Progress</th>
                          <th className="num">Rows read</th>
                          <th className="num">Merged size</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.merges.map((m, i) => (
                          <tr key={i}>
                            <td className="mono">{m.database}</td>
                            <td className="mono">{m.table}</td>
                            <td className="num mono">{m.elapsedSec.toFixed(1)}s</td>
                            <td className="num mono">{m.progressPct.toFixed(0)}%</td>
                            <td className="num mono">{fmtNum(m.rowsRead)}</td>
                            <td className="num mono">{fmtBytes(m.mergedSizeBytes)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            <Section title="Part hotspots (active parts per table, top 15)">
              {(!data.partHotspots || data.partHotspots.length === 0)
                ? <EmptyNote text="No part data available" />
                : (
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Database</th><th>Table</th>
                          <th className="num">Parts</th>
                          <th className="num">Rows</th>
                          <th className="num">Bytes</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.partHotspots.map((p, i) => (
                          <tr key={i}>
                            <td className="mono">{p.database}</td>
                            <td className="mono">{p.table}</td>
                            <td className="num mono" style={{
                              color: p.parts > 300 ? 'var(--err)' : p.parts > 150 ? 'var(--warn)' : 'var(--text)',
                              fontWeight: p.parts > 150 ? 600 : 400,
                            }}>{fmtNum(p.parts)}</td>
                            <td className="num mono">{fmtNum(p.rowsTotal)}</td>
                            <td className="num mono">{fmtBytes(p.bytesTotal)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            {data.replicationLag && data.replicationLag.length > 0 && (
              <Section title="Replication lag (cluster only)">
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Database</th><th>Table</th>
                        <th className="num">Queue</th>
                        <th className="num">Absolute delay</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.replicationLag.map((r, i) => (
                        <tr key={i}>
                          <td className="mono">{r.database}</td>
                          <td className="mono">{r.table}</td>
                          <td className="num mono">{fmtNum(r.queueSize)}</td>
                          <td className="num mono">{r.absoluteDelaySec}s</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Section>
            )}
          </>
        )}
      </div>
    </>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 24 }}>
      <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 8 }}>{title}</h3>
      {children}
    </div>
  );
}

function EmptyNote({ text }: { text: string }) {
  return (
    <div style={{
      padding: '14px 16px', borderRadius: 6,
      background: 'var(--bg2)', border: '1px dashed var(--border)',
      fontSize: 12, color: 'var(--text3)',
    }}>{text}</div>
  );
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: '10px 14px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{
        fontSize: 22, fontWeight: 700, marginTop: 4,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>{sub}</div>
      )}
    </div>
  );
}

function fmtBytes(n: number): string {
  if (!n) return '0';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}
