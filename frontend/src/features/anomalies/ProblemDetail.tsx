import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ArrowLeft, ArrowDownToLine, Link2 } from 'lucide-react';
import { api } from '@/lib/api';
import { fmtFixed } from '@/lib/utils';
import { CopilotExplain } from '@/components/CopilotExplain';
import { RootCausePanel } from '@/components/RootCausePanel';
import { ProblemRunbookPanel } from '@/components/ProblemRunbookPanel';
import { IconSparkles } from '@/components/icons';
import { fmtDurationNs, fmtStartedTs } from './problemTime';
import type { Problem } from '@/lib/types';
import { Button } from '@/components/ui/Button';

// ProblemDetail — Variant B (Dynatrace problem feed) full-page detail
// for a firing ALERT problem (AlertProblemDetail, ex-v0.5.80
// TriageDrawer). A top triage bar (badges + ID + started/duration +
// actions) over a 1.5fr/1fr grid — left column root-cause card →
// metric card → vertical timeline; right column blast radius →
// correlated signals → runbook.
//
// The exception-inbox side moved to the Variant A triage drawer
// (ExceptionTriageDrawer in AnomalyDetailDrawer.tsx) — this file no
// longer renders exception groups.
//
// All colors ride globals.css tokens (.pb-* helpers) so dark / light /
// redhat themes drive them; deploy correlation renders ONLY when the
// row carries recentDeploy — no placeholder, no extra fetch.

// ShareButton — copies the current address-bar URL to the clipboard.
// The URL is already the canonical shareable link (both detail views
// keep ?problem=<id> / ?exc=<fingerprint> in sync via problemLink.ts),
// so this is just a one-click affordance on top of "copy from the
// address bar" for an operator who wants to paste it into Slack.
function ShareButton() {
  const [copied, setCopied] = useState(false);
  const share = () => {
    navigator.clipboard?.writeText(window.location.href)
      .then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); });
  };
  return (
    <Button variant="secondary" size="sm" onClick={share}
      leftIcon={<Link2 size={13} strokeWidth={1.75} />}>
      {copied ? 'Copied' : 'Share'}
    </Button>
  );
}

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
        <ShareButton />
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
            <SignalLink to={`/traces?service=${encodeURIComponent(problem.service)}&hasError=true`}
              label="⋮ Error traces" sub="service-scoped" />
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
