import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ArrowLeft, ArrowDownToLine } from 'lucide-react';
import { api } from '@/lib/api';
import { fmtFixed, tsLong } from '@/lib/utils';
import { AIAnalysisPanel } from '@/components/AIAnalysisPanel';
import { Spinner } from '@/components/Spinner';
import { CopilotExplain } from '@/components/CopilotExplain';
import { RootCausePanel } from '@/components/RootCausePanel';
import { ProblemRunbookPanel } from '@/components/ProblemRunbookPanel';
import { IconSparkles } from '@/components/icons';
import { TimeChart } from '@/components/charts/TimeChart';
import { statusColor } from '@/lib/statusColor';
import { fmtDurationNs, fmtStartedTs } from './problemTime';
import type { ExceptionGroup, ExceptionGroupState, Problem } from '@/lib/types';
import { Button } from '@/components/ui/Button';
import { ShareButton } from '@/components/ShareButton';

// ProblemDetail — Variant B (Dynatrace problem feed) full-page details.
// Two surfaces share one skeleton: a top triage bar (badges + ID +
// started/duration + actions) over a 1.5fr/1fr grid — left column
// root-cause card → metric card → vertical timeline; right column
// blast radius → correlated signals → sample pre block. Exception
// groups (ProblemDetail) and firing alert problems (AlertProblemDetail,
// which replaced the v0.5.80 TriageDrawer) render through it.
//
// All colors ride globals.css tokens (.pb-* helpers) so dark / light /
// redhat themes drive them; deploy correlation renders ONLY when the
// row carries recentDeploy — no placeholder, no extra fetch.

const STATE_LABEL: Record<ExceptionGroupState, string> = {
  // 'new' renders OPEN (v0.8.382): NEW is reserved for the yellow
  // first-seen-recently badge on the list — same rule as StateBadge.
  new: 'OPEN', regressed: 'REGRESSED', acknowledged: 'ACK', resolved: 'RESOLVED', ignored: 'IGNORED',
};
const STATE_BADGE: Record<ExceptionGroupState, string> = {
  new: 'b-err', regressed: 'b-err', acknowledged: 'b-warn', resolved: 'b-ok', ignored: 'b-gray',
};

// ShareButton (shared, v0.8.540 — was a local copy here) copies the
// current address-bar URL. The URL is already the canonical shareable
// link: both detail views keep ?problem=<id> / ?exc=<fingerprint> in
// sync via problemLink.ts, so this is a one-click affordance on top of
// "copy from the address bar" for an operator pasting into Slack.

// Esc = back — same muscle memory the old drawer had.
function useEscBack(onBack: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onBack(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onBack]);
}

function Sect({ title, accent, sub, children }: {
  title: string; accent?: boolean; sub?: React.ReactNode; children: React.ReactNode;
}) {
  return (
    <div className="pb-sect">
      <div className={accent ? 'h accent' : 'h'}>
        {title}
        {sub && <span style={{ marginLeft: 'auto', fontWeight: 400, fontSize: 11, color: 'var(--text3)' }}>{sub}</span>}
      </div>
      <div className="b">{children}</div>
    </div>
  );
}

function SignalLink({ to, label, sub }: { to: string; label: string; sub?: string }) {
  return (
    <Link to={to} style={{
      display: 'flex', alignItems: 'baseline', gap: 8,
      padding: '7px 10px', marginBottom: 6,
      border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
      background: 'var(--bg2)', textDecoration: 'none',
      color: 'var(--accent2)', fontSize: 12,
    }}>
      <span style={{ fontWeight: 600 }}>{label} ↗</span>
      {sub && <span style={{ color: 'var(--text3)', fontSize: 11 }}>{sub}</span>}
    </Link>
  );
}

// DeployBox — renders ONLY when the row carries a recentDeploy (spec:
// no placeholder, no "no deploy detected", no extra fetch).
function DeployBox({ version, ageSeconds }: { version: string; ageSeconds: number }) {
  return (
    <div style={{
      fontSize: 12, padding: '8px 12px', marginTop: 10,
      borderRadius: 'var(--radius-sm)',
      background: 'color-mix(in srgb, var(--warn) 10%, transparent)',
      border: '1px solid color-mix(in srgb, var(--warn) 35%, transparent)',
    }}>
      <span style={{ fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <ArrowDownToLine size={13} strokeWidth={1.75} /> Deploy correlation
      </span>{' — '}
      <code className="mono">{version}</code> landed{' '}
      <b>{Math.max(1, Math.round(ageSeconds / 60))}m before</b> this problem opened.
    </div>
  );
}

// ── Exception-group detail (classic, pre-v0.8.426 layout) ───────────────────
//
// v0.8.510 (operator decision): the Variant-B redesign detail (triage bar
// over a 1.5fr/1fr root-cause / blast-radius grid, v0.8.426) is reverted
// here to the classic layout the operator preferred — detail bar (state +
// occurrences + actions) → red exception header → meta chips → AI Analizi →
// full-width occurrences histogram → a 1.4fr/1fr grid (Stack trace LEFT —
// the v0.8.61 ratio fix — | Sample traces). ONLY this exception-group
// surface reverts; AlertProblemDetail (firing alert problems) keeps the
// Variant-B full page below, and AnomaliesPage still owns the ?exc= URL
// open/close flow untouched.
//
// Data access stays on today's code: occurrences are the real server-side
// gap-filled COUNT (v0.8.309), state actions go through
// api.setExceptionGroupState. Two deliberate carry-overs from Variant-B:
// occTimes/occSeries are memoized so a Copy/state re-render doesn't tear
// down the uPlot (v0.5.184 unstable-input class), and the detail bar keeps
// ShareButton + Esc-back as the affordances for the kept shareable link.

export function ProblemDetail({ group, isAdmin, onBack, onChanged }: {
  group: ExceptionGroup;
  isAdmin: boolean;
  onBack: () => void;
  onChanged: () => void;
}) {
  const navigate = useNavigate();
  const [state, setState] = useState<ExceptionGroupState>(group.state);
  const [copied, setCopied] = useState(false);
  useEscBack(onBack);

  const samplesQ = useQuery({
    queryKey: ['exc-samples-detail', group.fingerprint],
    queryFn: () => api.exceptionGroupSamples(group.fingerprint, 100),
    staleTime: 30_000,
  });
  const samples = samplesQ.data ?? [];

  // Occurrences-over-time is a real server-side, gap-filled COUNT over the
  // group's whole window (v0.8.309) — NOT bucketed from the sampled
  // timestamps, which clustered near last_seen and mis-rendered any busy
  // group as a single right-edge spike.
  const occQ = useQuery({
    queryKey: ['exc-occ-detail', group.fingerprint],
    queryFn: () => api.exceptionGroupOccurrences(group.fingerprint),
    staleTime: 30_000,
  });
  const occ = occQ.data ?? [];
  // Memoize the TimeChart inputs on occQ.data: the effect deps include
  // times/series, so per-render arrays would tear down and rebuild the
  // uPlot on EVERY state change (e.g. the Copy button's `copied` flip) —
  // the v0.5.184 unstable-input class. x ticks use TimeChart's default
  // house day-boundary formatter (v0.8.402 fix), same as classic.
  const occTimes = useMemo(() => occ.map(p => p.time / 1e9), [occ]);
  const occSeries = useMemo(() => [{
    key: 'occ', label: 'occurrences', data: occ.map(p => p.count),
    color: statusColor('warn'), type: 'bar' as const,
  }], [occ]);

  // Representative stack = the first sample that carries one.
  const stack = samples.find(s => s.stacktrace)?.stacktrace ?? '';
  const stackLines = stack ? stack.split('\n') : [];

  const act = async (next: ExceptionGroupState) => {
    setState(next);
    try {
      await api.setExceptionGroupState(group.fingerprint, next);
      onChanged();
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err));
      setState(group.state);
    }
  };
  const copyStack = () => {
    if (!stack) return;
    navigator.clipboard?.writeText(stack).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); });
  };

  return (
    <div id="content">
      {/* Detail bar */}
      <div className="rb-bar">
        <Button variant="secondary" onClick={onBack} leftIcon={<ArrowLeft size={14} strokeWidth={1.75} />}>
          Problems
        </Button>
        <span className={`badge ${STATE_BADGE[state]}`}>{STATE_LABEL[state]}</span>
        <span className="badge b-gray">{group.occurrences.toLocaleString()} occurrences</span>
        <span className="spacer" />
        <ShareButton copiedLabel="Copied" />
        {isAdmin && (state === 'new' || state === 'regressed' || state === 'acknowledged') && (
          <>
            {state !== 'acknowledged' && <button className="sec" onClick={() => act('acknowledged')}>Acknowledge</button>}
            <button className="sec" onClick={() => act('ignored')}>Ignore</button>
            <button onClick={() => act('resolved')}>Resolve</button>
          </>
        )}
        {isAdmin && (state === 'resolved' || state === 'ignored') && (
          <button className="sec" onClick={() => act('new')}>Reopen</button>
        )}
      </div>

      {/* Exception header (no card) */}
      <div className="mono" style={{ fontSize: 13.5, fontWeight: 700, color: 'var(--err)', marginBottom: 4, wordBreak: 'break-all' }}>
        {group.type}
      </div>
      <div className="mono" style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16, wordBreak: 'break-word' }}>
        {group.message || '—'}
      </div>

      {/* Meta chips */}
      <div className="meta-row" style={{ marginBottom: 18 }}>
        <span className="chip"><span className="k">service</span><b className="mono">{group.service}</b></span>
        <span className="chip"><span className="k">first seen</span><b className="mono">{tsLong(group.firstSeen)}</b></span>
        <span className="chip"><span className="k">last seen</span><b className="mono">{tsLong(group.lastSeen)}</b></span>
      </div>

      {/* AI Analizi — auto-sends this group's service context (v0.8.89). */}
      <AIAnalysisPanel service={group.service} />

      {/* Occurrences over time — real server-side, gap-filled COUNT over the
          group's whole window (v0.8.309). */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="ov-card-h">
          <h3>Occurrences over time</h3>
          <span className="ov-sub">{group.occurrences.toLocaleString()} total</span>
        </div>
        <div className="ov-card-b">
          {occ.length === 0 ? (
            <div style={{ color: 'var(--text3)', fontSize: 12 }}>
              {occQ.isLoading ? 'Loading…' : 'No occurrences to chart.'}
            </div>
          ) : (
            <TimeChart times={occTimes} series={occSeries} height={110} />
          )}
        </div>
      </div>

      {/* Stack trace (left) · Sample traces (right). minWidth:0 on the columns
          so the long Java stack frames don't force the left column past 1.4fr
          (the v0.8.61 ratio fix — stack trace forced into the left column). */}
      <div style={{ display: 'grid', gridTemplateColumns: '1.4fr 1fr', gap: 16 }}>
        {/* Stack trace */}
        <div className="card" style={{ minWidth: 0 }}>
          <div className="ov-card-h">
            <h3>Stack trace</h3>
            <span className="ov-sub">representative sample</span>
            <span className="ov-right">
              <Button variant="secondary" size="sm" onClick={copyStack} disabled={!stack}>
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </span>
          </div>
          <div className="ov-card-b" style={{ background: 'var(--bg2)', borderRadius: '0 0 8px 8px' }}>
            {stackLines.length === 0 ? (
              <div style={{ color: 'var(--text3)', fontSize: 12 }}>
                {samplesQ.isLoading ? 'Loading…' : 'No stack trace on the sampled occurrences.'}
              </div>
            ) : (
              <pre className="mono" style={{ margin: 0, fontSize: 11.5, lineHeight: 1.7, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
                {stackLines.map((l, i) => (
                  <div key={i} style={{ color: i === 0 ? 'var(--err)' : 'var(--text2)' }}>{l}</div>
                ))}
              </pre>
            )}
          </div>
        </div>

        {/* Sample traces */}
        <div className="card" style={{ minWidth: 0 }}>
          <div className="ov-card-h"><h3>Sample traces</h3>{samples.length > 0 && <span className="ov-sub">{samples.length}</span>}</div>
          <div className="table-wrap">
            <table>
              <tbody>
                {samplesQ.isLoading && <tr><td style={{ padding: 12 }}><Spinner /></td></tr>}
                {!samplesQ.isLoading && samples.length === 0 && (
                  <tr><td style={{ padding: 12, color: 'var(--text3)', fontSize: 12 }}>No sample traces.</td></tr>
                )}
                {samples.slice(0, 14).map((s, i) => (
                  <tr key={i} style={{ cursor: s.traceId ? 'pointer' : 'default' }}
                    onClick={() => s.traceId && navigate(`/trace?id=${encodeURIComponent(s.traceId)}`)}>
                    <td className="mono" style={{ paddingLeft: 14 }}>
                      <span style={{ color: 'var(--accent)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block', maxWidth: 150 }}>
                        {s.traceId ? s.traceId.slice(0, 16) + '…' : '—'}
                      </span>
                    </td>
                    <td><span className="badge b-err">ERROR</span></td>
                    <td className="mono" style={{ textAlign: 'right', paddingRight: 14, fontSize: 11, color: 'var(--text3)' }}>{tsLong(s.time)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  );
}

// ── Firing alert-problem detail (ex-TriageDrawer, now full page) ────────────

export function AlertProblemDetail({ problem, isAdmin, onBack, onChanged }: {
  problem: Problem;
  isAdmin: boolean;
  onBack: () => void;
  onChanged: () => void;
}) {
  useEscBack(onBack);
  const [acking, setAcking] = useState(false);
  const isAnomaly = problem.ruleId?.startsWith('anomaly:');
  const endNs = problem.resolvedAt || Date.now() * 1e6;
  const sevCls = problem.severity === 'critical' ? 'b-err' : problem.severity === 'warning' ? 'b-warn' : 'b-info';

  const ack = async () => {
    setAcking(true);
    try {
      await api.acknowledgeProblems([problem.id]);
      onChanged();
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err));
    } finally {
      setAcking(false);
    }
  };

  const logsFrom = Math.round((problem.startedAt - 60 * 60 * 1e9) / 1e6);
  const logsTo = Math.round((endNs + 10 * 60 * 1e9) / 1e6);
  const logsHref = `/logs?q=${encodeURIComponent(`service.name:"${problem.service.replace(/"/g, '\\"')}"`)}&range=${encodeURIComponent(`custom:${logsFrom}-${logsTo}`)}`;

  return (
    <div id="content">
      <div className="rb-bar">
        <Button variant="secondary" onClick={onBack} leftIcon={<ArrowLeft size={14} strokeWidth={1.75} />}>
          Problems
        </Button>
        <span className={`badge ${sevCls}`}>{problem.severity.toUpperCase()}</span>
        {problem.status === 'open' && <span className="badge b-err">OPEN</span>}
        {problem.status === 'acknowledged' && <span className="badge b-warn">ACK</span>}
        {problem.status === 'resolved' && <span className="badge b-ok">RESOLVED</span>}
        {problem.priority && <span className={`badge ${problem.priority === 'P1' ? 'b-err' : problem.priority === 'P2' ? 'b-warn' : 'b-gray'}`}
          title={problem.priorityReason ? `${problem.priority} — ${problem.priorityReason}` : problem.priority}>{problem.priority}</span>}
        <span className="badge b-gray mono">{problem.id.slice(0, 12)}</span>
        <span className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
          Started {fmtStartedTs(problem.startedAt)} · {fmtDurationNs(endNs - problem.startedAt)}
          {problem.status !== 'resolved' ? ' · ongoing' : ''}
        </span>
        <span className="spacer" />
        <ShareButton copiedLabel="Copied" />
        {isAdmin && problem.status === 'open' && (
          <Button variant="secondary" size="sm" onClick={() => { void ack(); }} disabled={acking}>
            {acking ? 'Acknowledging…' : 'Acknowledge'}
          </Button>
        )}
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1.5fr 1fr', gap: 14, alignItems: 'start' }}>
        {/* ── Left column ── */}
        <div style={{ minWidth: 0 }}>
          <Sect title="Root cause analysis" accent>
            <div style={{ fontSize: 13, marginBottom: 8 }}>
              {isAnomaly && <span className="badge b-info" style={{ marginRight: 6 }}>ANOMALY</span>}
              <b>{problem.ruleName}</b>
            </div>
            <RootCausePanel problemId={problem.id} service={problem.service} />
            {/* Background problemExplainer's persisted first-look blurb —
                full prose here (the feed card only tooltips it). */}
            {problem.aiSummary && (
              <div style={{
                fontSize: 12, color: 'var(--text2)', marginTop: 10,
                padding: '8px 10px', borderRadius: 'var(--radius-sm)',
                background: 'var(--accent-soft)',
                borderLeft: '2px solid var(--accent)',
                whiteSpace: 'pre-wrap',
              }}>
                <IconSparkles size={11} /> {problem.aiSummary}
              </div>
            )}
            {problem.recentDeploy && (
              <DeployBox version={problem.recentDeploy.version} ageSeconds={problem.recentDeploy.ageSeconds} />
            )}
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 10 }}>
              <CopilotExplain kind="problem" id={problem.id}
                label={<><IconSparkles /> <span>Explain</span></>} />
              <CopilotExplain kind="runbook" id={problem.id}
                label={<><IconSparkles /> <span>Runbook AI</span></>} />
            </div>
          </Sect>

          <Sect title="Metric" sub={<span className="mono">{problem.metric}</span>}>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 10 }}>
              <span className="pb-headline" style={{ fontSize: 24 }}>{problem.value.toFixed(2)}</span>
              <span className="mono" style={{ color: 'var(--text3)', fontSize: 13 }}>
                / threshold {fmtFixed(problem.threshold, 2)}
              </span>
            </div>
            {problem.priorityReason && (
              <div style={{ fontSize: 12, color: 'var(--text2)', marginTop: 6 }}>{problem.priorityReason}</div>
            )}
          </Sect>

          <Sect title="Problem timeline">
            <ul className="pb-tl">
              {problem.recentDeploy && (
                <li className="warn">
                  <b>Deploy</b> <code className="mono">{problem.recentDeploy.version}</code>
                  <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8 }}>
                    {fmtStartedTs(problem.startedAt - problem.recentDeploy.ageSeconds * 1e9)}
                  </span>
                </li>
              )}
              <li className="err">
                <b>Detected</b> — {problem.ruleName}
                <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8 }}>{fmtStartedTs(problem.startedAt)}</span>
              </li>
              {problem.status === 'resolved' && problem.resolvedAt ? (
                <li className="ok">
                  <b>Resolved</b>
                  <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8 }}>{fmtStartedTs(problem.resolvedAt)}</span>
                </li>
              ) : null}
            </ul>
          </Sect>
        </div>

        {/* ── Right column ── */}
        <div style={{ minWidth: 0 }}>
          <Sect title="Blast radius">
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              <Link to={`/service?name=${encodeURIComponent(problem.service)}`}
                className={`pb-pill${problem.status === 'open' ? ' err' : ''}`}
                style={{ textDecoration: 'none', color: 'var(--accent2)' }}>
                <span className="dot" /> <span className="mono">{problem.service}</span>
              </Link>
              {(problem.clusters ?? []).map(c => (
                <span key={c} className="pb-pill"><span className="dot" /> <span className="mono">{c}</span></span>
              ))}
            </div>
          </Sect>

          <Sect title="Correlated signals">
            <SignalLink to={logsHref} label="≡ Logs" sub="service, problem window" />
            {/* v0.8.512 (perf raporu #12) — pivot problem penceresini
                taşır: logs ile aynı custom range, yoksa /traces global
                range'iyle açılıp problemle ilgisiz trace gösteriyordu. */}
            <SignalLink to={`/traces?service=${encodeURIComponent(problem.service)}&hasError=true&range=${encodeURIComponent(`custom:${logsFrom}-${logsTo}`)}`}
              label="⋮ Error traces" sub="service, problem window" />
            <SignalLink to={`/service-map?focus=${encodeURIComponent(problem.service)}`}
              label="◉ Service map" sub="focused" />
          </Sect>

          <Sect title="Runbook">
            {problem.runbookUrl && (
              <a href={problem.runbookUrl} target="_blank" rel="noopener"
                style={{
                  display: 'inline-block', marginBottom: 8,
                  fontSize: 12, padding: '4px 12px', borderRadius: 'var(--radius-sm)',
                  background: 'var(--accent-soft)', border: '1px solid var(--accent)',
                  color: 'var(--accent2)', textDecoration: 'none',
                }}>
                Runbook ↗
              </a>
            )}
            {/* Problem→Runbook bridge: run an operational runbook against this
                fire (tagged with problemId) + the runs already attached. */}
            <ProblemRunbookPanel problemId={problem.id} />
          </Sect>

          {problem.description && !isAnomaly && (
            <Sect title="Description">
              <pre className="mono" style={{
                margin: 0, fontSize: 11.5, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere',
                color: 'var(--text2)', background: 'var(--bg2)',
                borderRadius: 'var(--radius-sm)', padding: '8px 10px',
              }}>{problem.description}</pre>
            </Sect>
          )}
        </div>
      </div>
    </div>
  );
}
