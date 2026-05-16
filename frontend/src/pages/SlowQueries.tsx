import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import type { SlowQueryRow, TimeRange } from '@/lib/types';

// /databases/slow-queries — global slow-query catalog (v0.5.165).
// Answers "what query class is burning the most DB time across
// the whole install?". Per-service view stays at /service?name=…
// (the existing DBQueriesPanel); this one is cross-service so
// the platform team can see "payments-api's stale join is
// number-one across all our DB time" without per-service
// pivoting.
//
// Sorted by total wall-clock time (count × avg ms) because that's
// what's actually worth fixing. A 5ms query running a million
// times beats a 5s query running once.
export default function SlowQueriesPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  const [dbSystem, setDbSystem] = useState('');
  const [rows, setRows] = useState<SlowQueryRow[] | null | undefined>(undefined);
  const [expanded, setExpanded] = useState<string | null>(null);

  useEffect(() => {
    const { from, to } = timeRangeToNs(range);
    setRows(undefined);
    api.slowQueries({
      from, to,
      db_system: dbSystem || undefined,
      limit: 200,
    })
      .then(r => setRows(r ?? []))
      .catch(() => setRows(null));
  }, [range, dbSystem]);

  const systems = rows
    ? Array.from(new Set(rows.map(r => r.dbSystem).filter(Boolean))).sort()
    : [];

  return (
    <>
      <Topbar title="Slow queries" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12 }}>
          Cross-service slow-query catalog. Sorted by total wall-clock time —
          what's actually worth optimising. Click a row to expand a real
          sample with literals.
        </div>

        <div className="controls" style={{ marginBottom: 12 }}>
          <select value={dbSystem} onChange={e => setDbSystem(e.target.value)}
            style={{ fontSize: 12, padding: '3px 8px' }}>
            <option value="">All databases</option>
            {systems.map(s => <option key={s} value={s}>{s}</option>)}
          </select>
          {dbSystem && (
            <button className="sec" onClick={() => setDbSystem('')}
              style={{ fontSize: 11, padding: '3px 10px' }}>Clear</button>
          )}
          <Link to="/databases" className="sec"
            style={{ marginLeft: 'auto', fontSize: 11, padding: '4px 10px', textDecoration: 'none' }}>
            ← Database overview
          </Link>
        </div>

        {rows === undefined && <Spinner />}
        {rows === null && <Empty icon="✗" title="Failed to load slow queries" />}
        {rows && rows.length === 0 && (
          <Empty icon="◇" title="No DB spans in this window">
            Either no traffic, or no db.statement attributes were emitted by
            the instrumented apps.
          </Empty>
        )}
        {rows && rows.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th style={{ width: 36 }}></th>
                  <th>Service</th>
                  <th>DB</th>
                  <th>Statement (normalised)</th>
                  <th className="num">Calls</th>
                  <th className="num">Avg ms</th>
                  <th className="num">P99 ms</th>
                  <th className="num">Total time</th>
                  <th className="num">Errors</th>
                </tr>
              </thead>
              <tbody>
                {rows.map(r => {
                  const key = `${r.service}::${r.statement}`;
                  const isExpanded = expanded === key;
                  const totalSec = r.totalMs / 1000;
                  const totalLabel = totalSec >= 60
                    ? `${(totalSec / 60).toFixed(1)} min`
                    : totalSec >= 1
                    ? `${totalSec.toFixed(1)} s`
                    : `${r.totalMs.toFixed(0)} ms`;
                  const p99Color = r.p99Ms > 1000 ? 'var(--err)'
                    : r.p99Ms > 200 ? 'var(--warn)' : undefined;
                  return (
                    <>
                      <tr key={key}
                        onClick={() => setExpanded(isExpanded ? null : key)}
                        style={{ cursor: 'pointer' }}>
                        <td>
                          <span style={{ fontSize: 10, color: 'var(--text3)' }}>
                            {isExpanded ? '▼' : '▶'}
                          </span>
                        </td>
                        <td>
                          <Link to={`/service?name=${encodeURIComponent(r.service)}`}
                            onClick={e => e.stopPropagation()}
                            style={{ fontSize: 12, fontFamily: 'ui-monospace, monospace' }}>
                            {r.service}
                          </Link>
                        </td>
                        <td>
                          <span style={{
                            fontSize: 10, padding: '2px 6px', borderRadius: 3,
                            background: 'var(--bg3)', color: 'var(--text2)',
                            fontFamily: 'ui-monospace, monospace',
                          }}>{r.dbSystem || '?'}</span>
                        </td>
                        <td style={{
                          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                          fontSize: 11, color: 'var(--text)',
                          maxWidth: 540, overflow: 'hidden', textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}>{r.statement}</td>
                        <td className="num mono">{fmtNum(r.count)}</td>
                        <td className="num mono">{r.avgMs.toFixed(1)}</td>
                        <td className="num mono" style={{ color: p99Color }}>
                          {r.p99Ms.toFixed(0)}
                        </td>
                        <td className="num mono" style={{ fontWeight: 600 }}>{totalLabel}</td>
                        <td className="num mono" style={{
                          color: r.errorCount > 0 ? 'var(--err)' : 'var(--text3)',
                        }}>{fmtNum(r.errorCount)}</td>
                      </tr>
                      {isExpanded && (
                        <tr key={key + ':sample'}>
                          <td colSpan={9} style={{
                            background: 'var(--bg2)', padding: 12,
                          }}>
                            <div style={{
                              fontSize: 10, color: 'var(--text3)',
                              textTransform: 'uppercase', letterSpacing: 0.5,
                              marginBottom: 4,
                            }}>Real sample (literals shown)</div>
                            <pre style={{
                              margin: 0, fontSize: 12,
                              fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                              whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                              color: 'var(--text2)',
                            }}>{r.sampleStatement}</pre>
                            <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
                              <Link to={`/traces?service=${encodeURIComponent(r.service)}&db.statement=${encodeURIComponent(r.sampleStatement.slice(0, 60))}`}
                                style={{ marginRight: 12 }}>
                                Search traces with this query →
                              </Link>
                              <span>Max: {r.maxMs.toFixed(0)} ms · P95: {r.p95Ms.toFixed(0)} ms</span>
                            </div>
                          </td>
                        </tr>
                      )}
                    </>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
