import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import type { AnnotationItem } from '@/lib/types';
import { fmtClock } from '@/lib/utils';

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

export const KIND_GLYPH: Record<string, string> = {
  deploy: '▲', rollout: '↻', alert_fired: '🔥', alert_resolved: '✓',
  anomaly: '◈', event: '◇',
};
export const KIND_COLOR: Record<string, string> = {
  deploy: 'var(--text2)', rollout: 'var(--text2)',
  alert_fired: 'var(--err)', alert_resolved: 'var(--ok)',
  anomaly: 'var(--warn)', event: 'var(--text3)',
};
// v0.9.492 — lejant etiketleri (yalnız şeritte GERÇEKTEN görünen türler
// çizilir; ServiceAnnotationLane şeridin altına basar).
export const KIND_LABEL_TR: Record<string, string> = {
  deploy: 'deploy', rollout: 'rollout', alert_fired: 'alarm',
  alert_resolved: 'çözüldü', anomaly: 'anomali', event: 'olay',
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
  // v0.9.492 (operatör: "alev ve tick işaretleri anlaşılmıyor, timeline
  // gibi olsa daha iyi") — şerit gerçek bir zaman çizgisine dönüştü:
  // kesikli süs çizgisi yerine SOLID baseline, her işaretin baseline'a
  // inen renkli bir sapı, ve şeridin KENDİ saat etiketleri (üç kartın
  // içindeki eksenlerle hizalanamıyordu; kendi ekseni konumu okunur
  // kılar). Kümeleme/hover/tık-zoom aynen.
  const axis = useMemo(() => {
    const fracs = [0, 0.25, 0.5, 0.75, 1];
    const secs = fracs.map(f => (fromNs + f * (toNs - fromNs)) / 1e9);
    const sameDay = new Date(secs[0] * 1000).toDateString() ===
      new Date(secs[secs.length - 1] * 1000).toDateString();
    const fmt = (s: number) => {
      const d = new Date(s * 1000);
      const p2 = (n: number) => String(n).padStart(2, '0');
      const hm = `${p2(d.getHours())}:${p2(d.getMinutes())}`;
      return sameDay ? hm : `${p2(d.getDate())}.${p2(d.getMonth() + 1)} ${hm}`;
    };
    return fracs.map((f, i) => ({ frac: f, label: fmt(secs[i]) }));
  }, [fromNs, toNs]);
  if (clusters.length === 0) return null;
  const BASE = 17; // baseline'ın üstten piksel konumu
  return (
    <div style={{ position: 'relative', height: 34, marginTop: 2 }}
      onMouseLeave={() => setOpen(null)}>
      {/* baseline */}
      <div style={{
        position: 'absolute', top: BASE, left: 0, right: 0,
        borderTop: '1px solid var(--border)',
      }} />
      {/* saat etiketleri + eksen çentikleri (uçlar taşmasın diye clamp) */}
      {axis.map((t, i) => (
        <span key={`ax${i}`}>
          <span style={{
            position: 'absolute', top: BASE - 3, left: `${t.frac * 100}%`,
            width: 1, height: 7, background: 'var(--border)',
          }} />
          <span style={{
            position: 'absolute', top: BASE + 5, left: `${t.frac * 100}%`,
            transform: i === 0 ? 'none' : i === axis.length - 1 ? 'translateX(-100%)' : 'translateX(-50%)',
            fontSize: 9, color: 'var(--text3)', lineHeight: 1,
          }}>{t.label}</span>
        </span>
      ))}
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
              transform: 'translateX(-50%)', top: 1, cursor: 'pointer',
              fontSize: 11, lineHeight: 1, color, zIndex: 2,
              display: 'inline-flex', flexDirection: 'column', alignItems: 'center',
              ...(c.items.length > 1 ? {
                background: 'var(--bg2)', border: '1px solid var(--border)',
                borderRadius: 999, padding: '1px 7px', fontSize: 9.5,
              } : {}),
            }}>
            {label}
            {/* işaretin baseline'a inen sapı — konum bir ZAMAN noktası
                olarak okunur (timeline dili), simge havada yüzmez */}
            {c.items.length === 1 && (
              <span style={{ width: 1.5, height: 5, marginTop: 1, background: color }} />
            )}
          </span>
        );
      })}
      {open != null && clusters[open] && (
        <div style={{
          position: 'absolute', bottom: 36, zIndex: 6,
          left: `min(max(${clusters[open].frac * 100}%, 130px), calc(100% - 130px))`,
          transform: 'translateX(-50%)', width: 260,
          background: 'var(--bg1)', border: '1px solid var(--accent)',
          borderRadius: 7, padding: '8px 10px', fontSize: 11.5,
          boxShadow: '0 8px 22px rgba(0,0,0,.4)',
        }}>
          <div style={{ fontWeight: 700, marginBottom: 4 }}>
            {fmtClock(clusters[open].ts / 1e6)} · {clusters[open].items.length} olay
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
