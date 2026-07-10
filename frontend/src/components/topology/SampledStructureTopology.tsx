import { useEffect, useMemo, useState } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { AggregateTopology } from '@/components/AggregateTopology';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { AggSpanNode } from '@/lib/types';

// SampledStructureTopology — v0.8.467 (operatör isteği): Service
// Details'teki "Structure for" panelinin Topology görünümü ("sampled
// trace'lerden geldiği için tam istediğim gibi gösteriyor") odaklı
// topoloji yüzeylerinin ANA görünümü oldu. Bu bileşen o panelin
// sampled-yapı topolojisini bağımsız taşır: pencerede span sayısı en
// yüksek ≤50 trace'in gerçek çağrı ağacından servis-düzeyi grafik
// (AggregateTopology → TopologyPillGraph), Cross-service / Internal
// only kapsam düğmeleriyle. Veri kaynağı /api/services/{name}/structure
// (sunucuda 1h cache; panelle aynı 10m pencere + 50 örnek sınırı).
export function SampledStructureTopology({ service }: { service: string }) {
  const [internalOnly, setInternalOnly] = useState(false);
  const [roots, setRoots] = useState<AggSpanNode[] | null | undefined>(undefined);
  const [meta, setMeta] = useState<{ sampledFrom: number; totalSpans: number } | null>(null);

  useEffect(() => {
    let live = true;
    setRoots(undefined);
    api.serviceStructure(service, '10m', 50, internalOnly)
      .then(r => {
        if (!live) return;
        setRoots(r.roots ?? []);
        setMeta({ sampledFrom: r.sampledFrom, totalSpans: r.totalSpans });
      })
      .catch(() => { if (live) setRoots(null); });
    return () => { live = false; };
  }, [service, internalOnly]);

  const sampleNote = useMemo(() => {
    if (!meta) return '';
    return `${fmtNum(meta.sampledFrom)} örnek trace · ${fmtNum(meta.totalSpans)} span (son 10 dk)`;
  }, [meta]);

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
        <div className="seg">
          <button className={!internalOnly ? 'on' : ''} onClick={() => setInternalOnly(false)}>
            Cross-service
          </button>
          <button className={internalOnly ? 'on' : ''} onClick={() => setInternalOnly(true)}>
            Internal only
          </button>
        </div>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>{sampleNote}</span>
      </div>
      {roots === undefined && <Spinner />}
      {roots === null && <Empty icon="✗" title="Yapı örneklemi yüklenemedi" />}
      {roots && roots.length === 0 && (
        <Empty icon="◇" title="Son 10 dakikada örneklenecek trace yok">
          Servis trafiği başlayınca yapı burada belirir.
        </Empty>
      )}
      {roots && roots.length > 0 && <AggregateTopology roots={roots} />}
    </div>
  );
}
