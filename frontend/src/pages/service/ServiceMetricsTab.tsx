import { useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { RuntimeCharts } from './RuntimeCharts';
import { podDetailPath } from './podDetailPath';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// Pod-drill çip tavanı — üstü Infra'nın tam pod tablosuna yönlenir (v0.9.154).
const CHIP_CAP = 60;

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
        <div style={{ marginBottom: 14 }}>
          <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 6 }}>
            Pods · tıkla → tam detay (runtime · infra · JMX)
          </div>
          {/* Sınırlı yükseklik + kaydırma: yüksek-replica serviste (serviceInstances
              200'e dek) çip duvarı RuntimeCharts'ı aşağı itmesin (review v0.9.154,
              CLAUDE.md >100 kısıtı). CHIP_CAP üstü Infra'nın tam pod tablosuna yollar. */}
          <div style={{
            display: 'flex', gap: 8, flexWrap: 'wrap', maxHeight: 76,
            overflowY: 'auto', alignContent: 'flex-start',
          }}>
            {pods.slice(0, CHIP_CAP).map(id => (
              <button key={id} type="button" className="mono"
                onClick={() => navigate(podDetailPath({
                  service, pod: id, namespace: ns, deploy,
                  range: sp.get('range'), from: 'metrics',
                }))}
                title={`${id} — tam pod detayı`}
                style={{
                  all: 'unset', cursor: 'pointer', padding: '2px 8px',
                  border: '1px solid var(--border)', borderRadius: 4, fontSize: 12,
                  color: 'var(--accent2)', maxWidth: 260, overflow: 'hidden',
                  textOverflow: 'ellipsis', whiteSpace: 'nowrap', lineHeight: '18px',
                }}>
                {id} →
              </button>
            ))}
            {pods.length > CHIP_CAP && (
              <button type="button"
                onClick={() => navigate(`/service?name=${encodeURIComponent(service)}&tab=infra`)}
                title="Tüm pod'lar — Infrastructure sekmesindeki tablo"
                style={{
                  all: 'unset', cursor: 'pointer', padding: '2px 8px',
                  border: '1px dashed var(--border)', borderRadius: 4, fontSize: 12,
                  color: 'var(--text3)', lineHeight: '18px',
                }}>
                +{pods.length - CHIP_CAP} more → Infrastructure
              </button>
            )}
          </div>
        </div>
      )}
      <RuntimeCharts service={service} from={from} to={to} onZoom={onZoom} />
    </div>
  );
}
