import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import { AIAnalysisPanel } from '@/components/AIAnalysisPanel';
import { TimeChart } from '@/components/charts/TimeChart';
import { statusColor } from '@/lib/statusColor';
import type { ExceptionGroup, ExceptionGroupState } from '@/lib/types';
import { Button } from '@/components/ui/Button';

// ProblemDetail — full in-page exception-group detail (prototype design-parity,
// page 5). Opened from a Problems row; back returns to the list. Layout matches
// the prototype's ProblemDetail: detail bar (state + occurrences + actions) →
// red exception header → meta chips → occurrences-over-time histogram → a
// 1.4fr/1fr grid (Stack trace | Sample traces). Real data: the group record +
// exceptionGroupSamples(). There is no occurrences timeseries API, so the
// histogram is derived from the sample timestamps (real, sampled) and labelled
// as such.

const STATE_LABEL: Record<ExceptionGroupState, string> = {
  // 'new' renders OPEN (v0.8.382): NEW is reserved for the yellow
  // first-seen-recently badge on the list — same rule as StateBadge.
  new: 'OPEN', regressed: 'REGRESSED', acknowledged: 'ACK', resolved: 'RESOLVED', ignored: 'IGNORED',
};
const STATE_BADGE: Record<ExceptionGroupState, string> = {
  new: 'b-err', regressed: 'b-err', acknowledged: 'b-warn', resolved: 'b-ok', ignored: 'b-gray',
};

export function ProblemDetail({ group, isAdmin, onBack, onChanged }: {
  group: ExceptionGroup;
  isAdmin: boolean;
  onBack: () => void;
  onChanged: () => void;
}) {
  const navigate = useNavigate();
  const [state, setState] = useState<ExceptionGroupState>(group.state);
  const [copied, setCopied] = useState(false);

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
          group's whole window (v0.8.309). Replaced the old client-side
          bucketing of the 100 newest samples, which clustered near last_seen
          and rendered any busy group as a single right-edge spike, plus a
          fabricated deploy marker that fired on every chart. */}
      <div className="card ov-mb">
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
            <TimeChart
              times={occ.map(p => p.time / 1e9)}
              series={[{ key: 'occ', label: 'occurrences', data: occ.map(p => p.count), color: statusColor('warn'), type: 'bar' }]}
              height={110}
            />
          )}
        </div>
      </div>

      {/* Stack trace (left) · Sample traces (right). minWidth:0 on the columns
          so the long Java stack frames don't force the left column past 1.4fr. */}
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
