import { useNavigate } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Button } from '@/components/ui/Button';
import { Spinner, Empty } from '@/components/Spinner';
import { QueryError } from '@/components/QueryError';
import { readState } from '@/lib/readState';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { Runbook } from '@/lib/types';
import { useConfirm } from '@/components/ui/ConfirmDialog';

// v0.9.872 (tutarlılık denetimi BT11) — `is-fit` sınıfı vardı, primitif
// yoktu. Genişlikler beyan edilmediği için isFitTables.test.ts bu tabloyu
// ÖLÇEMİYOR ve UNMEASURED istisnasında bekliyordu.
// Toplam: flex + 90 + 100 + 260 + 165 = 615 + trailing 260 = 875px < 1150.
const RUNBOOK_COLS: DataTableColumn<Runbook>[] = [
  { id: 'title',   label: 'Title',   sortValue: r => r.title || '(untitled)', naturalDir: 'asc', flex: true },
  { id: 'steps',   label: 'Steps',   sortValue: r => r.steps?.length ?? 0,    numeric: true, width: 90 },
  { id: 'enabled', label: 'Enabled', sortValue: r => (r.enabled ? 1 : 0),     width: 100 },
  { id: 'labels',  label: 'Labels',  sortValue: r => (r.labels ?? []).join(', '), naturalDir: 'asc', width: 260 },
  { id: 'updated', label: 'Updated', sortValue: r => r.updatedAt,             width: 165 },
];
import { useAuth } from '@/components/AuthProvider';
import {
  useRunbooks,
  useCreateRunbook, useDeleteRunbook,
  useEnableRunbook, useDisableRunbook,
} from '@/lib/queries';
import { tsLong } from '@/lib/utils';
import { PageShell } from '@/components/ui/PageShell';

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
  const confirm = useConfirm();
  const navigate = useNavigate();
  const { user } = useAuth();
  const canEdit = user?.role === 'admin' || user?.role === 'editor';

  const runbooksQ = useRunbooks();
  // v0.9.865 (tutarlılık denetimi MT1) — `?? []` okuma hatasını BOŞ KATALOĞA
  // çeviriyordu: 3am'de runbook arayan oncall'a "hiç runbook yok" deniyordu.
  // Tri-state sözleşmesi: undefined = yükleniyor, null = okuma başarısız.
  const runbooks = runbooksQ.isLoading ? undefined
    : runbooksQ.isError ? null
    : runbooksQ.data ?? [];

  const dt = useDataTable<Runbook>({
    storageKey: 'runbooks', columns: RUNBOOK_COLS, rows: runbooks ?? [],
    initialSort: { id: 'updated', dir: 'desc' },
  });

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
    if (!await confirm({
      title: 'Runbook silinsin mi?',
      body: <><b>{title}</b> prosedürü ve tanımı kalıcı olarak silinecek.
        Geçmiş çalıştırmalar denetim için SAKLANIR.</>,
      confirmLabel: 'Runbook’u sil',
      danger: true,
    })) return;
    await deleteRb.mutateAsync(id);
  };

  return (
    <>
      <Topbar title="Runbooks" />
      <PageShell>
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Documented, step-by-step operational procedures. Each runbook is an
            ordered list of manual, query, HTTP, JavaScript, or Bash steps.
          </span>
          {canEdit && (
            <Button variant="primary" size="sm" onClick={newRunbook}
                    style={{ marginLeft: 'auto' }} loading={createRb.isPending}>
              + New runbook
            </Button>
          )}
        </div>

        {readState(runbooks) === 'loading' && <Spinner />}

        {readState(runbooks) === 'error' && (
          <QueryError onRetry={() => runbooksQ.refetch()}>
            Runbooks could not be loaded — this is a failed read, not an empty
            catalogue. Your runbooks are still there.
          </QueryError>
        )}

        {readState(runbooks) === 'empty' && (
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
          <div className="table-wrap is-fit">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} trailing={[260]} />
              <DataTableHead dt={dt} trailing={<th></th>} />
              <tbody>
                {dt.sortedRows.map(rb => (
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
                    <td><div className="cell-actions end">
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
                        <Button variant="ghost-danger" size="sm" loading={deleteRb.isPending}
                          onClick={() => void remove(rb.id, rb.title)}
                          title="Remove the runbook entirely">
                          Delete
                        </Button>
                      )}
                    </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </PageShell>
    </>
  );
}
