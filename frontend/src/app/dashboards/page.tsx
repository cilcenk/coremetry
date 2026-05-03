'use client';
import { useEffect, useState, FormEvent } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { DashboardSummary } from '@/lib/types';

export default function DashboardsPage() {
  const { user } = useAuth();
  const router = useRouter();
  const [items, setItems] = useState<DashboardSummary[] | null | undefined>(undefined);
  const [showNew, setShowNew] = useState(false);

  const refresh = () => {
    setItems(undefined);
    api.listDashboards().then(d => setItems(d ?? [])).catch(() => setItems(null));
  };
  useEffect(refresh, []);

  const isAdmin = user?.role === 'admin';

  return (
    <>
      <Topbar title="Dashboards" />
      <div id="content">
        {isAdmin && (
          <div className="controls">
            <button onClick={() => setShowNew(true)}>+ New dashboard</button>
            <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
              {items?.length ?? 0} dashboards
            </span>
          </div>
        )}
        {items === undefined && <Spinner />}
        {items !== undefined && (!items || items.length === 0) && (
          <Empty icon="◫" title="No dashboards yet">
            {isAdmin ? 'Create one to combine metrics, traces and logs into a single view.'
                     : 'Ask an admin to create dashboards.'}
          </Empty>
        )}
        {items && items.length > 0 && (
          <div style={{
            display: 'grid', gap: 12,
            gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))',
          }}>
            {items.map(d => (
              <Link key={d.id} href={`/dashboard?id=${d.id}`} style={{
                display: 'block', padding: 16, borderRadius: 8,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                color: 'inherit', textDecoration: 'none',
              }}>
                <div style={{ fontWeight: 600, marginBottom: 4 }}>{d.name}</div>
                {d.description && (
                  <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 8,
                                overflow: 'hidden', textOverflow: 'ellipsis',
                                display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical' }}>
                    {d.description}
                  </div>
                )}
                <div style={{ fontSize: 11, color: 'var(--text3)' }}>
                  Updated {tsLong(d.updatedAt)}
                </div>
              </Link>
            ))}
          </div>
        )}
        {showNew && isAdmin && (
          <NewDashboardModal
            onClose={() => setShowNew(false)}
            onCreated={(id) => { setShowNew(false); router.push(`/dashboard?id=${id}&edit=1`); }}
          />
        )}
      </div>
    </>
  );
}

function NewDashboardModal({ onClose, onCreated }: {
  onClose: () => void; onCreated: (id: string) => void;
}) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const d = await api.createDashboard({ name, description, panels: [] });
      onCreated(d.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 380, padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>New dashboard</div>
        <form onSubmit={submit}>
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Name</div>
            <input required autoFocus value={name}
              onChange={e => setName(e.target.value)} style={{ width: '100%' }} />
          </label>
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Description (optional)</div>
            <input value={description}
              onChange={e => setDescription(e.target.value)} style={{ width: '100%' }} />
          </label>
          {error && (
            <div style={{ color: 'var(--err)', fontSize: 12, marginBottom: 8 }}>{error}</div>
          )}
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button type="button" className="sec" onClick={onClose}>Cancel</button>
            <button type="submit" disabled={busy}>{busy ? 'Creating…' : 'Create'}</button>
          </div>
        </form>
      </div>
    </div>
  );
}
