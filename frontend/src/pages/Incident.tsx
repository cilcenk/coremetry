import { Suspense, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Link } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, AlertTriangle, Bell, Check, MessageSquare, Zap, Paperclip, PenLine } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { CopilotExplain } from '@/components/CopilotExplain';
import { OverviewChart } from '@/pages/service/charts/OverviewChart';
import {
  useIncident, useIncidentEvents, useIncidentProblems, useServiceDeploys, keys,
} from '@/lib/queries';
import { api } from '@/lib/api';
import { metricQuery } from '@/lib/metricQuery';
import { tsLong } from '@/lib/utils';
import type { Incident } from '@/lib/types';
import { Button } from '@/components/ui/Button';

export default function IncidentPage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp] = useSearchParams();
  const { user } = useAuth();
  const id = sp.get('id') ?? '';
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  const qc = useQueryClient();

  // Three parallel queries — incident detail, timeline, attached problems.
  const incQ = useIncident(id);
  const timelineQ = useIncidentEvents(id);
  const problemsQ = useIncidentProblems(id);
  const inc = incQ.isLoading ? undefined : incQ.isError ? null : incQ.data;
  const timeline = timelineQ.data ?? [];
  const problems = problemsQ.data ?? [];

  const [note, setNote] = useState('');
  const [postmortemDraft, setPostmortemDraft] = useState('');
  const [editingPM, setEditingPM] = useState(false);

  useEffect(() => {
    if (inc && !editingPM) setPostmortemDraft(inc.postmortem ?? '');
  }, [inc, editingPM]);

  // Impact window: incident start → resolved (or "now" captured ONCE so an
  // ongoing incident's window doesn't tick every render → infinite refetch,
  // the timeRangeToNs(range)-in-JSX pitfall). Hooks run unconditionally (before
  // the early returns) per the rules of hooks; they no-op until the service is
  // known via `enabled`.
  const svc = incQ.data?.service ?? '';
  const win = useMemo(() => {
    const from = incQ.data?.startedAt ?? 0;
    const to = incQ.data?.resolvedAt ?? Date.now() * 1_000_000;
    return { from, to };
  }, [incQ.data?.startedAt, incQ.data?.resolvedAt, incQ.data?.id]);
  const impactMq = useMemo(() => metricQuery({
    source: 'spanmetrics', metric: 'calls_total', agg: 'error_rate', unit: '%',
    filters: { 'service.name': svc },
  }), [svc]);
  const impactQ = useQuery({
    queryKey: ['incident-impact', svc, win.from, win.to],
    queryFn: () => api.resolveMetric(impactMq, { from: win.from, to: win.to }),
    enabled: !!svc && win.from > 0,
    staleTime: 30_000,
  });
  const deploysQ = useServiceDeploys(svc, win.from, win.to);

  const refresh = () => {
    qc.invalidateQueries({ queryKey: keys.incidents.one(id) });
    qc.invalidateQueries({ queryKey: keys.incidents.events(id) });
    qc.invalidateQueries({ queryKey: keys.incidents.problems(id) });
  };

  if (!id)               return <Empty icon="⚠" title="No incident selected" />;
  if (inc === undefined) return <Spinner />;
  if (inc === null)      return <Empty icon="⚠" title="Incident not found" />;

  const ack     = async () => { await api.ackIncident(id); refresh(); };
  const resolve = async () => { await api.resolveIncident(id); refresh(); };
  const submitNote = async () => {
    if (!note.trim()) return;
    await api.addIncidentNote(id, note.trim()); setNote(''); refresh();
  };
  const savePM = async () => {
    await api.updateIncident(id, { ...inc, postmortem: postmortemDraft });
    setEditingPM(false); refresh();
  };

  const elapsedNs = (inc.resolvedAt ?? Date.now() * 1_000_000) - inc.startedAt;

  // Impact chart series (error-rate over the incident window) → OverviewChart
  // shape: times in unix seconds, one red area series. Deploy marker = the
  // latest deploy inside the window (the design's ▼ vX flag).
  const impactSeries = impactQ.data?.series?.[0]?.points ?? [];
  const impactTimes = impactSeries.map(p => p.time / 1e9);
  const impactData = impactSeries.map(p => p.value);
  const deploy = (() => {
    const ds = (deploysQ.data ?? []).filter(d => d.timeUnixNs >= win.from && d.timeUnixNs <= win.to);
    if (!ds.length) return null;
    const latest = ds.reduce((a, b) => (b.timeUnixNs > a.timeUnixNs ? b : a));
    return { sec: latest.timeUnixNs / 1e9, label: latest.version };
  })();

  return (
    <>
      <Topbar title={`Incident · ${inc.title}`} />
      <div id="content">
        {/* Detail bar — back · status · severity · (spacer) · actions */}
        <div className="rb-bar">
          <Link to="/incidents" className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)', borderRadius: 6,
            fontSize: 12, color: 'var(--text)', textDecoration: 'none',
            display: 'inline-flex', alignItems: 'center', gap: 6,
          }}><ArrowLeft size={14} strokeWidth={1.75} /> Incidents</Link>
          <StatusPill s={inc.status} />
          <SeverityPill s={inc.severity} />
          <span className="spacer" />
          <CopilotExplain kind="incident" id={inc.id} />
          {isAdmin && inc.status === 'open' && <button className="sec" onClick={ack}>Acknowledge</button>}
          {isAdmin && inc.status !== 'resolved' && <button onClick={resolve}>Resolve</button>}
        </div>

        {/* Title + meta chips */}
        <h1 style={{ fontSize: 20, margin: '0 0 4px' }}>{inc.title}</h1>
        <div className="meta-row" style={{ marginBottom: 18 }}>
          {inc.service && <span className="chip"><span className="k">service</span><b className="mono">{inc.service}</b></span>}
          <span className="chip"><span className="k">started</span><b className="mono">{tsLong(inc.startedAt)}</b></span>
          <span className="chip"><span className="k">duration</span><b>{fmtDuration(elapsedNs)}{inc.resolvedAt ? '' : ' (ongoing)'}</b></span>
          {inc.assignee && <span className="chip"><span className="k">assignee</span><b>{inc.assignee}</b></span>}
        </div>

        {inc.summary && (
          <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6, padding: 12, marginBottom: 16, fontSize: 13, color: 'var(--text)' }}>
            {inc.summary}
          </div>
        )}

        {/* Timeline (left) · Impact + Linked + Problems + Postmortem (right) */}
        <div style={{ display: 'grid', gridTemplateColumns: '1.4fr 1fr', gap: 16 }}>
          {/* LEFT — Timeline */}
          <div className="card">
            <div className="ov-card-h"><h3>Timeline</h3><span className="ov-sub">{timeline.length} events</span></div>
            {timeline.length === 0 ? (
              <div className="ov-card-b" style={{ color: 'var(--text3)', fontSize: 12 }}>No events yet.</div>
            ) : (
              <div>
                {timeline.map((e, i) => {
                  const st = eventStyle(e.kind, inc.severity);
                  return (
                    <div className="prob" key={i}>
                      <div className="ic" style={{ background: `color-mix(in srgb, var(${st.token}) 14%, transparent)`, color: `var(${st.token})` }}>
                        {st.icon}
                      </div>
                      <div style={{ minWidth: 0 }}>
                        <div className="ti">{kindLabel(e.kind)}</div>
                        <div className="de">{e.body || e.actor || '—'}{e.actor && e.body ? ` · ${e.actor}` : ''}</div>
                      </div>
                      <div className="tm">{tsLong(e.time)}</div>
                    </div>
                  );
                })}
              </div>
            )}
            {isAdmin && (
              <div className="ov-card-b" style={{ display: 'flex', gap: 8, borderTop: '1px solid var(--border)' }}>
                <input value={note} onChange={e => setNote(e.target.value)}
                  onKeyDown={e => e.key === 'Enter' && submitNote()}
                  placeholder="Add a note (mitigation tried, hypothesis, who's on it)…"
                  style={{ flex: 1 }} />
                <button onClick={submitNote} disabled={!note.trim()}>Add note</button>
              </div>
            )}
          </div>

          {/* RIGHT — Impact, Linked, Attached problems, Postmortem (stacked) */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            {/* Impact */}
            {svc && (
              <div className="card">
                <div className="ov-card-h"><h3>Impact</h3><span className="ov-sub">{svc} · error rate</span></div>
                <div className="ov-card-b" style={{ paddingTop: 10, paddingBottom: 10 }}>
                  {impactTimes.length < 2 ? (
                    <div style={{ height: 110, display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 12 }}>
                      {impactQ.isLoading ? 'Loading…' : 'No data in this window'}
                    </div>
                  ) : (
                    <OverviewChart times={impactTimes} height={110} mode="area" unit="%"
                      series={[{ label: 'error rate', color: 'var(--err)', data: impactData }]}
                      deployAtSec={deploy?.sec ?? null} deployLabel={deploy?.label} />
                  )}
                </div>
              </div>
            )}

            {/* Linked */}
            <div className="card">
              <div className="ov-card-h"><h3>Linked</h3></div>
              <div className="ov-card-b" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {inc.service ? (
                  <Link className="ud-pill" to={`/service?name=${encodeURIComponent(inc.service)}`}>
                    <Paperclip size={15} strokeWidth={1.75} /><span>Service</span>
                    <span className="mult">{inc.service} →</span>
                  </Link>
                ) : (
                  <div style={{ color: 'var(--text3)', fontSize: 12 }}>No linked service.</div>
                )}
                <Link className="ud-pill" to="/alerts">
                  <Bell size={15} strokeWidth={1.75} /><span>Alert rules</span>
                  <span className="mult">view firing →</span>
                </Link>
              </div>
            </div>

            {/* Attached problems (preserved feature) */}
            <div className="card">
              <div className="ov-card-h"><h3>Attached problems</h3>{problems.length > 0 && <span className="ov-sub">{problems.length}</span>}</div>
              <div className="ov-card-b">
                {problems.length === 0
                  ? <div style={{ color: 'var(--text3)', fontSize: 12 }}>No problems attached.</div>
                  : problems.map(pid => (
                      <div key={pid} className="mono" style={{ fontSize: 11, padding: '4px 8px', background: 'var(--bg2)', borderRadius: 4, marginBottom: 4 }}>{pid}</div>
                    ))}
              </div>
            </div>

            {/* Postmortem (preserved feature) */}
            <div className="card">
              <div className="ov-card-h">
                <h3><PenLine size={14} strokeWidth={1.75} style={{ verticalAlign: '-2px', marginRight: 4 }} />Postmortem</h3>
                {isAdmin && !editingPM && (
                  <Button variant="secondary" size="sm" onClick={() => setEditingPM(true)} style={{ marginLeft: 'auto' }}>
                    {inc.postmortem ? 'Edit' : 'Write'}
                  </Button>
                )}
              </div>
              <div className="ov-card-b">
                {editingPM ? (
                  <div>
                    <textarea value={postmortemDraft} onChange={e => setPostmortemDraft(e.target.value)}
                      rows={12} style={{ width: '100%', resize: 'vertical', fontFamily: 'ui-monospace, monospace', fontSize: 12 }}
                      placeholder={POSTMORTEM_TEMPLATE} />
                    <div style={{ display: 'flex', gap: 6, marginTop: 6, justifyContent: 'flex-end' }}>
                      <button className="sec" onClick={() => { setEditingPM(false); setPostmortemDraft(inc.postmortem ?? ''); }}>Cancel</button>
                      <button onClick={savePM}>Save</button>
                    </div>
                  </div>
                ) : inc.postmortem ? (
                  <pre className="mono" style={{ fontSize: 12, whiteSpace: 'pre-wrap', margin: 0 }}>{inc.postmortem}</pre>
                ) : (
                  <div style={{ color: 'var(--text3)', fontSize: 12 }}>
                    {isAdmin ? 'Once resolved, write a blameless postmortem here.' : 'No postmortem yet.'}
                  </div>
                )}
              </div>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}

const POSTMORTEM_TEMPLATE = `## Summary
What happened, in one paragraph.

## Impact
Who was affected and for how long.

## Root cause
The actual technical cause (be specific).

## Resolution
What we did to mitigate and fix.

## Action items
- [ ] Owner — concrete change to prevent recurrence
`;

function StatusPill({ s }: { s: Incident['status'] }) {
  const cls = s === 'open' ? 'b-err' : s === 'acknowledged' ? 'b-warn' : 'b-ok';
  const label = s === 'open' ? 'OPEN' : s === 'acknowledged' ? 'ACK' : 'RESOLVED';
  return <span className={`badge ${cls}`}>{label}</span>;
}
function SeverityPill({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}
function fmtDuration(ns: number): string {
  const sec = Math.floor(ns / 1e9);
  if (sec < 60)    return sec + 's';
  if (sec < 3600)  return Math.floor(sec / 60) + 'm';
  if (sec < 86400) return (sec / 3600).toFixed(1) + 'h';
  return Math.floor(sec / 86400) + 'd';
}
function kindLabel(k: string): string {
  switch (k) {
    case 'created':          return 'Incident opened';
    case 'ack':              return 'Acknowledged';
    case 'resolved':         return 'Resolved';
    case 'note':             return 'Note';
    case 'problem_attached': return 'Problem attached';
    case 'problem_resolved': return 'Problem resolved';
    default:                 return k;
  }
}
// eventStyle — the .prob icon chip glyph + token tint per event kind, matching
// the prototype's level→color map (red/amber/green/accent).
function eventStyle(kind: string, severity: string): { icon: ReactNode; token: string } {
  switch (kind) {
    case 'created':          return { icon: <AlertTriangle size={16} />, token: severity === 'critical' ? '--err' : '--warn' };
    case 'ack':              return { icon: <Bell size={16} />, token: '--warn' };
    case 'resolved':         return { icon: <Check size={16} />, token: '--ok' };
    case 'note':             return { icon: <MessageSquare size={16} />, token: '--accent' };
    case 'problem_attached': return { icon: <AlertTriangle size={16} />, token: '--err' };
    case 'problem_resolved': return { icon: <Check size={16} />, token: '--ok' };
    default:                 return { icon: <Zap size={16} />, token: '--text3' };
  }
}
