import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api } from '@/lib/api';
import { Modal } from '@/components/ui';
import { Button } from '@/components/ui/Button';
import { Field, SelectField } from '@/components/ui/Field';
import { Spinner } from '@/components/Spinner';
import type { Panel } from '@/lib/types';

// PinToDashboardModal (v0.8.419, DE4) — target picker for an Explore
// query pinned as a live dashboard panel. Fetch-on-open only (the
// dashboard list loads when the modal mounts, never speculatively
// from the toolbar). Appending to an existing dashboard re-reads the
// full doc first so concurrent panels/variables are carried forward —
// the PUT is whole-doc (ReplacingMergeTree row replace, invariant #4).
export function PinToDashboardModal({ panel, onClose }: {
  panel: Panel;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const listQ = useQuery({
    queryKey: ['dashboards'],
    queryFn: () => api.listDashboards(),
    staleTime: 30_000,
  });
  const dashboards = useMemo(() => listQ.data ?? [], [listQ.data]);

  const [target, setTarget] = useState<string>('new');
  const [newName, setNewName] = useState('');
  const [title, setTitle] = useState(panel.title);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [done, setDone] = useState<{ id: string; name: string } | null>(null);

  const pin = async () => {
    setBusy(true);
    setErr('');
    const p: Panel = { ...panel, title: title.trim() || panel.title };
    try {
      if (target === 'new') {
        const name = newName.trim() || 'Explore pins';
        const created = await api.createDashboard({
          name, description: 'Pinned from Explore', panels: [p],
        });
        setDone({ id: created.id, name });
      } else {
        const doc = await api.getDashboard(target);
        const updated = await api.updateDashboard(target, {
          name: doc.name, description: doc.description,
          panels: [...(doc.panels ?? []), p],
          variables: doc.variables,
        });
        setDone({ id: updated.id, name: doc.name });
      }
      void qc.invalidateQueries({ queryKey: ['dashboards'] });
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Pin failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open onClose={onClose} title="Pin to dashboard" size="md"
      footer={done ? (
        <Button onClick={onClose}>Kapat</Button>
      ) : (
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>Vazgeç</Button>
          <Button onClick={() => { void pin(); }} loading={busy}
            disabled={busy || (target === 'new' && !newName.trim())}>
            Pinle
          </Button>
        </>
      )}>
      {done ? (
        <div style={{ fontSize: 13, display: 'flex', flexDirection: 'column', gap: 8 }}>
          <span>Panel eklendi: <b>{done.name}</b></span>
          <Link to={`/dashboards/${done.id}`} style={{ color: 'var(--accent2)' }}>
            Dashboard&apos;u aç →
          </Link>
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <Field label="Panel başlığı" value={title}
            onChange={e => setTitle(e.target.value)} />
          {listQ.isLoading ? <Spinner /> : (
            <SelectField label="Hedef dashboard" value={target}
              onChange={e => setTarget(e.target.value)}>
              <option value="new">+ Yeni dashboard…</option>
              {dashboards.map(d => (
                <option key={d.id} value={d.id}>{d.name}</option>
              ))}
            </SelectField>
          )}
          {target === 'new' && (
            <Field label="Yeni dashboard adı" value={newName}
              onChange={e => setNewName(e.target.value)}
              placeholder="Explore pins" />
          )}
          {err && <div style={{ fontSize: 12, color: 'var(--err)' }}>{err}</div>}
        </div>
      )}
    </Modal>
  );
}
