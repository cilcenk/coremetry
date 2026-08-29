// SnoozeButton — v0.10.162: streams.tsx'ten çıkarıldı (aynı gövde), Service
// Overview'daki «Anomaliler · bu pencere» tablosu da kullansın diye
// («Değil → sessize al» = MEVCUT susturma; karar deposu yok, brief §6).
// Etiket değiştirilebilir; süre menüsü ve davranış birebir aynı.
import { useState } from 'react';
import { Button, MenuItem } from '@/components/ui';

export function SnoozeButton({ onMute, label = 'Mute', title = 'Mute this anomaly' }: { onMute: (durationSec: number) => void; label?: string; title?: string }) {
  const [open, setOpen] = useState(false);
  const opts: { label: string; sec: number }[] = [
    { label: '1 hour',  sec: 3600 },
    { label: '8 hours', sec: 8 * 3600 },
    { label: '24 hours', sec: 24 * 3600 },
    { label: '7 days', sec: 7 * 24 * 3600 },
  ];
  return (
    <span style={{ position: 'relative', flexShrink: 0 }}>
      <Button variant="ghost" size="sm"
        onClick={() => setOpen(o => !o)}
        title={title}>
        {label}
      </Button>
      {open && (
        <div style={{
          position: 'absolute', top: '100%', right: 0,
          marginTop: 4, padding: 4, borderRadius: 4, zIndex: 'var(--z-dropdown)',
          background: 'var(--bg1)', border: '1px solid var(--border)',
          boxShadow: '0 6px 18px rgba(0,0,0,0.25)',
          display: 'flex', flexDirection: 'column', gap: 2,
        }} onClick={e => e.stopPropagation()}>
          {opts.map(o => (
            <MenuItem key={o.sec} onClick={() => { setOpen(false); onMute(o.sec); }}>
              {o.label}
            </MenuItem>
          ))}
        </div>
      )}
    </span>
  );
}
