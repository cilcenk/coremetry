import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { useAuth } from '@/components/AuthProvider';

// TopologyMuteChip — one-click toggle on the service detail page
// that hides this service from the global + flow topology
// diagrams (v0.5.176). Useful for hub-like infra services
// (kafka config server, service-mesh control plane, identity)
// that fan out to every other service and turn the diagram
// into spaghetti.
//
// Admin-only. The toggled list lives in system_settings and is
// read by every topology API call — change applies to everyone
// the next time their page refetches (60s topology cache).
export function TopologyMuteChip({ service }: { service: string }) {
  const { user } = useAuth();
  const canEdit = user?.role === 'admin';
  const [muted, setMuted] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [list, setList] = useState<string[]>([]);

  useEffect(() => {
    api.topologyExclude()
      .then(d => {
        const arr = d?.services ?? [];
        setList(arr);
        setMuted(arr.includes(service));
      })
      .catch(() => { setMuted(false); });
  }, [service]);

  if (muted === null || !canEdit) return null;

  const toggle = async () => {
    setBusy(true);
    try {
      const next = muted
        ? list.filter(s => s !== service)
        : [...list, service];
      const resp = await api.putTopologyExclude(next);
      const arr = resp?.services ?? next;
      setList(arr);
      setMuted(arr.includes(service));
    } catch (e) {
      alert('Failed to update topology mute: ' + (e instanceof Error ? e.message : String(e)));
    } finally {
      setBusy(false);
    }
  };

  return (
    <button type="button" onClick={toggle} disabled={busy}
      className="sec"
      title={muted
        ? 'Currently hidden from topology diagrams. Click to un-mute.'
        : 'Hide this service from topology diagrams (useful for hub-like infra)'}
      style={{
        fontSize: 11, padding: '5px 10px', borderRadius: 6,
        background: muted ? 'rgba(255,158,77,0.15)' : 'var(--bg3)',
        border: muted ? '1px solid rgba(255,158,77,0.4)' : '1px solid var(--border)',
        color: muted ? 'var(--warn)' : 'var(--text2)',
        cursor: busy ? 'wait' : 'pointer',
        fontFamily: 'ui-monospace, monospace',
      }}>
      {muted ? '🔇 Muted on topology' : '🔇 Mute on topology'}
    </button>
  );
}
