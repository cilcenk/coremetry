import { useEffect, useMemo, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs, tsLong, fmtNum } from '@/lib/utils';
import {
  type AIRateTable, mergeRates, costForCall, fmtCost,
} from '@/lib/ai-rates';
import type {
  AICall, AIStats, AICallsTimePoint, TimeRange,
} from '@/lib/types';

// /ai — Coremetry-native AI observability dashboard. The
// Langfuse-alike: every Copilot Explain call lands as one
// ai_calls row, this page surfaces KPIs + per-surface and per-
// provider breakdowns + a paginated recent-calls table with a
// drill-in modal that shows prompt + response samples (capped
// at 4KB each at insert time so a runaway prompt can't blow up
// the row).
//
// Admin-only — prompts can carry telemetry the viewer role
// might not otherwise have access to.
export default function AIObservabilityPage() {
  const [range, setRange] = useUrlRange('24h');
  const [stats, setStats] = useState<AIStats | null | undefined>(undefined);
  const [series, setSeries] = useState<AICallsTimePoint[] | undefined>(undefined);
  const [calls, setCalls] = useState<AICall[] | null | undefined>(undefined);
  const [surface, setSurface] = useState('');
  const [provider, setProvider] = useState('');
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState<AICall | null>(null);
  const [rates, setRates] = useState<AIRateTable>(() => mergeRates(null));

  // Pull operator-set rate overrides; merge over the bundled
  // defaults. Done once per mount — rates change infrequently
  // (manual Settings edits).
  useEffect(() => {
    api.aiRates()
      .then(o => setRates(mergeRates(o)))
      .catch(() => { /* fall back to bundled */ });
  }, []);

  // Poll every 60s so the page stays close to live for the
  // operator watching deployments. Cheap — the stats query is
  // server-side cached 30s.
  useEffect(() => {
    let timer: number | undefined;
    const tick = () => {
      const { from, to } = timeRangeToNs(range);
      api.aiStats({ from, to }).then(setStats).catch(() => setStats(null));
      api.aiSeries({ from, to }).then(s => setSeries(s ?? [])).catch(() => setSeries([]));
    };
    tick();
    // v0.5.248 — skip the refresh when the tab is hidden so
    // backgrounded operator sessions don't re-query CH every
    // minute. Foreground operator sees fresh stats on focus
    // (the next tick fires within 60s).
    timer = window.setInterval(() => { if (!document.hidden) tick(); }, 60_000);
    return () => { if (timer) clearInterval(timer); };
  }, [range]);

  useEffect(() => {
    const { from, to } = timeRangeToNs(range);
    setCalls(undefined);
    api.aiCalls({
      from, to, limit: 200,
      surface: surface || undefined,
      provider: provider || undefined,
      status: status || undefined,
    })
      .then(c => setCalls(c ?? []))
      .catch(() => setCalls(null));
  }, [range, surface, provider, status]);

  return (
    <>
      <Topbar title="AI observability" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12 }}>
          Every Copilot Explain call lands here — latency, tokens, status,
          per-surface breakdown. Prompt + response samples (≤4KB) are kept
          for inspection. Admin-only.
        </div>

        {/* KPI cards */}
        {stats === undefined && <Spinner />}
        {stats === null && (
          <Empty icon="✗" title="Failed to load AI stats">
            Check that the Copilot is configured and that ai_calls table exists.
          </Empty>
        )}
        {stats && (() => {
          // Sum estimated cost across the per-provider breakdown.
          // Skip models we have no rate for; null total means
          // every model was unknown — render "—" instead of $0.
          let totalCost = 0;
          let anyKnown = false;
          for (const r of stats.byProvider) {
            const c = costForCall(rates, r.model, r.inputTokens, r.outputTokens);
            if (c !== null) {
              totalCost += c;
              anyKnown = true;
            }
          }
          const totalCostLabel = anyKnown ? fmtCost(totalCost) : '—';
          return (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))',
              gap: 10, marginBottom: 14,
            }}>
              <KPI label="Total calls" value={fmtNum(stats.totalCalls)} />
              <KPI label="Error rate" value={`${(stats.errorRate * 100).toFixed(2)}%`}
                cls={stats.errorRate > 0.1 ? 'err' : stats.errorRate > 0.02 ? 'warn' : 'ok'} />
              <KPI label="Avg latency" value={`${stats.avgDurationMs.toFixed(0)} ms`} />
              <KPI label="P99 latency" value={`${stats.p99DurationMs.toFixed(0)} ms`} />
              <KPI label="Input tokens" value={fmtNum(stats.inputTokens)} />
              <KPI label="Output tokens" value={fmtNum(stats.outputTokens)} />
              <KPI label="Est cost" value={totalCostLabel} />
              <KPI label="Users" value={fmtNum(stats.distinctUsers)} />
            </div>

            {/* Volume + error timeseries */}
            {series && series.length > 0 && (
              <CallsChart series={series} />
            )}

            {/* Per-surface + per-provider breakdowns */}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: 12, marginTop: 14 }}>
              {stats.bySurface.length > 0 && (
                <BreakdownTable title="By surface (which Explain button)"
                  rows={stats.bySurface.map(r => ({
                    a: r.surface, b: fmtNum(r.calls),
                    c: `${(r.errorRate * 100).toFixed(1)}%`,
                    d: `${r.avgMs.toFixed(0)} ms`,
                  }))}
                  cols={['Surface', 'Calls', 'Err rate', 'Avg ms']}
                  onPickFirst={v => setSurface(v)} />
              )}
              {stats.byProvider.length > 0 && (
                <BreakdownTable title="By provider · model"
                  rows={stats.byProvider.map(r => ({
                    a: `${r.provider} · ${r.model || '—'}`, b: fmtNum(r.calls),
                    c: fmtNum(r.inputTokens) + ' in',
                    d: fmtCost(costForCall(rates, r.model, r.inputTokens, r.outputTokens)),
                  }))}
                  cols={['Provider', 'Calls', 'Input tok', 'Est cost']}
                  onPickFirst={v => setProvider(v.split(' · ')[0])} />
              )}
            </div>
          </>
          );
        })()}

        {/* Filter strip */}
        <div className="controls" style={{ marginTop: 18, marginBottom: 8 }}>
          <input type="search" placeholder="Filter by surface…" aria-label="Filter by surface"
            value={surface} onChange={e => setSurface(e.target.value)}
            style={{ fontSize: 12, padding: '3px 8px', width: 200 }} />
          <select value={provider} onChange={e => setProvider(e.target.value)}
            aria-label="Filter by provider"
            style={{ fontSize: 12 }}>
            <option value="">All providers</option>
            <option value="openai">openai</option>
            <option value="anthropic">anthropic</option>
            <option value="github">github</option>
          </select>
          <select value={status} onChange={e => setStatus(e.target.value)}
            aria-label="Filter by status"
            style={{ fontSize: 12 }}>
            <option value="">All statuses</option>
            <option value="ok">ok</option>
            <option value="error">error</option>
          </select>
          {(surface || provider || status) && (
            <button className="sec" onClick={() => { setSurface(''); setProvider(''); setStatus(''); }}
              style={{ fontSize: 11, padding: '3px 10px' }}>Clear</button>
          )}
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
            {calls ? `${calls.length} call${calls.length === 1 ? '' : 's'}` : ''}
          </span>
          {calls && calls.length > 0 && (
            <button className="sec"
              onClick={() => exportCallsCSV(calls, rates)}
              style={{ fontSize: 11, padding: '3px 10px' }}
              title="Download the currently-filtered calls as a CSV (with computed cost)">
              ↓ CSV
            </button>
          )}
        </div>

        {/* Recent calls table */}
        {calls === undefined && <Spinner />}
        {calls === null && (
          <Empty icon="✗" title="Failed to load calls" />
        )}
        {calls && calls.length === 0 && (
          <Empty icon="◇" title="No AI calls in this window">
            Click an "✨ Explain" button anywhere in Coremetry — it lands here.
          </Empty>
        )}
        {calls && calls.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead><tr>
                <th>Time</th>
                <th>Surface</th>
                <th>Provider · Model</th>
                <th>Status</th>
                <th className="num">Duration</th>
                <th className="num">In / Out tokens</th>
                <th className="num">Cost</th>
                <th>User</th>
              </tr></thead>
              <tbody>
                {calls.map(c => {
                  const cost = costForCall(rates, c.model, c.inputTokens, c.outputTokens);
                  return (
                  <tr key={c.id} onClick={() => setOpen(c)}
                    style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                    <td className="mono" style={{ fontSize: 11 }}>{tsLong(c.createdAt)}</td>
                    <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{c.surface}</td>
                    <td style={{ fontSize: 12 }}>
                      <span style={{ color: 'var(--text2)' }}>{c.provider}</span>
                      {c.model && <span style={{ color: 'var(--text3)' }}> · {c.model}</span>}
                    </td>
                    <td>
                      {c.status === 'ok'
                        ? <span className="badge b-ok">ok</span>
                        : <span className="badge b-err">error</span>}
                    </td>
                    <td className="num mono">{c.durationMs} ms</td>
                    <td className="num mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                      {c.inputTokens} / {c.outputTokens}
                    </td>
                    <td className="num mono" style={{
                      fontSize: 11,
                      color: cost === null ? 'var(--text3)' : 'var(--text2)',
                    }}>{fmtCost(cost)}</td>
                    <td style={{ fontSize: 11, color: 'var(--text3)' }}>
                      {c.userEmail || c.userId || '—'}
                    </td>
                  </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        {open && <CallDrawer call={open} rates={rates} onClose={() => setOpen(null)} />}
      </div>
    </>
  );
}

function KPI({ label, value, cls }: { label: string; value: string; cls?: 'ok' | 'warn' | 'err' }) {
  const color = cls === 'err' ? 'var(--err)'
    : cls === 'warn' ? 'var(--warn)'
    : cls === 'ok' ? 'var(--ok)' : 'var(--text)';
  return (
    <div style={{
      padding: '10px 12px', borderRadius: 6,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{ fontSize: 18, fontWeight: 600, color, marginTop: 4, fontFamily: 'ui-monospace, monospace' }}>
        {value}
      </div>
    </div>
  );
}

function BreakdownTable({ title, rows, cols, onPickFirst }: {
  title: string;
  rows: Array<{ a: string; b: string; c: string; d: string }>;
  cols: [string, string, string, string];
  onPickFirst: (v: string) => void;
}) {
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 12,
    }}>
      <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 8 }}>{title}</div>
      <div className="table-wrap" style={{ maxHeight: 220, overflowY: 'auto' }}>
        <table>
          <thead><tr>
            {cols.map(c => <th key={c}>{c}</th>)}
          </tr></thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i} onClick={() => onPickFirst(r.a)} style={{ cursor: 'pointer' }}>
                <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{r.a}</td>
                <td className="num mono">{r.b}</td>
                <td className="num mono" style={{ fontSize: 11 }}>{r.c}</td>
                <td className="num mono" style={{ fontSize: 11 }}>{r.d}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// CallsChart — tiny inline SVG line chart of call volume + error
// volume + avg-latency. No chart lib — at 120 points the geometry
// is one path per series and there's no need to pay the bundle
// cost of recharts/visx.
function CallsChart({ series }: { series: AICallsTimePoint[] }) {
  const W = 1000, H = 140, PAD = 24;
  const xs = useMemo(() => series.map(p => p.time), [series]);
  const minT = xs[0] ?? 0;
  const maxT = xs[xs.length - 1] ?? 1;
  const maxCalls = Math.max(1, ...series.map(p => p.calls));
  const maxLatency = Math.max(1, ...series.map(p => p.avgMs));
  const xOf = (t: number) => PAD + ((t - minT) / Math.max(1, maxT - minT)) * (W - PAD * 2);
  const yCalls = (v: number) => H - PAD - (v / maxCalls) * (H - PAD * 2);
  const yLatency = (v: number) => H - PAD - (v / maxLatency) * (H - PAD * 2);
  const callsPath = series.map((p, i) =>
    `${i === 0 ? 'M' : 'L'} ${xOf(p.time).toFixed(1)} ${yCalls(p.calls).toFixed(1)}`
  ).join(' ');
  const errorsPath = series.map((p, i) =>
    `${i === 0 ? 'M' : 'L'} ${xOf(p.time).toFixed(1)} ${yCalls(p.errors).toFixed(1)}`
  ).join(' ');
  const latencyPath = series.map((p, i) =>
    `${i === 0 ? 'M' : 'L'} ${xOf(p.time).toFixed(1)} ${yLatency(p.avgMs).toFixed(1)}`
  ).join(' ');
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 10, marginBottom: 14,
    }}>
      <div style={{
        display: 'flex', gap: 14, fontSize: 11, color: 'var(--text3)',
        marginBottom: 4, paddingLeft: PAD,
      }}>
        <span><span style={{ color: 'var(--accent2)' }}>━</span> calls</span>
        <span><span style={{ color: 'var(--err)' }}>━</span> errors</span>
        <span><span style={{ color: 'var(--warn)' }}>━</span> avg latency (ms)</span>
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H}
        preserveAspectRatio="none"
        style={{ display: 'block' }}>
        <path d={callsPath}   stroke="var(--accent2)" strokeWidth={1.5} fill="none" />
        <path d={errorsPath}  stroke="var(--err)"     strokeWidth={1.2} fill="none" />
        <path d={latencyPath} stroke="var(--warn)"    strokeWidth={1.2} fill="none"
          strokeDasharray="3,2" opacity={0.85} />
      </svg>
    </div>
  );
}

function CallDrawer({ call, rates, onClose }: { call: AICall; rates: AIRateTable; onClose: () => void }) {
  const cost = costForCall(rates, call.model, call.inputTokens, call.outputTokens);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  return (
    <>
      <div onClick={onClose}
        style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', zIndex: 30 }} />
      <div style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: 'min(680px, 100vw)',
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        zIndex: 31, overflowY: 'auto',
        boxShadow: '-4px 0 24px rgba(0,0,0,0.3)',
      }}>
        <div style={{
          padding: '14px 18px', borderBottom: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          <span className={`badge ${call.status === 'ok' ? 'b-ok' : 'b-err'}`}>
            {call.status}
          </span>
          <span style={{ fontWeight: 700, fontSize: 13 }}>{call.surface}</span>
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            {call.provider} · {call.model || '—'} · {call.durationMs} ms
          </span>
          <span style={{ flex: 1 }} />
          <button onClick={onClose} className="sec"
            style={{ fontSize: 14, padding: '2px 10px' }}>×</button>
        </div>
        <div style={{ padding: '14px 18px', display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))', gap: 8 }}>
            <Kv k="Time" v={tsLong(call.createdAt)} />
            <Kv k="Input tok" v={String(call.inputTokens)} />
            <Kv k="Output tok" v={String(call.outputTokens)} />
            <Kv k="Est cost" v={fmtCost(cost)} />
            <Kv k="Prompt chars" v={String(call.promptChars)} />
            <Kv k="Resp chars" v={String(call.responseChars)} />
            <Kv k="User" v={call.userEmail || call.userId || '—'} />
            {call.baseUrl && <Kv k="Base URL" v={call.baseUrl} />}
          </div>
          {call.errorMsg && (
            <Section title="Error">
              <pre style={{
                margin: 0, fontSize: 12, color: 'var(--err)',
                whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              }}>{call.errorMsg}</pre>
            </Section>
          )}
          <Section title="Prompt (sample)">
            <pre style={{
              margin: 0, fontSize: 12,
              whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              maxHeight: 280, overflowY: 'auto',
              background: 'var(--bg2)', padding: 10, borderRadius: 4,
              border: '1px solid var(--border)',
            }}>{call.promptSample || '(empty)'}</pre>
          </Section>
          <Section title="Response (sample)">
            <pre style={{
              margin: 0, fontSize: 12,
              whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              maxHeight: 280, overflowY: 'auto',
              background: 'var(--bg2)', padding: 10, borderRadius: 4,
              border: '1px solid var(--border)',
            }}>{call.responseSample || '(empty)'}</pre>
          </Section>
        </div>
      </div>
    </>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div style={{
        fontSize: 11, fontWeight: 600, letterSpacing: 0.4,
        textTransform: 'uppercase', color: 'var(--text3)',
        marginBottom: 6,
      }}>{title}</div>
      {children}
    </div>
  );
}
function Kv({ k, v }: { k: string; v: string }) {
  return (
    <div>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>{k}</div>
      <div style={{ fontSize: 12, fontFamily: 'ui-monospace, monospace', marginTop: 2 }}>{v}</div>
    </div>
  );
}

// CSV escape — RFC 4180 minimum: wrap fields that contain
// commas/quotes/newlines in double quotes, and double-up any
// embedded quotes. We omit BOM since modern Excel handles UTF-8
// fine and operators piping into pandas/duckdb prefer it
// without.
function csvField(v: string | number): string {
  const s = String(v);
  if (/[",\n\r]/.test(s)) {
    return '"' + s.replace(/"/g, '""') + '"';
  }
  return s;
}

// exportCallsCSV — v0.5.174. Bundles the operator's currently-
// filtered ai_calls rows into a CSV file with a computed cost
// column (uses the same rate table the page renders with).
// Prompt + response samples are deliberately omitted — they can
// be 4KB each and the operator can drill in via the table row
// for individual inspection.
function exportCallsCSV(calls: AICall[], rates: AIRateTable) {
  const header = [
    'createdAt', 'surface', 'provider', 'model', 'status',
    'durationMs', 'inputTokens', 'outputTokens', 'estCostUsd',
    'userEmail', 'userId', 'errorMsg',
  ];
  const lines: string[] = [header.join(',')];
  for (const c of calls) {
    const cost = costForCall(rates, c.model, c.inputTokens, c.outputTokens);
    lines.push([
      new Date(c.createdAt / 1e6).toISOString(),
      c.surface, c.provider, c.model, c.status,
      c.durationMs, c.inputTokens, c.outputTokens,
      cost === null ? '' : cost.toFixed(6),
      c.userEmail ?? '', c.userId ?? '',
      c.errorMsg ?? '',
    ].map(csvField).join(','));
  }
  const blob = new Blob([lines.join('\n')], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `coremetry-ai-calls-${new Date().toISOString().slice(0, 10)}.csv`;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}
