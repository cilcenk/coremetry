import { useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { RuntimeCharts } from './RuntimeCharts';
import { podDetailPath } from './podDetailPath';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// ServiceMetricsTab (v0.9.139) — servis detayının "Metrics" sekmesi:
// Overview'dan taşınan OTel dil-runtime çizelgeleri (JVM heap/GC/threads ·
// .NET · Go — RuntimeCharts, kaynağı CH metric_points / /api/metrics/query).
//
// v0.9.153 (operatör "metrics ile infra entegre olsun, infrastructure daha
// güzel, metrics sayfasında breadcrumb bu mantıkla"): Infra tab'ıyla tutarlı
// pod-drill. Üstte servisin pod'ları (serviceInstances) tıklanabilir çip
// olarak; her biri TAM /pod detayına gider (runtime + infra + JMX tek yerde,
// from='metrics' → /pod geri-breadcrumb'ı "← <svc> · Metrics" der, ?range
// taşınır). Böylece iki tab da aynı pod detayına iner — deneyim birleşir.
export function ServiceMetricsTab({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  const navigate = useNavigate();
  const [sp] = useSearchParams();
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  // Pod listesi (host_name = pod kimliği) + ns/deploy (drill'in /pod'a
  // taşıdığı bağlam; cluster'ı /pod kendisi Thanos'tan çözer).
  const metaQ = useServicesMetadata();
  const ns = metaQ.data?.[service]?.namespace ?? '';
  const deploy = metaQ.data?.[service]?.deployment ?? '';
  const instancesQ = useQuery({
    queryKey: ['svc-instances', service],
    queryFn: () => api.serviceInstances(service),
    enabled: !!service, staleTime: 60_000,
  });
  const pods = useMemo(
    () => [...new Set((instancesQ.data ?? []).map(i => i.id).filter(Boolean))].sort(),
    [instancesQ.data],
  );

  return (
    <div>
      {pods.length > 0 && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
          fontSize: 12, marginBottom: 14,
        }}>
          <span style={{ color: 'var(--text3)' }}>Pods · tıkla → tam detay (runtime · infra · JMX):</span>
          {pods.map(id => (
            <button key={id} type="button" className="mono"
              onClick={() => navigate(podDetailPath({
                service, pod: id, namespace: ns, deploy,
                range: sp.get('range'), from: 'metrics',
              }))}
              title={`${id} — tam pod detayı`}
              style={{
                all: 'unset', cursor: 'pointer', padding: '2px 8px',
                border: '1px solid var(--border)', borderRadius: 4,
                color: 'var(--accent2)', maxWidth: 260, overflow: 'hidden',
                textOverflow: 'ellipsis', whiteSpace: 'nowrap', lineHeight: '18px',
              }}>
              {id} →
            </button>
          ))}
        </div>
      )}
      <RuntimeCharts service={service} from={from} to={to} onZoom={onZoom} />
    </div>
  );
}
