import { Suspense, useEffect, useState } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { Link } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, History, Paperclip, PenLine } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { CopilotExplain } from '@/components/CopilotExplain';
import {
  useIncident, useIncidentEvents, useIncidentProblems, keys,
} from '@/lib/queries';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { Incident } from '@/lib/types';

export default function IncidentPage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp] = useSearchParams();
  const navigate = useNavigate();
  const { user } = useAuth();
  const id = sp.get('id') ?? '';
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  const qc = useQueryClient();

  // Three parallel queries — incident detail, timeline, attached
  // problems. Each its own cache key so a timeline-only refresh
  // (after an addNote) doesn't refetch the postmortem text.
  const incQ = useIncident(id);
  const timelineQ = useIncidentEvents(id);
  const problemsQ = useIncidentProblems(id);
  const inc = incQ.isLoading ? undefined : incQ.isError ? null : incQ.data;
  const timeline = timelineQ.data ?? [];
  const problems = problemsQ.data ?? [];

  const [note, setNote] = useState('');
  const [postmortemDraft, setPostmortemDraft] = useState('');
  const [editingPM, setEditingPM] = useState(false);

  // Sync the postmortem draft with the loaded incident — only
  // when not actively editing, so a save-in-progress doesn't
  // get clobbered by a refetch.
  useEffect(() => {
    if (inc && !editingPM) setPostmortemDraft(inc.postmortem ?? '');
  }, [inc, editingPM]);

  const refresh = () => {
    qc.invalidateQueries({ queryKey: keys.incidents.one(id) });
    qc.invalidateQueries({ queryKey: keys.incidents.events(id) });
    qc.invalidateQueries({ queryKey: keys.incidents.problems(id) });
  };

  if (!id)             return <Empty icon="⚠" title="No incident selected" />;
  if (inc === undefined) return <Spinner />;
  if (inc === null)    return <Empty icon="⚠" title="Incident not found" />;

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

  const elapsed = (inc.resolvedAt ?? Date.now() * 1_000_000) - inc.startedAt;

  return (
    <>
      <Topbar title={`Incident · ${inc.title}`} />
      <div id="content">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14, flexWrap: 'wrap' }}>
          <Link to="/incidents" className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)',
            borderRadius: 6, fontSize: 12, color: 'var(--text)', textDecoration: 'none',
            display: 'inline-flex', alignItems: 'center', gap: 6,
          }}><ArrowLeft size={14} strokeWidth={1.75} /> All incidents</Link>
          <StatusPill s={inc.status} />
          <SeverityPill s={inc.severity} />
          {inc.service && (
            <Link to={`/service?name=${encodeURIComponent(inc.service)}`} style={{ fontSize: 12 }}>
              Service: {inc.service}
            </Link>
          )}
          <span style={{ color: 'var(--text3)', fontSize: 12 }}>
            Started {tsLong(inc.startedAt)} · {fmtDuration(elapsed)} {inc.resolvedAt ? '(resolved)' : '(ongoing)'}
          </span>
          {inc.assignee && (
            <span style={{ color: 'var(--text3)', fontSize: 12 }}>· {inc.assignee}</span>
          )}
          <span style={{ marginLeft: 'auto' }} />
          {/* Davis-style triage summary on demand — feeds the
              incident title + service + attached problems to
              the LLM, returns "what's happening / blast
              radius / first three actions / escalate?" in
              one bullet block. Self-hides when the copilot
              isn't configured. */}
          <CopilotExplain kind="incident" id={inc.id} />
          {isAdmin && inc.status === 'open' && (
            <button onClick={ack}>Acknowledge</button>
          )}
          {isAdmin && inc.status !== 'resolved' && (
            <button onClick={resolve}>Resolve</button>
          )}
        </div>

        {inc.summary && (
          <div style={{
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 6, padding: 12, marginBottom: 14, fontSize: 13,
            color: 'var(--text)',
          }}>{inc.summary}</div>
        )}

        <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: 16 }}>
          {/* Timeline */}
          <div>
            <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 8, display: 'flex', alignItems: 'center', gap: 6 }}>
              <History size={15} strokeWidth={1.75} /> Timeline
            </h3>
            {timeline.length === 0 && (
              <div className="empty" style={{ padding: 20, fontSize: 12 }}>Empty timeline</div>
            )}
            {timeline.length > 0 && (
              <div style={{ borderLeft: '2px solid var(--border)', paddingLeft: 14 }}>
                {timeline.map((e, i) => (
                  <div key={i} style={{ position: 'relative', marginBottom: 14 }}>
                    <span style={{
                      position: 'absolute', left: -20, top: 4, width: 8, height: 8,
                      borderRadius: '50%', background: kindColor(e.kind),
                    }} />
                    <div style={{ fontSize: 11, color: 'var(--text3)' }}>
                      <b style={{ color: 'var(--text2)' }}>{kindLabel(e.kind)}</b>
                      {e.actor && <> · {e.actor}</>}
                      <> · {tsLong(e.time)}</>
                    </div>
                    {e.body && (
                      <div style={{ fontSize: 13, color: 'var(--text)', marginTop: 2, whiteSpace: 'pre-wrap' }}>
                        {e.body}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
            {isAdmin && (
              <div style={{ marginTop: 12, display: 'flex', gap: 8 }}>
                <input value={note} onChange={e => setNote(e.target.value)}
                  onKeyDown={e => e.key === 'Enter' && submitNote()}
                  placeholder="Add a note (e.g. mitigation tried, hypothesis, who's on it)…"
                  style={{ flex: 1 }} />
                <button onClick={submitNote} disabled={!note.trim()}>Add note</button>
              </div>
            )}
          </div>

          {/* Right column: attached problems + postmortem */}
          <div>
            <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 8, display: 'flex', alignItems: 'center', gap: 6 }}>
              <Paperclip size={15} strokeWidth={1.75} /> Attached problems
            </h3>
            {problems.length === 0 && (
              <div className="empty" style={{ padding: 16, fontSize: 12 }}>No problems attached</div>
            )}
            {problems.map(pid => (
              <div key={pid} style={{
                fontSize: 11, fontFamily: 'monospace', padding: '4px 8px',
                background: 'var(--bg3)', borderRadius: 4, marginBottom: 4,
              }}>{pid}</div>
            ))}

            <h3 style={{ fontSize: 13, fontWeight: 700, margin: '20px 0 8px', display: 'flex', alignItems: 'center', gap: 6 }}>
              <PenLine size={15} strokeWidth={1.75} /> Postmortem
              {isAdmin && !editingPM && (
                <button className="sec" onClick={() => setEditingPM(true)}
                  style={{ marginLeft: 8, padding: '2px 8px', fontSize: 11 }}>
                  {inc.postmortem ? 'Edit' : 'Write'}
                </button>
              )}
            </h3>
            {editingPM ? (
              <div>
                <textarea value={postmortemDraft} onChange={e => setPostmortemDraft(e.target.value)}
                  rows={12} style={{ width: '100%', resize: 'vertical', fontFamily: 'monospace', fontSize: 12 }}
                  placeholder={POSTMORTEM_TEMPLATE} />
                <div style={{ display: 'flex', gap: 6, marginTop: 6, justifyContent: 'flex-end' }}>
                  <button className="sec" onClick={() => { setEditingPM(false); setPostmortemDraft(inc.postmortem ?? ''); }}>Cancel</button>
                  <button onClick={savePM}>Save</button>
                </div>
              </div>
            ) : inc.postmortem ? (
              <pre style={{ fontSize: 12, whiteSpace: 'pre-wrap', background: 'var(--bg2)', padding: 10, borderRadius: 4 }}>
                {inc.postmortem}
              </pre>
            ) : (
              <div className="empty" style={{ padding: 16, fontSize: 12 }}>
                {isAdmin ? 'Once resolved, write a blameless postmortem here.' : 'No postmortem yet.'}
              </div>
            )}
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

## Timeline
- HH:MM — first signal observed
- HH:MM — paged
- HH:MM — mitigation deployed
- HH:MM — confirmed resolved

## Action items
- [ ] Owner — concrete change to prevent recurrence
- [ ] Owner — monitor / alert / runbook gap
`;

function StatusPill({ s }: { s: Incident['status'] }) {
  const cls = s === 'open' ? 'outage' : s === 'acknowledged' ? 'degraded' : 'operational';
  const label = s === 'open' ? 'OPEN' : s === 'acknowledged' ? 'ACK' : 'RESOLVED';
  return <span className={`status-pill status-pill-${cls}`}>{label}</span>;
}
function SeverityPill({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}
function fmtDuration(ns: number): string {
  const sec = Math.floor(ns / 1e9);
  if (sec < 60)   return sec + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm';
  if (sec < 86400) return (sec / 3600).toFixed(1) + 'h';
  return Math.floor(sec / 86400) + 'd';
}
function kindLabel(k: string): string {
  switch (k) {
    case 'created':           return 'Created';
    case 'ack':               return 'Acknowledged';
    case 'resolved':          return 'Resolved';
    case 'note':              return 'Note';
    case 'problem_attached':  return 'Problem attached';
    case 'problem_resolved':  return 'Problem resolved';
    default:                  return k;
  }
}
function kindColor(k: string): string {
  switch (k) {
    case 'created':           return 'var(--accent2)';
    case 'ack':               return 'var(--warn)';
    case 'resolved':          return 'var(--ok)';
    case 'note':              return 'var(--accent)';
    case 'problem_attached':  return 'var(--err)';
    case 'problem_resolved':  return 'var(--ok)';
    default:                  return 'var(--text3)';
  }
}
