import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { Drawer } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { serviceHref } from '@/lib/serviceHref';
import { tracesPivotHref } from '@/lib/pivotHref';
import { fmtNum, fmtFixed } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// ServiceMapNodeDrawer — v0.9.1112 (Faz 5 "harita salt görselden giriş
// kapısına"). Odaklı düğüme ikinci tık bu çekmeceyi açar: pencerenin
// RED değerleri + sinyallere pivotlar, haritadan ayrılmadan.
//
// ES-cost sözleşmesi: fetch YALNIZ açılışta (tek /api/services okuması,
// name= daraltılmış, 30s stale) — liste üstünde prefetch yok, poll yok.
// Kabuk ui/Drawer (overlay/Esc/✕ tek evden, InboxTriageDrawer emsali).
export function ServiceMapNodeDrawer({ service, range, fromNs, toNs, onClose }: {
  service: string;
  range: TimeRange;
  fromNs: number;
  toNs: number;
  onClose: () => void;
}) {
  const q = useQuery({
    queryKey: ['servicemap', 'node-red', service, fromNs, toNs],
    // name= substring eşleşmesi — birebir satırı istemci seçer (name'in
    // başka adların içinde geçtiği kurulumda ilk satıra güvenilmez).
    queryFn: async () => {
      const r = await api.servicesPage({ from: fromNs, to: toNs }, { name: service, limit: 50 });
      return r?.services.find(s => s.name === service) ?? null;
    },
    staleTime: 30_000,
  });
  const s = q.data;
  const win = { fromNs, toNs };

  return (
    <Drawer onClose={onClose} width={420} header={
      <>
        {s?.health && (
          <span className={`badge b-${s.health === 'red' ? 'err' : s.health === 'yellow' ? 'warn' : 'ok'}`}
            style={{ fontSize: 10 }} title={s.healthReason}>
            {s.health.toUpperCase()}
          </span>
        )}
        <Link to={serviceHref(service, { range })} style={{ fontWeight: 700, fontSize: 14 }}>
          {service}
        </Link>
      </>
    }>
      <div style={{ paddingTop: 10 }}>
        {q.isLoading && <Spinner />}
        {!q.isLoading && !s && (
          <Empty icon="⬡" title="Bu pencerede veri yok">
            Servis seçili zaman aralığında hiç span üretmemiş; aralığı
            genişletmeyi dene.
          </Empty>
        )}
        {s && (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '6px 14px',
              fontSize: 12, marginBottom: 14,
            }}>
              <span style={{ color: 'var(--text3)' }}>Spans</span>
              <span className="mono">{fmtNum(s.spanCount)}</span>
              <span style={{ color: 'var(--text3)' }}>Error rate</span>
              <span className="mono">
                <span className={`badge b-${s.errorRate > 5 ? 'err' : s.errorRate > 0 ? 'warn' : 'ok'}`}>
                  {fmtFixed(s.errorRate, 2)}%
                </span>
              </span>
              <span style={{ color: 'var(--text3)' }}>Avg</span>
              <span className="mono">{fmtFixed(s.avgDurationMs, 1)}ms</span>
              <span style={{ color: 'var(--text3)' }}>P99</span>
              <span className="mono">{fmtFixed(s.p99DurationMs, 1)}ms</span>
              <span style={{ color: 'var(--text3)' }}>Apdex</span>
              <span className="mono">{fmtFixed(s.apdex, 2)}</span>
              {(s.openProblems ?? 0) > 0 && (
                <>
                  <span style={{ color: 'var(--text3)' }}>Open problems</span>
                  <span>
                    <Link className="badge b-err" style={{ textDecoration: 'none' }}
                      to={`/inbox?service=${encodeURIComponent(service)}`}
                      title="Inbox'ta bu servisin açık problemleri">
                      {s.openProblems}
                    </Link>
                  </span>
                </>
              )}
            </div>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <Link className="sec" style={{ textDecoration: 'none' }}
                to={`/logs?service=${encodeURIComponent(service)}&range=custom:${Math.floor(fromNs / 1e6)}-${Math.ceil(toNs / 1e6)}`}
                title="Bu pencerede servisin logları">
                Logs
              </Link>
              <Link className="sec" style={{ textDecoration: 'none' }}
                to={tracesPivotHref({ window: win, service, hasError: true })}
                title="Bu pencerede servisin hatalı trace'leri">
                Error traces
              </Link>
              <Link className="sec" style={{ textDecoration: 'none' }}
                to={`/endpoints?service=${encodeURIComponent(service)}`}
                title="Servisin rota-bazlı RED tablosu">
                Endpoints
              </Link>
            </div>
          </>
        )}
      </div>
    </Drawer>
  );
}
