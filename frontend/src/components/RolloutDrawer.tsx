// RolloutDrawer — v0.10.203 (ROLLOUTS Faz 4b; audit §4.2). Satır çekmecesi:
// rollout kimliği URL'de (?rollout=<codec> — rolloutRow.ts encode/decode,
// rolloutHref.test.ts pinler); içerik /api/rollout/detail (30 s sunucu
// cache'i, staleTime aynı). Eski Deployment Report gövdesinin tek-rollout
// daraltması: servis başına health verdict + önce/sonra RED + deploy'dan
// beri problem / anomali / yeni hata. Küçük sabit listeler ham <table>
// (frontend-conventions: sabit ≤10 satır meşru); 20'de kesilir ve ilgili
// sayfaya köprü verir.
import { Link } from 'react-router-dom';
import { Drawer, DrawerSection, Badge } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { serviceHref } from '@/lib/serviceHref';
import { fmtDateTime, fmtNum } from '@/lib/utils';
import { useRolloutDetail } from '@/lib/queries';
import { statusTone, statusLabel, statusTitle, shortRevision, imageDiff, imageRef, rolloutChangeKind, changeKindLabel, changeKindTitle, changeKindTone } from '@/lib/rolloutRow';
import type { RolloutIdParam } from '@/lib/rolloutRow';

function pct(n: number) { return `%${n.toFixed(1)}`; }
function ms(n: number) { return `${n.toFixed(0)}ms`; }
function rps(n: number) { return `${n.toFixed(2)}/s`; }
const CAP = 20;

export function RolloutDrawer({ id, onClose }: { id: RolloutIdParam; onClose: () => void }) {
  const q = useRolloutDetail(id);
  const d = q.data;
  return (
    <Drawer onClose={onClose} width={720} header={
      <>
        <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 14, fontWeight: 600 }}>{id.workload}</span>
        {d && <Badge tone={statusTone(d.rollout.status)}>{statusLabel(d.rollout.status)}</Badge>}
        {d && <span className="field-hint">{shortRevision(d.rollout.revision, d.rollout.workload)} · {imageDiff(d.rollout)} · {fmtDateTime(new Date(d.rollout.startedAt))}</span>}
      </>
    }>
      {q.isPending && <Spinner />}
      {q.isError && <Empty icon="!" title="Çekmece yüklenemedi">{(q.error as Error).message}</Empty>}
      {d && (d.rollout.note || d.note) && (
        <div className="field-hint" style={{ marginBottom: 8 }}>{[d.rollout.note, d.note].filter(Boolean).join(' · ')}</div>
      )}
      {d && (() => {
        // v0.10.234 — Operator-reported: "hangi versiyondan hangisine geçti
        // göremiyorum". Başlıktaki tek satır kısaltılmış revizyon + imaj
        // diff'iydi; tam kimlikler (revizyon, repo:tag) ve olayın TÜRÜ
        // (imaj değişti → Deployment / aynı → config rollout) burada.
        const r = d.rollout;
        const k = rolloutChangeKind(r);
        const mono = { fontFamily: 'ui-monospace, monospace', wordBreak: 'break-all' as const };
        return (
          <DrawerSection title="Geçiş">
            <table style={{ width: '100%' }}>
              <tbody>
                <tr><th style={{ textAlign: 'left', width: 110 }}>tür</th><td><Badge tone={changeKindTone(k)}>{changeKindLabel(k)}</Badge> <span className="field-hint">{changeKindTitle(k)}</span></td></tr>
                <tr><th style={{ textAlign: 'left' }}>durum</th><td><Badge tone={statusTone(r.status)}>{statusLabel(r.status)}</Badge> <span className="field-hint">{statusTitle(r.status)}</span></td></tr>
                <tr><th style={{ textAlign: 'left' }}>revizyon</th><td style={mono}>{r.prevRevision || '—'} → <b>{r.revision}</b></td></tr>
                <tr><th style={{ textAlign: 'left' }}>imaj</th><td style={mono}>{imageRef(r.prevImage, r.prevImageTag)} → <b>{imageRef(r.image, r.imageTag)}</b></td></tr>
                <tr><th style={{ textAlign: 'left' }}>zaman</th><td className="field-hint">başladı {fmtDateTime(new Date(r.startedAt))}{r.completedAt > 0 ? ` · tamamlandı ${fmtDateTime(new Date(r.completedAt))}` : ''}{r.detectedBy ? ` · kaynak ${r.detectedBy}` : ''}</td></tr>
              </tbody>
            </table>
          </DrawerSection>
        );
      })()}
      {d && d.services.length === 0 && (
        <Empty icon="∅" title="Bu revizyonun servisi çözülemedi">{d.note || 'MV bu revizyon için servis kaydı taşımıyor (etkinlik penceresi geçmiş olabilir).'}</Empty>
      )}
      {d && d.services.length > 0 && (
        <>
          <DrawerSection title={`Servis sağlığı — deploy öncesi/sonrası (${d.services.length})`}>
            <div className="table-wrap">
              <table style={{ width: '100%' }}>
                <thead><tr><th>servis</th><th>sağlık</th><th style={{ textAlign: 'right' }}>hata% ö/s</th><th style={{ textAlign: 'right' }}>p99 ö/s</th><th style={{ textAlign: 'right' }}>istek ö/s</th></tr></thead>
                <tbody>
                  {d.services.map(s => (
                    <tr key={s.service}>
                      <td className="mono" style={{ fontSize: 12 }}><Link to={serviceHref(s.service, { params: { range: `custom:${Math.round(d.since / 1e6)}-${Math.round(d.generatedAt / 1e6)}` } })} className="sec">{s.service}</Link></td>
                      <td><Badge tone={s.health === 'red' ? 'danger' : s.health === 'yellow' ? 'warning' : s.health === 'green' ? 'success' : 'neutral'}>{s.health || 'n/a'}</Badge></td>
                      {s.after.throughput === 0 ? (
                        <>
                          {/* deploy'dan sonra hiç span yok: sahte %0.0/0ms basma */}
                          <td className="num mono">{pct(s.before.errorRate)} → <span style={{ color: 'var(--warn)' }}>—</span></td>
                          <td className="num mono">{ms(s.before.p99Ms)} → <span style={{ color: 'var(--warn)' }}>—</span></td>
                          <td className="num mono">{rps(s.before.throughput)} → <span style={{ color: 'var(--warn)' }} title="deploy'dan sonra span görülmedi">yok</span></td>
                        </>
                      ) : (
                        <>
                          <td className="num mono">{pct(s.before.errorRate)} → <span style={s.after.errorRate > s.before.errorRate ? { color: 'var(--err)' } : undefined}>{pct(s.after.errorRate)}</span></td>
                          <td className="num mono">{ms(s.before.p99Ms)} → <span style={s.after.p99Ms > s.before.p99Ms ? { color: 'var(--err)' } : undefined}>{ms(s.after.p99Ms)}</span></td>
                          <td className="num mono">{rps(s.before.throughput)} → {rps(s.after.throughput)}</td>
                        </>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </DrawerSection>
          <SignalSection title="Deploy'dan beri açık problemler" rows={d.services.flatMap(s => s.problems.map(p => ({ key: p.id, svc: s.service, a: p.severity, b: p.ruleName, at: p.startedAt })))} moreHref="/problems" />
          <SignalSection title="Aktif anomaliler" rows={d.services.flatMap(s => s.anomalies.map(a => ({ key: `${s.service}/${a.id}`, svc: s.service, a: a.kind, b: a.pattern, at: a.startedAt })))} moreHref="/anomalies" />
          <SignalSection title="Yeni hatalar" rows={d.services.flatMap(s => s.newErrors.map(e => ({ key: `${s.service}/${e.fingerprint}`, svc: s.service, a: e.type, b: e.message, at: e.firstSeen })))} moreHref="/problems" />
        </>
      )}
    </Drawer>
  );
}

function SignalSection({ title, rows, moreHref }: { title: string; rows: { key: string | number; svc: string; a: string; b: string; at: number }[]; moreHref: string }) {
  return (
    <DrawerSection title={`${title} (${rows.length})`}>
      {rows.length === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>yok</div>
      ) : (
        <div className="table-wrap">
          <table style={{ width: '100%' }}>
            <tbody>
              {rows.slice(0, CAP).map(r => (
                <tr key={r.key}>
                  <td className="mono" style={{ fontSize: 12, width: 160 }}>{r.svc}</td>
                  <td className="field-hint" style={{ width: 120 }}>{r.a}</td>
                  <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: 320 }} title={r.b}>{r.b}</td>
                  <td className="num mono" style={{ width: 150 }}>{fmtDateTime(new Date(Math.round(r.at / 1e6)))}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length > CAP && <div className="field-hint">{fmtNum(rows.length - CAP)} satır daha — <Link to={moreHref} className="sec">tam liste</Link></div>}
        </div>
      )}
    </DrawerSection>
  );
}
