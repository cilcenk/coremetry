import { Link } from 'react-router-dom';
import { Spinner } from '@/components/Spinner';
import { useProblemAffected } from '@/lib/queries/problems';
import { serviceHref } from '@/lib/serviceHref';
import type { AffectedEntity } from '@/lib/types';

// AffectedEntitiesList — v0.10.707 (Dynatrace paritesi #5, dilim 2). Problem
// çekmecesi ve detay sayfası aynı bileşeni çizer: blast-radius çağıranlar
// (çağrı desc; kendi problemi açık olan kırmızı nokta), hipotez pod'ları,
// k8s cluster'lar. Aç-üzerine-getir: bileşen mount olunca tek istek (60 s
// cache). Boş liste dürüst "—" (servissiz problem, ya da pencerede çağıran
// yok) — sessiz gizleme yok.
export function AffectedEntitiesList({ problemId, service, window: win }: {
  problemId: string;
  service?: string;
  window?: { fromNs: number; toNs: number };
}) {
  const q = useProblemAffected(problemId);
  const ents = q.data?.entities ?? [];
  return (
    <div style={{ marginBottom: 12 }}>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.4 }}>
        Affected entities{q.data ? ` (${q.data.total})` : ''}
      </div>
      {q.isPending && <Spinner />}
      {q.isError && <div style={{ fontSize: 12, color: 'var(--err)' }}>Etkilenen varlıklar okunamadı.</div>}
      {q.data && ents.length === 0 && (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>— {service ? 'pencerede çağıran, pod ya da cluster yok' : 'özne servis değil'}</div>
      )}
      {ents.length > 0 && (
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {ents.map(e => <EntityPill key={`${e.kind}|${e.id}`} e={e} service={service} window={win} />)}
        </div>
      )}
    </div>
  );
}

function EntityPill({ e, service, window: win }: { e: AffectedEntity; service?: string; window?: { fromNs: number; toNs: number } }) {
  const title = e.kind === 'service'
    ? `${e.id} · ${e.calls ?? 0} çağrı, ${e.errors ?? 0} hata (${(e.errorRate ?? 0).toFixed(1)}%)${e.hasOpenProblem ? ' · kendi problemi AÇIK' : ''}`
    : e.kind === 'pod' ? `${e.id} · hipotez kanıtında ${e.count ?? 0} kez` : `cluster ${e.id}`;
  const label = (
    <>
      {e.kind === 'service' && <span className="dot" style={{ background: e.hasOpenProblem ? 'var(--err)' : undefined }} />}
      <span style={{ color: 'var(--text3)', fontSize: 10, marginRight: 4 }}>{e.kind}</span>
      <span className="mono">{e.id}</span>
    </>
  );
  if (e.kind === 'service') {
    return <Link to={serviceHref(e.id, { range: win })} className="pb-pill" title={title} style={{ textDecoration: 'none', color: 'var(--accent2)' }}>{label} →</Link>;
  }
  if (e.kind === 'pod' && service) {
    return <Link to={serviceHref(service, { range: win, tab: 'pods', params: { jpod: e.id } })} className="pb-pill" title={title} style={{ textDecoration: 'none', color: 'var(--accent2)' }}>{label} →</Link>;
  }
  return <span className="pb-pill" title={title}>{label}</span>;
}
