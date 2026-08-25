import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useServiceRollouts7d } from '@/lib/queries/services';
import { api } from '@/lib/api';
import { timeRangeToNs, tsRel, rangeToSince } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// DetailsPropsStrip — v0.8.370 (operator-approved mockup): the
// Dynatrace "Properties and tags" counterpart on the Details tab.
// One compact strip: technology/SDK, version + last rollout,
// clusters, pod count. Every datum comes from an EXISTING endpoint
// (runtime fingerprint is server-cached 5 min; the rest are the
// same reads the panels below make) — no new backend. Each group
// renders independently and simply drops out on error/empty, so
// the strip never blocks the tab.

const STALE = 60_000;

function Prop({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'baseline', gap: 6 }}>
      <span style={{ color: 'var(--text3)', fontSize: 10, textTransform: 'uppercase', letterSpacing: '.6px' }}>{k}</span>
      <span style={{ fontSize: 12.5, fontWeight: 600 }}>{children}</span>
    </span>
  );
}

export function DetailsPropsStrip({ service, range }: { service: string; range: TimeRange }) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const runtimeQ = useQuery({
    queryKey: ['svc-runtime', service],
    queryFn: () => api.serviceRuntime(service),
    enabled: !!service, staleTime: 5 * 60_000, retry: false,
  });
  // v0.9.360 — panelle AYNI 7g okuma (tek istek, tek cache girdisi).
  // Bonus: Version çipi artık 7 güne bakıyor — varsayılan 30dk pencerede
  // sürüm-değiştiren rollout neredeyse hiç olmadığı için çip hiç çıkmıyordu.
  const rolloutsQ = useServiceRollouts7d(service);
  const clustersQ = useQuery({
    queryKey: ['svc-clusters', service, from, to],
    queryFn: () => api.serviceClusters(service, from, to),
    enabled: !!service, staleTime: STALE, retry: false,
  });
  // v0.8.383 (env-separation 0c) — which deployment environments this
  // service emitted from in the window (the operator's "same
  // mobile-bff in int/uat/prep" case). Bounded per-service GROUP BY
  // deploy_env server-side, 60s cached; drops out like every group.
  const envsQ = useQuery({
    queryKey: ['svc-envs', service, from, to],
    queryFn: () => api.serviceEnvironments(service, from, to),
    enabled: !!service, staleTime: STALE, retry: false,
  });
  // v0.9.261 — was `range.preset` straight through. The preset vocabulary
  // includes '2d' / '7d' / '30d', which Go's time.ParseDuration rejects, so
  // internal/api.parseDuration fell back to this endpoint's 15-MINUTE default:
  // picking a 7-day window quietly showed a quarter hour of instances. It also
  // never handled 'custom'. rangeToSince covers both and can only emit h/m/s.
  const instancesSince = rangeToSince(range).since;
  const instancesQ = useQuery({
    queryKey: ['svc-instances-strip', service, instancesSince],
    queryFn: () => api.serviceInstances(service, instancesSince),
    enabled: !!service, staleTime: STALE, retry: false,
  });

  const rt = runtimeQ.data;
  const tech = rt?.language
    ? `${rt.language}${rt.runtimeVersion ? ' · ' + rt.runtimeVersion : ''}${rt.sdkVersion ? ' · otel ' + rt.sdkVersion : ''}`
    : null;
  const rollouts = rolloutsQ.data?.rollouts ?? [];
  // v0.8.405 — the header chip means "the new code shipped": key it
  // on the last VERSION-CHANGING rollout, not a restart/reschedule.
  const deploys = rollouts.filter(r => r.kind !== 'restart' && r.versionAfter);
  const last = deploys.length ? deploys[deploys.length - 1] : null;
  const version = last?.versionAfter || null;
  const clusters = (clustersQ.data?.clusters ?? []).map(c => c.cluster).filter(c => c && c !== '(default)');
  const envs = envsQ.data?.environments ?? [];
  // v0.9.374 — "Pods" ŞU AN yaşayanlar: strip'te 28 okuyan operatör güncel
  // replika sayısı anlar; 7g penceresinde bu "hafta boyunca var olmuş her
  // pod"du (günlük deploy'lu 4-replika servis 28+ gösteriyordu). `up`
  // (2dk tazelik) zaten wire'da — sıfır yeni sorgu.
  const pods = instancesQ.data?.filter(i => i.up).length ?? 0;

  if (!tech && !version && clusters.length === 0 && envs.length === 0 && pods === 0) return null;

  const chip: React.CSSProperties = {
    border: '1px solid var(--border)', borderRadius: 3, background: 'var(--bg2)',
    padding: '0 6px', fontSize: 11, color: 'var(--text2)',
  };

  return (
    <div style={{
      display: 'flex', flexWrap: 'wrap', gap: '8px 22px', alignItems: 'center',
      border: '1px solid var(--border)', borderRadius: 6, background: 'var(--bg2)',
      padding: '9px 14px', marginBottom: 14,
    }}>
      {tech && <Prop k="Technology">{tech}</Prop>}
      {version && (
        <Prop k="Version">
          <span className="mono">{version}</span>
          {last && (
            <a href="#deploys" style={{ ...chip, marginLeft: 6, textDecoration: 'none' }}
              title="Recent rollouts panel">
              deployed {tsRel(last.timeUnixNs)}
            </a>
          )}
        </Prop>
      )}
      {envs.length > 0 && (
        <Prop k="Envs">
          {envs.map(e => <span key={e} style={{ ...chip, marginRight: 4 }}>{e}</span>)}
        </Prop>
      )}
      {clusters.length > 0 && (
        <Prop k="Clusters">
          {clusters.slice(0, 4).map(c => <span key={c} style={{ ...chip, marginRight: 4 }}>{c}</span>)}
          {clusters.length > 4 && <span style={{ color: 'var(--text3)', fontSize: 11 }}>+{clusters.length - 4}</span>}
        </Prop>
      )}
      {pods > 0 && <Prop k="Pods"><span className="mono">{pods}</span></Prop>}
    </div>
  );
}
