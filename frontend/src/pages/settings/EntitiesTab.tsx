import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/Button';
import { Field } from '@/components/ui/Field';
import { Stack, Row } from '@/components/ui';
import { Badge, type Tone } from '@/components/ui/Badge';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { fmtDateTime } from '@/lib/utils';
import type { DataTableColumn } from '@/lib/dataTable';
import { useEntitySettings, useSaveEntitySettings, useEntitySync, useRunEntitySync } from '@/lib/queries';
import type { EntitySettings, EntitySyncRun } from '@/lib/types';

// EntitiesTab — v0.10.131 (K8s entity katmanı adım 7). Bayrak + vidalar
// (system_settings["entity_layer"]) ve senkronizasyon durumu
// (entity_sync_runs, son 24 h). Kapalıyken mevcut sayfalar etkilenmez;
// açıkken Service → Infra "Pods (entity)" tablosu ve /pod şeridi çıkar.
// Sync tablosu useDataTable (storageKey 'entity-sync-runs').

const RUN_COLS: DataTableColumn<EntitySyncRun>[] = [
  { id: 'cluster', label: 'Cluster', width: 160, sortValue: r => r.ClusterID },
  { id: 'status', label: 'Status', width: 90, sortValue: r => r.Status },
  { id: 'started', label: 'Started', width: 170, sortValue: r => r.StartedAt },
  { id: 'ents', label: 'Entities', width: 90, numeric: true, sortValue: r => r.EntitiesWritten },
  { id: 'rels', label: 'Relations', width: 90, numeric: true, sortValue: r => r.RelationsWritten },
  { id: 'closed', label: 'Closed', width: 80, numeric: true, sortValue: r => r.Closed },
  { id: 'thanos', label: 'Thanos ms', width: 90, numeric: true, sortValue: r => r.ThanosMs },
  { id: 'ch', label: 'CH ms', width: 80, numeric: true, sortValue: r => r.CHMs },
  { id: 'info', label: 'Error / unmapped', width: 360, sortValue: r => r.Error },
];

function statusTone(s: EntitySyncRun['Status']): Tone {
  if (s === 'ok') return 'success';
  if (s === 'failed') return 'danger';
  if (s === 'partial') return 'warning';
  return 'neutral';
}

export function EntitiesTab() {
  const settingsQ = useEntitySettings();
  const save = useSaveEntitySettings();
  const [form, setForm] = useState<EntitySettings>({ enabled: false });
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  useEffect(() => { if (settingsQ.data) setForm(settingsQ.data.settings); }, [settingsQ.data]);
  const enabled = !!settingsQ.data?.resolved.enabled;
  const syncQ = useEntitySync(enabled);
  const run = useRunEntitySync();
  const runs = syncQ.data?.runs ?? [];
  const dt = useDataTable<EntitySyncRun>({
    storageKey: 'entity-sync-runs',
    columns: RUN_COLS,
    rows: runs,
    initialSort: { id: 'started', dir: 'desc' },
  });
  const onSave = async () => {
    setMsg(null);
    try {
      const res = await save.mutateAsync(form);
      setMsg({ kind: 'ok', text: `Saved — ${res.resolved.enabled ? 'enabled' : 'disabled'}, sync ${res.resolved.syncInterval}, podGap ${res.resolved.podGap}.` });
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : 'Save failed' });
    }
  };
  if (settingsQ.isPending) return <Spinner />;
  if (settingsQ.error) return <Empty icon="!" title="Entity settings could not be loaded">{String(settingsQ.error)}</Empty>;
  const d = settingsQ.data!.defaults;
  const obs = syncQ.data?.observability;
  return (
    <Stack gap={4}>
      <p className="field-hint">
        Builds the cluster › node › namespace › workload › pod › container graph from Thanos (kube-state-metrics)
        and span resource attributes, with time validity. Root identity is the Remote Cluster record (Settings → Remote clusters:
        Thanos label + span cluster value). Default off — existing pages are unaffected.
      </p>
      <label className="field" style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
        <input type="checkbox" checked={form.enabled} onChange={e => setForm(f => ({ ...f, enabled: e.target.checked }))} />
        <span>Enable entity layer (Thanos sync on the worker leader + span pass)</span>
      </label>
      <Row gap={4} wrap>
        <Field label="Sync interval" hint={`15s–1h · default ${d.syncInterval}`} value={form.syncInterval ?? ''} placeholder={d.syncInterval}
          onChange={e => setForm(f => ({ ...f, syncInterval: e.target.value }))} />
        <Field label="Pod gap (new lifetime after)" hint={`1m–24h · default ${d.podGap} — StatefulSet names reuse`} value={form.podGap ?? ''} placeholder={d.podGap}
          onChange={e => setForm(f => ({ ...f, podGap: e.target.value }))} />
        <Field label="Stale after" hint={`1h–30d · default ${d.staleAfter} — never deleted, only aged`} value={form.staleAfter ?? ''} placeholder={d.staleAfter}
          onChange={e => setForm(f => ({ ...f, staleAfter: e.target.value }))} />
        <Field label="Parallel clusters" hint={`1–16 · default ${d.parallelClusters}`} type="number" value={form.parallelClusters ?? ''} placeholder={String(d.parallelClusters)}
          onChange={e => setForm(f => ({ ...f, parallelClusters: e.target.value ? Number(e.target.value) : undefined }))} />
      </Row>
      <Row gap={4}>
        <Button variant="primary" type="button" disabled={save.isPending} onClick={() => void onSave()}>Save</Button>
        {enabled && (
          <Button variant="secondary" type="button" disabled={run.isPending || syncQ.data?.workerOnThisPod === false}
            title={syncQ.data?.workerOnThisPod === false ? 'This pod is not the worker — sync runs on the worker pod' : 'Run one sync tick now'}
            onClick={() => run.mutate()}>Run sync now</Button>
        )}
        {msg && <span className={msg.kind === 'ok' ? 'field-hint' : 'field-error'}>{msg.text}</span>}
      </Row>
      {enabled && (
        <>
          {obs && (
            <p className="field-hint">
              ticks {obs.Ticks} · clusters ok {obs.ClustersOK} / failed {obs.ClustersFailed} · written {obs.EntitiesWritten} entities, {obs.RelationsWritten} relations · last tick {obs.LastTickMs} ms
            </p>
          )}
          {syncQ.isPending ? <Spinner /> : runs.length === 0 ? (
            <Empty icon="∅" title="No sync runs in the last 24 h">Add a Remote Cluster (Settings → Remote clusters) and run a sync, or wait for the interval.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <DataTableColgroup dt={dt} />
                <DataTableHead dt={dt} />
                <tbody>
                  {dt.sortedRows.map((r, i) => (
                    <tr key={`${r.ClusterID}-${r.StartedAt}-${i}`}>
                      <td className="mono">{r.ClusterID}</td>
                      <td><Badge tone={statusTone(r.Status)}>{r.Status}</Badge></td>
                      <td className="mono">{fmtDateTime(new Date(r.StartedAt))}</td>
                      <td className="num">{r.EntitiesWritten}</td>
                      <td className="num">{r.RelationsWritten}</td>
                      <td className="num">{r.Closed}</td>
                      <td className="num">{r.ThanosMs}</td>
                      <td className="num">{r.CHMs}</td>
                      <td>
                        {r.Error || (r.UnmappedKeys && r.UnmappedKeys.length > 0
                          ? r.UnmappedKeys.map((k, j) => `${k || '(empty)'}→${r.UnmappedCounts?.[j] ?? 0}`).join(', ')
                          : '')}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </Stack>
  );
}
