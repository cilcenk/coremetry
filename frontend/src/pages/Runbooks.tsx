import { useNavigate } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Button } from '@/components/ui/Button';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import {
  useRunbooks,
  useCreateRunbook, useDeleteRunbook,
  useEnableRunbook, useDisableRunbook,
} from '@/lib/queries';
import { tsLong } from '@/lib/utils';

// Runbooks list (v0.7.0) — operator-authored executable procedures
// (OneUptime model). This page is the catalogue: title, step count,
// enabled state, labels, last-updated. Authoring (the steps editor,
// kind cards, drag-reorder) lives on the per-runbook detail page
// (/runbook?id=). Executions + the runner land in a later increment.
//
// Permission gating mirrors Alerts: editor+ can create / enable /
// disable / delete; viewers see the table read-only (no action
// column buttons) so they still SEES state per CLAUDE.md invariant 7.

export default function RunbooksPage() {
  const navigate = useNavigate();
  const { user } = useAuth();
  const canEdit = user?.role === 'admin' || user?.role === 'editor';

  const runbooksQ = useRunbooks();
  const runbooks = runbooksQ.isLoading ? undefined : runbooksQ.data ?? [];

  const createRb  = useCreateRunbook();
  const deleteRb  = useDeleteRunbook();
  const enableRb  = useEnableRunbook();
  const disableRb = useDisableRunbook();

  const newRunbook = async () => {
    try {
      const created = await createRb.mutateAsync({
        title: 'Untitled runbook', steps: [], enabled: true,
      });
      if (created?.id) navigate(`/runbook?id=${encodeURIComponent(created.id)}`);
    } catch (e) {
      alert(`Could not create runbook: ${e instanceof Error ? e.message : String(e)}`);
    }
  };

  const remove = async (id: string, title: string) => {
    if (!confirm(`Delete runbook "${title}" permanently? This removes the procedure and its definition. Historical executions are kept for audit.`)) return;
    await deleteRb.mutateAsync(id);
  };

  return (
    <>
      <Topbar title="Runbooks" />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Documented, step-by-step operational procedures. Each runbook is an
            ordered list of manual, query, HTTP, JavaScript, or Bash steps.
          </span>
          {canEdit && (
            <Button variant="primary" size="sm" onClick={newRunbook} disabled={createRb.isPending}
                    style={{ marginLeft: 'auto' }}>
              {createRb.isPending ? 'Creating…' : '+ New runbook'}
            </Button>
          )}
        </div>

        {runbooks === undefined && <Spinner />}

        {runbooks && runbooks.length === 0 && (
          <Empty icon="▤" title="No runbooks">
            <div style={{ marginTop: 6, color: 'var(--text2)' }}>
              Runbooks turn tribal knowledge into repeatable, executable
              procedures — the playbook your oncall reaches for at 3am.
              {canEdit
                ? <> Click <b>+ New runbook</b> to author your first one.</>
                : <> Ask an editor or admin to author one.</>}
            </div>
          </Empty>
        )}

        {runbooks && runbooks.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Title</th>
                  <th className="num">Steps</th>
                  <th>Enabled</th>
                  <th>Labels</th>
                  <th>Updated</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {runbooks.map(rb => (
                  <tr key={rb.id}>
                    <td>
                      <a href={`/runbook?id=${encodeURIComponent(rb.id)}`}
                        onClick={e => { e.preventDefault(); navigate(`/runbook?id=${encodeURIComponent(rb.id)}`); }}
                        style={{ color: 'var(--accent2)', textDecoration: 'none', fontWeight: 600 }}>
                        {rb.title || '(untitled)'}
                      </a>
                    </td>
                    <td className="num mono">{rb.steps?.length ?? 0}</td>
                    <td>{rb.enabled
                      ? <span className="badge b-ok">ON</span>
                      : <span className="badge b-gray">OFF</span>}</td>
                    <td>
                      {(rb.labels ?? []).length === 0
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : (
                          <span style={{ display: 'inline-flex', gap: 4, flexWrap: 'wrap' }}>
                            {(rb.labels ?? []).map(l => (
                              <span key={l} className="badge b-gray" style={{ fontSize: 10 }}>{l}</span>
                            ))}
                          </span>
                        )}
                    </td>
                    <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                      {tsLong(rb.updatedAt)}
                    </td>
                    <td style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
                      <Button variant="secondary" size="sm"
                        onClick={() => navigate(`/runbook?id=${encodeURIComponent(rb.id)}`)}>
                        Open
                      </Button>
                      {canEdit && (rb.enabled
                        ? <Button variant="secondary" size="sm" onClick={() => disableRb.mutateAsync(rb.id)}
                            title="Stop this runbook from being executable without deleting it">
                            Disable
                          </Button>
                        : <Button variant="secondary" size="sm" onClick={() => enableRb.mutateAsync(rb.id)}>
                            Enable
                          </Button>)}
                      {canEdit && (
                        <Button variant="danger" size="sm" onClick={() => remove(rb.id, rb.title)}
                          title="Remove the runbook entirely">
                          Delete
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
