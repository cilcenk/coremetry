import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { IconFlame } from '@/components/icons';
import { ServicePicker } from '@/components/ServicePicker';
import { BreakdownBar, KindBadge } from '@/components/KindBadge';
import { api } from '@/lib/api';
import { tsShort, timeRangeToNs, fmtNum } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ProfileRow, ProfileHotspotsResponse, TimeRange } from '@/lib/types';

// Columns for the shared sortable + resizable DataTable.
const PROFILE_COLS: DataTableColumn<ProfileRow>[] = [
  { id: 'time',    label: 'Time',    sortValue: p => p.startTime,        naturalDir: 'desc', width: 170 },
  { id: 'service', label: 'Service', sortValue: p => p.serviceName,      naturalDir: 'asc',  width: 200 },
  { id: 'type',    label: 'Type',    sortValue: p => p.profileType,      naturalDir: 'asc',  width: 110 },
  { id: 'window',  label: 'Window',  sortValue: p => p.durationMs,       naturalDir: 'desc', width: 110 },
  { id: 'samples', label: 'Samples', sortValue: p => p.sampleCount,      naturalDir: 'desc', width: 120 },
  { id: 'host',    label: 'Host',    sortValue: p => p.hostName ?? '',   naturalDir: 'asc',  width: 180 },
];
type HotspotRow = ProfileHotspotsResponse['hotspots'][number];
const HOTSPOT_COLS: DataTableColumn<HotspotRow>[] = [
  { id: 'method',   label: 'Method',   sortValue: h => h.name,        naturalDir: 'asc',  width: 280 },
  { id: 'location', label: 'Location', sortValue: h => h.file ?? '',  naturalDir: 'asc',  width: 240 },
  { id: 'self',     label: 'Self',     sortValue: h => h.self,  numeric: true, naturalDir: 'desc', width: 160 },
  { id: 'total',    label: 'Total',    sortValue: h => h.total, numeric: true, naturalDir: 'desc', width: 160 },
  { id: 'paths',    label: 'Paths',    sortValue: h => h.paths, numeric: true, naturalDir: 'desc', width: 90 },
];

const TYPES = [
  { v: '', label: 'All types' },
  { v: 'cpu', label: 'CPU' },
  { v: 'heap', label: 'Heap' },
  { v: 'goroutine', label: 'Goroutine' },
  { v: 'alloc', label: 'Alloc' },
];

export default function ProfilingPage() {
  // URL-bound state so the service detail page can deep-link
  // operators into a pre-filtered profiling view (v0.5.161).
  // Range stays local — the topbar picker mutates it post-mount
  // and bookmarks aren't time-stable anyway.
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const service = params.get('service') || '';
  const ptype = params.get('type') || '';
  // `view` toggles between the per-profile list (default) and
  // the service-level aggregated hotspot panel. The hotspot
  // panel needs a service to be selected — backend rejects
  // requests without one to keep aggregation bounded.
  const view = (params.get('view') === 'hotspots' ? 'hotspots' : 'list') as 'list' | 'hotspots';
  const setService = (v: string) => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('service', v); else p.delete('service');
    return p;
  }, { replace: true });
  const setPtype = (v: string) => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('type', v); else p.delete('type');
    return p;
  }, { replace: true });
  const setView = (v: 'list' | 'hotspots') => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (v === 'hotspots') p.set('view', 'hotspots'); else p.delete('view');
    return p;
  }, { replace: true });
  const [data, setData] = useState<ProfileRow[] | null | undefined>(undefined);
  const [hotspots, setHotspots] = useState<ProfileHotspotsResponse | null | undefined>(undefined);
  // Shared sortable + resizable profiles list (unconditional hook).
  const profileDt = useDataTable<ProfileRow>({
    storageKey: 'profiles', columns: PROFILE_COLS,
    rows: data ?? [], initialSort: { id: 'time', dir: 'desc' },
  });
  // Setup recipes accordion — empty/no profiles is the common
  // first-run state, and operators end up grepping the demo source
  // to figure out the wire format. Surfacing copy-paste snippets
  // here turns "is profiling working?" into a 90-second exercise.
  const [setupOpen, setSetupOpen] = useState(false);

  useEffect(() => {
    if (view !== 'list') return;
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.profiles({ service, type: ptype, from, to, limit: 200 })
      .then(p => setData(p ?? []))
      .catch(() => setData(null));
  }, [view, range, service, ptype]);

  // Hotspots fetch — service is a hard requirement; skip
  // entirely without one and let the empty-state nudge the
  // operator to pick.
  const hsRange = useMemo(() => timeRangeToNs(range), [range]);
  useEffect(() => {
    if (view !== 'hotspots' || !service) {
      setHotspots(undefined);
      return;
    }
    setHotspots(undefined);
    api.profileHotspots({
      service,
      type: ptype || 'cpu',
      from: hsRange.from,
      to: hsRange.to,
      limit: 200,
      top: 100,
    })
      .then(r => setHotspots(r ?? null))
      .catch(() => setHotspots(null));
  }, [view, service, ptype, hsRange]);

  return (
    <>
      <Topbar title="Profiling" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls">
          {/* View tabs — per-profile list (the original page)
              vs aggregated method hotspots across the time
              window. Hotspot tab needs a service; the empty
              state nudges the operator if they switch without
              picking one. */}
          <div style={{ display: 'inline-flex', borderRadius: 6, overflow: 'hidden',
                        border: '1px solid var(--border)' }}>
            <ViewTab cur={view} v="list"     label="Profiles"  onClick={setView} />
            <ViewTab cur={view} v="hotspots" label="Hotspots"  onClick={setView} />
          </div>
          <ServicePicker value={service} onChange={setService}
            placeholder="Service…" width={170} />
          <select value={ptype} onChange={e => setPtype(e.target.value)}>
            {TYPES.map(t => <option key={t.v} value={t.v}>{t.label}</option>)}
          </select>
          <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 'auto' }}>
            {view === 'hotspots'
              ? 'Aggregated method hotspots across the selected window.'
              : 'Continuous CPU + heap profiles, captured in 5s windows.'}
          </span>
          {/* Pyroscope is the de-facto continuous-profiling tool.
              When the bundled Compose stack runs it's at port 4040;
              the link is harmless if the operator hasn't deployed it. */}
          <a href={pyroscopeURL()} target="_blank" rel="noopener" className="sec"
             style={{ padding: '5px 12px', fontSize: 12, textDecoration: 'none',
                      borderRadius: 6, border: '1px solid var(--border)',
                      color: 'var(--accent2)',
                      display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <IconFlame size={14} /> Open Pyroscope ↗
          </a>
          <button onClick={() => setSetupOpen(o => !o)} className="sec"
                  style={{ padding: '5px 12px', fontSize: 12 }}>
            {setupOpen ? '× Close setup' : '⌘ Setup recipes'}
          </button>
        </div>

        {setupOpen && <SetupRecipes />}

        {view === 'list' && (
          <>
            {data === undefined && <Spinner />}
            {data === null && (
              <Empty icon="⚠" title="Failed to load profiles">
                The backend rejected the request — try widening the time range.
              </Empty>
            )}
            {data && data.length === 0 && (
              <Empty icon={<IconFlame size={28} />} title="No profiles yet">
                The demo pushes profiles every 10s to <code>POST /v1/profiles</code>.
              </Empty>
            )}
            {data && data.length > 0 && (
              <div className="table-wrap">
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={profileDt} />
                  <DataTableHead dt={profileDt} />
                  <tbody>
                    {profileDt.sortedRows.map(p => (
                      <tr key={p.profileId} onClick={() => navigate(`/profile?id=${p.profileId}`)}>
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
          </>
        )}

        {view === 'hotspots' && (
          <HotspotsPanel service={service} hotspots={hotspots} />
        )}
      </div>
    </>
  );
}

function ViewTab({ cur, v, label, onClick }:
  { cur: 'list' | 'hotspots'; v: 'list' | 'hotspots'; label: string;
    onClick: (v: 'list' | 'hotspots') => void }) {
  const active = cur === v;
  return (
    <button onClick={() => onClick(v)} className={active ? '' : 'sec'}
      style={{
        padding: '5px 14px', fontSize: 12, fontWeight: 600,
        background: active ? 'var(--accent2)' : 'transparent',
        color: active ? 'white' : 'var(--text2)',
        border: 0, cursor: 'pointer', borderRadius: 0,
      }}>
      {label}
    </button>
  );
}

// HotspotsPanel — service-level aggregated hotspots. The
// backend merges every profile in the window into a virtual
// flame tree, rolls it up by function name, and returns the
// top 100 — the same row shape MethodHotspots uses, so the
// table layout is identical to a single profile's view.
function HotspotsPanel({ service, hotspots }: {
  service: string;
  hotspots: ProfileHotspotsResponse | null | undefined;
}) {
  // Sortable + resizable hotspots table — hook BEFORE the early returns
  // (react-hooks rules-of-hooks).
  const hsDt = useDataTable<HotspotRow>({
    storageKey: 'hotspots', columns: HOTSPOT_COLS,
    rows: hotspots?.hotspots ?? [],
  });
  if (!service) {
    return (
      <Empty icon={<IconFlame size={28} />} title="Pick a service">
        Aggregated hotspots roll N profiles into one view — pick a service to begin.
      </Empty>
    );
  }
  if (hotspots === undefined) return <Spinner />;
  if (hotspots === null) {
    return (
      <Empty icon="⚠" title="Failed to load hotspots">
        The backend rejected the request — try widening the time range.
      </Empty>
    );
  }
  if (!hotspots.hotspots || hotspots.hotspots.length === 0) {
    return (
      <Empty icon={<IconFlame size={28} />} title="No profiles in this window">
        Widen the time range, or check that the service is pushing profiles.
      </Empty>
    );
  }
  const totalSamples = hotspots.totalSamples || 1;
  return (
    <>
      <BreakdownBar b={hotspots.breakdown} />
      <div style={{
        marginBottom: 10, padding: 10, borderRadius: 6,
        background: 'var(--bg1)', border: '1px solid var(--border)',
        fontSize: 12, color: 'var(--text2)',
        display: 'flex', gap: 16, flexWrap: 'wrap',
      }}>
        <span><b style={{ color: 'var(--text)' }}>{hotspots.profilesUsed}</b> profiles merged</span>
        <span><b style={{ color: 'var(--text)' }}>{fmtNum(hotspots.totalSamples)}</b> total samples</span>
        <span><b style={{ color: 'var(--text)' }}>{hotspots.hotspots.length}</b> unique methods shown</span>
        {hotspots.profilesFailed > 0 && (
          <span style={{ color: 'var(--warn)' }}>
            {hotspots.profilesFailed} unparseable
          </span>
        )}
      </div>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={hsDt} />
          <DataTableHead dt={hsDt} />
          <tbody>
            {hsDt.sortedRows.map((h, i) => {
              const selfPct = (h.self / totalSamples) * 100;
              const totalPct = (h.total / totalSamples) * 100;
              return (
                <tr key={i} style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                  <td className="mono" style={{ fontSize: 12, wordBreak: 'break-all' }}>
                    {h.name}<KindBadge kind={h.kind} />
                  </td>
                  <td className="mono" style={{ fontSize: 11, color: 'var(--text2)', wordBreak: 'break-all' }}>
                    {h.file ? `${h.file}${h.line ? `:${h.line}` : ''}` : '—'}
                  </td>
                  <td className="num mono"><HotspotBar pct={selfPct} value={h.self} /></td>
                  <td className="num mono"><HotspotBar pct={totalPct} value={h.total} /></td>
                  <td className="num mono">{h.paths.toLocaleString()}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

function HotspotBar({ pct, value }: { pct: number; value: number }) {
  const safe = Math.max(0, Math.min(100, pct));
  return (
    <div style={{ position: 'relative', minWidth: 140 }}>
      <div style={{
        position: 'absolute', inset: 0,
        background: 'linear-gradient(to right, var(--accent2) 0%, var(--accent2) ' + safe + '%, transparent ' + safe + '%)',
        opacity: 0.18, borderRadius: 2,
      }} />
      <span style={{ position: 'relative', fontSize: 11 }}>
        {value.toLocaleString()} <span style={{ color: 'var(--text3)' }}>({safe.toFixed(1)}%)</span>
      </span>
    </div>
  );
}

// pyroscopeURL — same host as Coremetry, port 4040 (Pyroscope's default).
// Override at build time with VITE_PYROSCOPE_URL for prod.
function pyroscopeURL(): string {
  if (typeof window === 'undefined') return '';
  const env = import.meta.env.VITE_PYROSCOPE_URL;
  if (env) return env;
  return `${window.location.protocol}//${window.location.hostname}:4040`;
}

// SetupRecipes — copy-paste continuous-profiling integration snippets
// per language. Each recipe POSTs pprof bytes (or a wrapper that
// converts) to /v1/profiles with the four required headers
// (X-Coremetry-Service / Host / Profile-Type / Start-Time-Ns +
// optional Duration-Ns). The endpoint is OTel-agnostic and accepts
// raw bytes, so even non-Go runtimes that emit pprof through a
// converter (py-spy → pprof, async-profiler → pprof) ship straight
// to Coremetry without an OpenTelemetry Collector hop.
function SetupRecipes() {
  const endpoint = typeof window !== 'undefined'
    ? `${window.location.protocol}//${window.location.host}`
    : 'http://coremetry:8088';
  const tabs: { key: string; label: string; body: React.ReactNode }[] = [
    { key: 'go', label: 'Go', body: <GoRecipe endpoint={endpoint} /> },
    { key: 'python', label: 'Python', body: <PythonRecipe endpoint={endpoint} /> },
    { key: 'java', label: 'Java', body: <JavaRecipe endpoint={endpoint} /> },
    { key: 'node', label: 'Node.js', body: <NodeRecipe endpoint={endpoint} /> },
    { key: 'pyroscope', label: 'Pyroscope', body: <PyroscopeRecipe endpoint={endpoint} /> },
    { key: 'curl', label: 'curl', body: <CurlRecipe endpoint={endpoint} /> },
  ];
  const [active, setActive] = useState(tabs[0].key);
  const cur = tabs.find(t => t.key === active) ?? tabs[0];
  return (
    <div style={{
      marginTop: 12, marginBottom: 18, padding: 14, borderRadius: 8,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 12 }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>Wire your service</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          POST pprof bytes to <code>/v1/profiles</code> · headers carry the metadata · no agent / collector required
        </span>
      </div>
      <div style={{ display: 'flex', gap: 4, marginBottom: 12,
                    borderBottom: '1px solid var(--border)' }}>
        {tabs.map(t => (
          <button key={t.key} onClick={() => setActive(t.key)}
            style={{
              padding: '5px 14px', fontSize: 12, fontWeight: 600, cursor: 'pointer',
              background: 'transparent', border: 'none', borderBottom: '2px solid',
              borderColor: active === t.key ? 'var(--accent)' : 'transparent',
              color: active === t.key ? 'var(--text)' : 'var(--text3)',
            }}>
            {t.label}
          </button>
        ))}
      </div>
      {cur.body}
      <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text3)' }}>
        Required headers on every push: <code>X-Coremetry-Service</code>,
        {' '}<code>X-Coremetry-Host</code>, <code>X-Coremetry-Profile-Type</code>
        {' '}(cpu / heap / goroutine / alloc), <code>X-Coremetry-Start-Time-Ns</code>.
        Optional: <code>X-Coremetry-Duration-Ns</code> for sampled profiles.
      </div>
    </div>
  );
}

function CodeBlock({ code, lang }: { code: string; lang: string }) {
  const [copied, setCopied] = useState(false);
  const onCopy = () => {
    navigator.clipboard.writeText(code).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };
  return (
    <div style={{ position: 'relative' }}>
      <button onClick={onCopy} className="sec"
        style={{
          position: 'absolute', top: 6, right: 6, fontSize: 10, padding: '2px 8px',
          background: 'var(--bg3)',
        }}>
        {copied ? '✓ copied' : 'Copy'}
      </button>
      <pre style={{
        margin: 0, padding: 12, background: 'var(--bg)',
        border: '1px solid var(--border)', borderRadius: 4,
        fontSize: 11, lineHeight: 1.55, overflowX: 'auto',
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      }} data-lang={lang}>
        <code>{code}</code>
      </pre>
    </div>
  );
}

function GoRecipe({ endpoint }: { endpoint: string }) {
  const code = `// runtime/pprof + tiny ticker — no extra deps.
// Drop-in: import this package once from main.go.
package main

import (
\t"bytes"
\t"net/http"
\t"os"
\t"runtime/pprof"
\t"time"
)

const coremetryEndpoint = "${endpoint}"

func init() {
\tservice := os.Getenv("OTEL_SERVICE_NAME")
\thost, _ := os.Hostname()
\tgo profileLoop(service, host)
}

func profileLoop(service, host string) {
\tfor {
\t\t// CPU: 30s sample window, every 60s
\t\tvar buf bytes.Buffer
\t\tstart := time.Now()
\t\tif err := pprof.StartCPUProfile(&buf); err == nil {
\t\t\ttime.Sleep(30 * time.Second)
\t\t\tpprof.StopCPUProfile()
\t\t\tpush(service, host, "cpu", start, 30*time.Second, buf.Bytes())
\t\t}
\t\t// Heap snapshot
\t\tvar h bytes.Buffer
\t\tpprof.WriteHeapProfile(&h)
\t\tpush(service, host, "heap", time.Now(), 0, h.Bytes())
\t\ttime.Sleep(30 * time.Second)
\t}
}

func push(svc, host, kind string, start time.Time, dur time.Duration, data []byte) {
\treq, _ := http.NewRequest("POST", coremetryEndpoint+"/v1/profiles", bytes.NewReader(data))
\treq.Header.Set("Content-Type", "application/octet-stream")
\treq.Header.Set("X-Coremetry-Service", svc)
\treq.Header.Set("X-Coremetry-Host", host)
\treq.Header.Set("X-Coremetry-Profile-Type", kind)
\treq.Header.Set("X-Coremetry-Start-Time-Ns", itoa(start.UnixNano()))
\tif dur > 0 {
\t\treq.Header.Set("X-Coremetry-Duration-Ns", itoa(int64(dur)))
\t}
\thttp.DefaultClient.Do(req)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }`;
  return <CodeBlock code={code} lang="go" />;
}

function PythonRecipe({ endpoint }: { endpoint: string }) {
  const code = `# py-spy → pprof bytes → POST. Run as a sidecar so the target
# process needs no code change. Requires py-spy (\`pip install py-spy\`).
#!/usr/bin/env python3
import os, time, subprocess, requests, socket

SVC      = os.environ["OTEL_SERVICE_NAME"]
TARGET   = int(os.environ["TARGET_PID"])
ENDPOINT = "${endpoint}/v1/profiles"
HOST     = socket.gethostname()
WINDOW   = 30  # seconds per CPU sample

while True:
    start_ns = time.time_ns()
    out = "/tmp/py.pprof"
    subprocess.run(
        ["py-spy", "record", "-p", str(TARGET),
         "-o", out, "-d", str(WINDOW), "--format", "raw"],
        check=True,
    )
    with open(out, "rb") as f:
        data = f.read()
    requests.post(ENDPOINT, data=data, headers={
        "Content-Type": "application/octet-stream",
        "X-Coremetry-Service": SVC,
        "X-Coremetry-Host": HOST,
        "X-Coremetry-Profile-Type": "cpu",
        "X-Coremetry-Start-Time-Ns": str(start_ns),
        "X-Coremetry-Duration-Ns": str(WINDOW * 1_000_000_000),
    })
    time.sleep(30)`;
  return <CodeBlock code={code} lang="python" />;
}

function JavaRecipe({ endpoint }: { endpoint: string }) {
  const code = `# async-profiler ships pprof natively (\`-o pprof\`) since 2.9.
# Run as a sidecar attaching to the JVM via PID, push pprof to Coremetry.
# Requires: async-profiler binary in /opt/async-profiler.
#!/usr/bin/env bash
set -euo pipefail

ENDPOINT="${endpoint}/v1/profiles"
SVC="\${OTEL_SERVICE_NAME}"
HOST="\$(hostname)"
TARGET="\${TARGET_PID}"
WINDOW=30

while true; do
  START_NS="\$(date +%s%N)"
  OUT=/tmp/java.pprof
  /opt/async-profiler/profiler.sh \\
      -e cpu -d "\${WINDOW}" -o pprof -f "\${OUT}" "\${TARGET}"

  curl -sS -X POST "\${ENDPOINT}" \\
    -H "Content-Type: application/octet-stream" \\
    -H "X-Coremetry-Service: \${SVC}" \\
    -H "X-Coremetry-Host: \${HOST}" \\
    -H "X-Coremetry-Profile-Type: cpu" \\
    -H "X-Coremetry-Start-Time-Ns: \${START_NS}" \\
    -H "X-Coremetry-Duration-Ns: \$((WINDOW * 1000000000))" \\
    --data-binary "@\${OUT}"

  sleep 30
done`;
  return <CodeBlock code={code} lang="bash" />;
}

function NodeRecipe({ endpoint }: { endpoint: string }) {
  const code = `// pprof (npm package) writes Node.js heap + CPU profiles
// in pprof format directly. \`npm i pprof\`.
import * as pprof from 'pprof';
import * as os from 'os';
import * as http from 'http';

const ENDPOINT = '${endpoint}/v1/profiles';
const SVC      = process.env.OTEL_SERVICE_NAME!;
const HOST     = os.hostname();
const WINDOW_MS = 30_000;

setInterval(async () => {
  const startNs = process.hrtime.bigint();
  // CPU profile, 30s window
  const cpuBuf = await pprof.time.profile({ durationMillis: WINDOW_MS });
  const cpuPprof = await pprof.encode(cpuBuf);
  push(cpuPprof, 'cpu', startNs, BigInt(WINDOW_MS) * 1_000_000n);

  // Heap snapshot — instantaneous
  const heap = pprof.heap.profile();
  const heapPprof = await pprof.encode(heap);
  push(heapPprof, 'heap', process.hrtime.bigint(), 0n);
}, 60_000);

function push(data: Buffer, kind: 'cpu' | 'heap', startNs: bigint, durNs: bigint) {
  const req = http.request(ENDPOINT, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/octet-stream',
      'X-Coremetry-Service': SVC,
      'X-Coremetry-Host': HOST,
      'X-Coremetry-Profile-Type': kind,
      'X-Coremetry-Start-Time-Ns': startNs.toString(),
      ...(durNs > 0n ? { 'X-Coremetry-Duration-Ns': durNs.toString() } : {}),
    },
  });
  req.end(data);
}`;
  return <CodeBlock code={code} lang="typescript" />;
}

function PyroscopeRecipe({ endpoint }: { endpoint: string }) {
  const code = `# Grafana Alloy / Pyroscope OSS agent — point it at
# \`${endpoint}/ingest\` and Coremetry accepts the standard
# Pyroscope wire format (?name=app.cpu{tags}&from=&until=
# + pprof body). No Coremetry-specific exporter required.
#
# Alloy config snippet (river syntax):
pyroscope.write "to_coremetry" {
  endpoint {
    url = "${endpoint}"
  }
}

# Then any pyroscope.scrape / pyroscope.java component:
pyroscope.scrape "demo" {
  forward_to = [pyroscope.write.to_coremetry.receiver]
  targets    = [{__address__ = "host:6060", service_name = "my-service"}]
}

# Or with the OSS pyroscope-agent CLI directly:
pyroscope exec --server-address=${endpoint} \\
               --application-name=my-service \\
               --profile-cpu --profile-allocations \\
               -- ./my-app`;
  return <CodeBlock code={code} lang="bash" />;
}

function CurlRecipe({ endpoint }: { endpoint: string }) {
  const code = `# Smoke-test the ingest path with any pprof file you have
# (\`go tool pprof\` produces them, \`py-spy record --format raw\` produces them,
# async-profiler \`-o pprof\` produces them).

START_NS=$(date +%s%N)
curl -sS -X POST "${endpoint}/v1/profiles" \\
  -H 'Content-Type: application/octet-stream' \\
  -H 'X-Coremetry-Service: smoke-test' \\
  -H "X-Coremetry-Host: $(hostname)" \\
  -H 'X-Coremetry-Profile-Type: cpu' \\
  -H "X-Coremetry-Start-Time-Ns: $START_NS" \\
  -H 'X-Coremetry-Duration-Ns: 30000000000' \\
  --data-binary @/path/to/profile.pprof

# Then watch /profiling for the row to land within ~5 seconds.`;
  return <CodeBlock code={code} lang="bash" />;
}

