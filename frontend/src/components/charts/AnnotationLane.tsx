import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import type { AnnotationItem } from '@/lib/types';

// AnnotationLane — v0.9.395, Faz C-2 Ş2 (mockup 52b05851 operatör onaylı).
// Chart'ın ALTINDA x-hizalı ince olay bandı: deploy ▲ / rollout ↻ /
// alarm 🔥 (tetik+çözülme) / anomali ◈ / operatör olayı ◇. Aynı
// ±yarım-bucket'a düşenler "×N" kümesi; hover popover olay listesi +
// hedef linkler; tık = anın ±15dk'sına zoom (sayfanın zoom yığını,
// çift-tık geri). Problem x-BÖLGELERİ chart içinde KALIR (alan vurgusu
// şeride sığmaz) — şerit yalnız nokta-olaylar.
// Salt sunum + saf kümeleme (clusterAnnotations, testli); fetch ÇAĞIRANDA.

export interface LaneCluster {
  ts: number; // temsilci zaman (ns) — küme üyelerinin ortalaması değil İLKİ
  frac: number; // 0..1 x konumu
  items: AnnotationItem[];
}

// clusterAnnotations — saf: pencereyi `buckets` dilime böler, aynı dilime
// düşen olayları tek kümeye toplar. Pencere dışı olaylar atılır.
export function clusterAnnotations(
  items: AnnotationItem[], fromNs: number, toNs: number, buckets = 120,
): LaneCluster[] {
  const span = toNs - fromNs;
  if (span <= 0 || items.length === 0) return [];
  const byBucket = new Map<number, AnnotationItem[]>();
  for (const it of items) {
    if (it.ts < fromNs || it.ts >= toNs) continue;
    const b = Math.min(buckets - 1, Math.floor(((it.ts - fromNs) / span) * buckets));
    const arr = byBucket.get(b);
    if (arr) arr.push(it); else byBucket.set(b, [it]);
  }
  return [...byBucket.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([b, its]) => ({
      ts: its[0].ts,
      frac: (b + 0.5) / buckets,
      items: its,
    }));
}

const KIND_GLYPH: Record<string, string> = {
  deploy: '▲', rollout: '↻', alert_fired: '🔥', alert_resolved: '✓',
  anomaly: '◈', event: '◇',
};
const KIND_COLOR: Record<string, string> = {
  deploy: 'var(--text2)', rollout: 'var(--text2)',
  alert_fired: 'var(--err)', alert_resolved: 'var(--ok)',
  anomaly: 'var(--warn)', event: 'var(--text3)',
};

function targetHref(it: AnnotationItem): string | null {
  switch (it.targetType) {
    case 'problem': return `/problems?problem=${encodeURIComponent(it.targetId ?? '')}`;
    case 'anomaly': return `/anomalies?event=${encodeURIComponent(it.targetId ?? '')}`;
    case 'event': return it.link || null;
    default: return null;
  }
}

export function AnnotationLane({ items, fromNs, toNs, onZoomTo }: {
  items: AnnotationItem[];
  fromNs: number;
  toNs: number;
  // Küme tıkı — anın ±15dk penceresi (unix sec); sayfa zoom yığınına push.
  onZoomTo?: (fromSec: number, toSec: number) => void;
}) {
  const clusters = useMemo(
    () => clusterAnnotations(items, fromNs, toNs), [items, fromNs, toNs]);
  const [open, setOpen] = useState<number | null>(null);
  if (clusters.length === 0) return null;
  return (
    <div style={{
      position: 'relative', height: 22, marginTop: 2,
      borderTop: '1px dashed var(--border)',
    }}
      onMouseLeave={() => setOpen(null)}>
      {clusters.map((c, i) => {
        const glyphs = [...new Set(c.items.map(x => KIND_GLYPH[x.kind] ?? '•'))];
        const label = c.items.length > 1
          ? `${glyphs[0]} ×${c.items.length}` : glyphs[0];
        const color = KIND_COLOR[c.items[0].kind] ?? 'var(--text2)';
        return (
          <span key={i}
            onMouseEnter={() => setOpen(i)}
            onClick={() => {
              const sec = c.ts / 1e9;
              onZoomTo?.(sec - 900, sec + 900);
            }}
            title={c.items.length === 1 ? c.items[0].title : undefined}
            style={{
              position: 'absolute', left: `${c.frac * 100}%`,
              transform: 'translateX(-50%)', top: 3, cursor: 'pointer',
              fontSize: 11, lineHeight: 1, color,
              ...(c.items.length > 1 ? {
                background: 'var(--bg2)', border: '1px solid var(--border)',
                borderRadius: 999, padding: '1px 7px', fontSize: 9.5,
              } : {}),
            }}>
            {label}
          </span>
        );
      })}
      {open != null && clusters[open] && (
        <div style={{
          position: 'absolute', bottom: 24, zIndex: 6,
          left: `min(max(${clusters[open].frac * 100}%, 130px), calc(100% - 130px))`,
          transform: 'translateX(-50%)', width: 260,
          background: 'var(--bg1)', border: '1px solid var(--accent)',
          borderRadius: 7, padding: '8px 10px', fontSize: 11.5,
          boxShadow: '0 8px 22px rgba(0,0,0,.4)',
        }}>
          <div style={{ fontWeight: 700, marginBottom: 4 }}>
            {new Date(clusters[open].ts / 1e6).toLocaleTimeString()} · {clusters[open].items.length} olay
          </div>
          <div style={{ display: 'grid', gap: 3, maxHeight: 140, overflowY: 'auto' }}>
            {clusters[open].items.map((it, j) => {
              const href = targetHref(it);
              return (
                <span key={j} style={{ color: KIND_COLOR[it.kind] }}>
                  {KIND_GLYPH[it.kind] ?? '•'} {it.title}
                  {href && (
                    href.startsWith('/')
                      ? <Link to={href} style={{ color: 'var(--accent)', marginLeft: 5 }}>aç →</Link>
                      : <a href={href} target="_blank" rel="noreferrer" style={{ color: 'var(--accent)', marginLeft: 5 }}>aç →</a>
                  )}
                </span>
              );
            })}
          </div>
          {onZoomTo && (
            <div style={{ marginTop: 5, color: 'var(--text3)', fontSize: 10.5 }}>
              tık = pencereyi ±15dk'ya daralt · çift tık = geri
            </div>
          )}
        </div>
      )}
    </div>
  );
}
