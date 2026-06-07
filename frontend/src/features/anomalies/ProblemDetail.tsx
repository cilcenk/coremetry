import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { ExceptionGroup, ExceptionGroupState } from '@/lib/types';

// ProblemDetail — full in-page exception-group detail (prototype design-parity,
// page 5). Opened from a Problems row; back returns to the list. Layout matches
// the prototype's ProblemDetail: detail bar (state + occurrences + actions) →
// red exception header → meta chips → occurrences-over-time histogram → a
// 1.4fr/1fr grid (Stack trace | Sample traces). Real data: the group record +
// exceptionGroupSamples(). There is no occurrences timeseries API, so the
// histogram is derived from the sample timestamps (real, sampled) and labelled
// as such.

const STATE_LABEL: Record<ExceptionGroupState, string> = {
  new: 'NEW', regressed: 'REGRESSED', acknowledged: 'ACK', resolved: 'RESOLVED', ignored: 'IGNORED',
};
const STATE_BADGE: Record<ExceptionGroupState, string> = {
  new: 'b-err', regressed: 'b-err', acknowledged: 'b-warn', resolved: 'b-ok', ignored: 'b-gray',
};

// bucketTimes — counts of sample timestamps (unix ns) across `n` equal buckets
// spanning [from, to]. Returns [] when the window is degenerate or empty.
function bucketTimes(timesNs: number[], fromNs: number, toNs: number, n: number): number[] {
  if (!timesNs.length || !(toNs > fromNs)) return [];
  const span = toNs - fromNs;
  const out = new Array(n).fill(0);
  for (const t of timesNs) {
    let b = Math.floor(((t - fromNs) / span) * n);
    if (b < 0) b = 0; if (b >= n) b = n - 1;
    out[b]++;
  }
  return out;
}

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

  const buckets = useMemo(
    () => bucketTimes(samples.map(s => s.time), group.firstSeen, group.lastSeen, 24),
    [samples, group.firstSeen, group.lastSeen],
  );
  const maxB = Math.max(1, ...buckets);

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
        <button className="sec" onClick={onBack} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <ArrowLeft size={14} strokeWidth={1.75} /> Problems
        </button>
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

      {/* Occurrences over time */}
      <div className="card ov-mb">
        <div className="ov-card-h">
          <h3>Occurrences over time</h3>
          <span className="ov-sub">from {samples.length} recent sample{samples.length === 1 ? '' : 's'}</span>
        </div>
        <div className="ov-card-b">
          {buckets.length === 0 ? (
            <div style={{ color: 'var(--text3)', fontSize: 12 }}>
              {samplesQ.isLoading ? 'Loading…' : 'Not enough sampled occurrences to chart.'}
            </div>
          ) : (
            <div className="volbars" style={{ height: 70 }}>
              {buckets.map((v, i) => (
                <div className="col" key={i}>
                  <i style={{ height: Math.max(2, (v / maxB) * 70), background: v >= maxB * 0.66 ? 'var(--err)' : 'var(--warn)' }} />
                </div>
              ))}
            </div>
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
              <button className="sec" onClick={copyStack} disabled={!stack} style={{ padding: '3px 9px', fontSize: 11 }}>
                {copied ? 'Copied' : 'Copy'}
              </button>
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
