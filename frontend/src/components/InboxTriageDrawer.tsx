import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Users } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Empty } from '@/components/Spinner';
import { RootCauseRibbon } from '@/components/RootCauseRibbon';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import { keys } from '@/lib/queries/keys';
import { DEFAULT_DURATIONS } from '@/lib/actions';
import { tsLong } from '@/lib/utils';
import {
  inboxActionsForKind,
  rootCauseAnchor,
  buildAnomalySilenceBody,
} from '@/lib/inboxDrawer';
import type { InboxItem } from '@/lib/types';

// InboxTriageDrawer — v0.8.292 (Option B slice 3): the /inbox row-click opens
// this right-side drawer so the operator triages WITHOUT leaving the inbox,
// instead of navigating to the source page. Shell mirrors AnomalyDetailDrawer
// (overlay + slide-in panel, Esc closes, one drawer language). Body:
//   • Root-cause ribbon (reused RootCauseRibbon) for problem + anomaly kinds —
//     fetch-on-expand only, never prefetched across rows (ES-cost discipline).
//   • Exception detail (message / occurrences) for exception kind (no rc endpoint).
//   • Inline actions on the EXISTING endpoints — problem → Acknowledge
//     (api.acknowledgeProblems) + Assign… (api.setProblemAssignee, which also
//     emails since v0.8.289); anomaly → Mute… (api.createAnomalySilence);
//     all kinds → Open source → (the goToSource deep-link escape hatch).
//   • On a successful mutation the parent's inbox list + count queries are
//     invalidated (queryKey ['inbox']) so the row/badge update.
// Mutating actions are hidden from viewers (backend still gates); the ribbon +
// Open source stay visible read-only. The drawer NEVER polls.
//
// item === undefined ⇒ ?item=<id> pointed at a row not in the current list
// (stale deep-link / filtered out) — a soft fallback, not a blank panel.
export function InboxTriageDrawer({ item, onClose, onOpenSource }: {
  item: InboxItem | undefined;
  onClose: () => void;
  onOpenSource: (it: InboxItem) => void;
}) {
  // Esc closes — same triage muscle memory as the anomaly / problem drawers.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const prioClass = item
    ? (item.priority === 'P1' ? 'b-err' : item.priority === 'P2' ? 'b-warn' : 'b-gray')
    : 'b-gray';

  return (
    <>
      <div onClick={onClose}
        style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
          zIndex: 30, animation: 'fadeIn 120ms ease-out',
        }} />
      <div style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: 'min(560px, 100vw)',
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        boxShadow: '-4px 0 24px rgba(0,0,0,0.3)',
        zIndex: 31, overflowY: 'auto',
        animation: 'slideInRight 180ms ease-out',
      }}>
        {/* Header — mirrors the row: priority + title + service + assignee. */}
        <div style={{
          padding: '14px 18px', borderBottom: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          <span className={`badge ${prioClass}`} style={{ fontSize: 10 }}>
            {item ? item.priority : '—'}
          </span>
          <span className="badge b-gray" style={{ fontSize: 10 }}>
            {(item?.source ?? 'ITEM').toUpperCase()}
          </span>
          {item?.service && (
            <Link to={`/service?name=${encodeURIComponent(item.service)}`}
              style={{ fontWeight: 700, fontSize: 14 }}>
              {item.service}
            </Link>
          )}
          {item?.assignee && (
            <span className="badge b-info" style={{ fontSize: 10 }}>
              {!item.assignee.includes('@') && <Users size={11} strokeWidth={1.75} />}{item.assignee}
            </span>
          )}
          <span style={{ flex: 1 }} />
          <button type="button" onClick={onClose} className="sec"
            title="Close (Esc)" style={{ fontSize: 12, padding: '3px 9px' }}>✕</button>
        </div>

        <div style={{ padding: '14px 18px' }}>
          {item
            ? <DrawerBody item={item} onClose={onClose} onOpenSource={onOpenSource} />
            : (
              <Empty icon="↔" title="Item no longer in this view">
                It may have been resolved, silenced, or filtered out. Close this
                drawer and pick another row.
              </Empty>
            )}
        </div>
      </div>
    </>
  );
}

function DrawerBody({ item, onClose, onOpenSource }: {
  item: InboxItem;
  onClose: () => void;
  onOpenSource: (it: InboxItem) => void;
}) {
  const rc = rootCauseAnchor(item);

  return (
    <>
      <div style={{
        fontWeight: 700, fontSize: 14, marginBottom: 4, overflowWrap: 'anywhere',
      }} title={item.title}>{item.title}</div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 12 }}>
        {item.priorityReason && <span>{item.priorityReason} · </span>}
        last seen {tsLong(item.lastSeen)}
      </div>

      {/* Root cause — the differentiator. Fetch-on-expand inside the ribbon;
          nothing is fetched until the operator clicks ▸. Exceptions have no
          fan-out endpoint, so they show the exception detail instead. */}
      {rc && (
        <div style={{ marginBottom: 14 }}>
          <RootCauseRibbon anchor={rc.anchor} id={rc.id} summary={undefined} />
        </div>
      )}
      {item.kind === 'exception' && item.exception && (
        <div style={{
          marginBottom: 14, padding: '10px 12px', borderRadius: 6,
          background: 'var(--bg1)', border: '1px solid var(--border)',
        }}>
          <div style={{ fontSize: 12, marginBottom: 6 }}>
            <span className="badge b-err mono" style={{ fontSize: 10 }}>{item.exception.type}</span>
            <span style={{ marginLeft: 8, color: 'var(--text3)' }}>
              <b className="mono" style={{ color: 'var(--text)' }}>
                {item.exception.occurrences.toLocaleString()}
              </b> occurrences
            </span>
          </div>
          {item.exception.message && (
            <pre style={{
              fontSize: 11, fontFamily: 'ui-monospace, SFMono-Regular, monospace',
              whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', margin: 0,
              color: 'var(--text2)', maxHeight: 160, overflowY: 'auto',
            }} title={item.exception.message}>{item.exception.message}</pre>
          )}
        </div>
      )}

      <InboxActions item={item} onClose={onClose} onOpenSource={onOpenSource} />
    </>
  );
}

// InboxActions — the inline triage row. All calls hit EXISTING endpoints; no new
// mutation surface. Success invalidates ['inbox'] (list + count) plus the source
// feed so both the inbox and the source page reflect the change. Ack + Mute
// resolve the item out of the open view, so they close the drawer; Assign leaves
// it in the list, so the drawer stays open and re-renders the new assignee.
function InboxActions({ item, onClose, onOpenSource }: {
  item: InboxItem;
  onClose: () => void;
  onOpenSource: (it: InboxItem) => void;
}) {
  const { user } = useAuth();
  const isEditor = user?.role === 'admin' || user?.role === 'editor';
  const qc = useQueryClient();
  const matrix = inboxActionsForKind(item.kind);
  const [durationSec, setDurationSec] = useState<number>(60 * 60);

  const invalidateInbox = () => qc.invalidateQueries({ queryKey: ['inbox'] });

  const ackMut = useMutation({
    mutationFn: () => api.acknowledgeProblems(item.problem ? [item.problem.id] : []),
    onSuccess: () => { invalidateInbox(); qc.invalidateQueries({ queryKey: keys.problems.all }); onClose(); },
  });
  const assignMut = useMutation({
    mutationFn: (assignee: string) =>
      api.setProblemAssignee(item.problem ? item.problem.id : '', assignee),
    onSuccess: () => { invalidateInbox(); qc.invalidateQueries({ queryKey: keys.problems.all }); },
  });
  const muteMut = useMutation({
    mutationFn: () => {
      const body = buildAnomalySilenceBody(item, durationSec);
      if (!body) throw new Error('not an anomaly');
      return api.createAnomalySilence(body);
    },
    onSuccess: () => { invalidateInbox(); qc.invalidateQueries({ queryKey: keys.anomalies.all }); onClose(); },
  });

  const onAssign = () => {
    // Mirror the Problems page AssigneeCell: dependency-light prompt(), empty
    // clears the assignee. Cancel (null) is a no-op.
    const v = window.prompt('Assignee (email or team name; empty = unassign):', item.assignee ?? '');
    if (v === null) return;
    assignMut.mutate(v.trim());
  };

  const anyErr = ackMut.isError || assignMut.isError || muteMut.isError;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        {isEditor && matrix.acknowledge && (
          <Button variant="primary" size="sm"
            loading={ackMut.isPending} onClick={() => ackMut.mutate()}>
            Acknowledge
          </Button>
        )}
        {isEditor && matrix.assign && (
          <Button variant="secondary" size="sm"
            loading={assignMut.isPending} onClick={onAssign}>
            Assign…
          </Button>
        )}
        {isEditor && matrix.mute && (
          <>
            <select value={durationSec}
              onChange={e => setDurationSec(Number(e.target.value))}
              title="Silence duration"
              style={{ fontSize: 12, padding: '4px 8px' }}>
              {DEFAULT_DURATIONS.map(d => (
                <option key={d.seconds} value={d.seconds}>{d.label}</option>
              ))}
            </select>
            <Button variant="secondary" size="sm"
              loading={muteMut.isPending} onClick={() => muteMut.mutate()}>
              Mute…
            </Button>
          </>
        )}
        {/* Escape hatch — the EXISTING goToSource deep-link. Always available,
            including for viewers and the exception kind. */}
        <Button variant="ghost" size="sm" onClick={() => onOpenSource(item)}>
          Open source →
        </Button>
      </div>
      {anyErr && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>
          Action failed — check the server log and retry.
        </div>
      )}
    </div>
  );
}
