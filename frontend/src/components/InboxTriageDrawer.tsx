import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Users } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui';
import { Empty } from '@/components/Spinner';
import { RootCauseRibbon } from '@/components/RootCauseRibbon';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import { keys } from '@/lib/queries/keys';
import { DEFAULT_DURATIONS } from '@/lib/actions';
import { tsLong } from '@/lib/utils';
import {
  inboxActionsForKind,
  exceptionStateActions,
  rootCauseAnchor,
  buildAnomalySilenceBody,
} from '@/lib/inboxDrawer';
import type { InboxItem, ExceptionGroupState } from '@/lib/types';
import { serviceHref, inboxItemWindow } from '@/lib/serviceHref';
import { tracesPivotHref } from '@/lib/pivotHref';
import { logsHref } from '@/lib/logsUrl';
import { SubjectLink } from './SubjectLink';

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
  const prioClass = item
    ? (item.priority === 'P1' ? 'b-err' : item.priority === 'P2' ? 'b-warn' : 'b-gray')
    : 'b-gray';

  // v0.8.498 (sadeleştirme #2) — kabuk ui/Drawer'a taşındı:
  // overlay/Esc/✕ tek evden; başlık ve gövde birebir.
  return (
    <Drawer onClose={onClose} width={560} header={
      <>
        <span className={`badge ${prioClass}`} style={{ fontSize: 10 }}>
          {item ? item.priority : '—'}
        </span>
        <span className="badge b-gray" style={{ fontSize: 10 }}>
          {(item?.source ?? 'ITEM').toUpperCase()}
        </span>
        {item?.service && (
          /* v0.9.860 (UX denetimi K1) — öğenin kendi penceresi (başlangıç →
             son görülme, ±tampon) linke biner. */
          <SubjectLink service={item.service} subjectKind={item.subjectKind} href={serviceHref(item.service, { range: inboxItemWindow(item) })}
            style={{ fontWeight: 700, fontSize: 14 }} />
        )}
        {item?.assignee && (
          <span className="badge b-info" style={{ fontSize: 10 }}>
            {!item.assignee.includes('@') && <Users size={11} strokeWidth={1.75} />}{item.assignee}
          </span>
        )}
      </>
    }>
        <div style={{ paddingTop: 10 }}>
          {item
            ? <DrawerBody item={item} onClose={onClose} onOpenSource={onOpenSource} />
            : (
              <Empty icon="↔" title="Item no longer in this view">
                It may have been resolved, silenced, or filtered out. Close this
                drawer and pick another row.
              </Empty>
            )}
        </div>
    </Drawer>
  );
}

function DrawerBody({ item, onClose, onOpenSource }: {
  item: InboxItem;
  onClose: () => void;
  onOpenSource: (it: InboxItem) => void;
}) {
  const rc = rootCauseAnchor(item);
  // v0.9.1110 (Faz 5) — kuyruktan çıkmadan triyaj: öğenin kendi penceresi
  // (başlık linkiyle AYNI inboxItemWindow) Logs + hatalı-trace pivotlarına
  // biner. Pencere çözülemiyorsa (bozuk satır) linkler hiç basılmaz —
  // epoch-0'a çivilenmiş boş sayfadan iyi (serviceHref sözleşmesi).
  const w = inboxItemWindow(item);

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
          <RootCauseRibbon anchor={rc.anchor} id={rc.id} summary={undefined} defaultOpen
          window={inboxItemWindow(item)} />
        </div>
      )}
      {(item.kind === 'exception' || item.kind === 'httperror') && item.exception && (
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

      {item.service && w && (
        <div style={{ display: 'flex', gap: 8, marginBottom: 14 }}>
          {/* Logs: service= + olay penceresi. v0.9.1348 — el-yapımı
              `custom:` kopyası logsHref üreticisine indi. */}
          <Link className="sec" style={{ textDecoration: 'none' }}
            to={logsHref({ window: w, service: item.service })}
            title="Bu pencerede servisin logları">
            Logs
          </Link>
          {/* v0.10.419 (C2) — hata pivotu; "Logs" tüm seviyeler kalır. */}
          <Link className="sec" style={{ textDecoration: 'none' }}
            to={logsHref({ window: w, service: item.service, severity: 17 })}
            title="Bu pencerede servisin ERROR+ logları">
            Error logs
          </Link>
          <Link className="sec" style={{ textDecoration: 'none' }}
            to={tracesPivotHref({ window: w, service: item.service, hasError: true })}
            title="Bu pencerede servisin hatalı trace'leri">
            Error traces
          </Link>
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

  // v0.9.254 — exception state transitions. The endpoint has existed since
  // the Errors Inbox shipped; the drawer just never called it, which is what
  // made Ignore a one-way door from the unified inbox.
  const stateMut = useMutation({
    mutationFn: (to: string) =>
      api.setExceptionGroupState(item.exception ? item.exception.fingerprint : '', to as ExceptionGroupState),
    onSuccess: (_d, to) => {
      invalidateInbox();
      qc.invalidateQueries({ queryKey: keys.exceptions.all });
      // Un-ignore / reopen put the row BACK into the default view, so the
      // operator should stay and keep working it. Resolve / ignore take it
      // out — same close-on-terminal-action rule ack and mute follow.
      if (to !== 'new') onClose();
    },
  });

  const anyErr = ackMut.isError || assignMut.isError || muteMut.isError || stateMut.isError;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        {isEditor && matrix.setState && item.exception && exceptionStateActions(item.status).map(a => (
          <Button key={`${a.to}:${a.label}`}
            variant={a.label === 'Un-ignore' || a.label === 'Reopen' ? 'primary' : 'secondary'}
            size="sm"
            title={a.label === 'Un-ignore'
              ? 'Bu grubu susturmadan çıkar — varsayılan görünüme geri döner'
              : undefined}
            loading={stateMut.isPending} onClick={() => stateMut.mutate(a.to)}>
            {a.label}
          </Button>
        ))}
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
