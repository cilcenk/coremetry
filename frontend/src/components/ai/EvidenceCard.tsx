// EvidenceCard.tsx — v0.10.558 (CoSRE Faz 4c-2; operatör mockup onayı 2026-09-08).
// Guided kök-neden rotasının yapısal kanıt bloğu: verdict, RED şimdi/taban/Δ,
// penceredeki değişiklikler, log desenleri, açık problemler. Balonun altında
// chart bloğuyla aynı yerde; sabit ≤10 satırlı ham tablolar (sıralanmaz).
// Hipotez yoksa üst satır soluk, kart yine çizilir. Tek commit, kolay geri alınır.
import { Link } from 'react-router-dom';
import { serviceHref } from '@/lib/serviceHref';
import { fmtNum } from '@/lib/utils';
import { evidenceHasHypothesis, fmtEvidenceTime, fmtRangeTR, redRows } from '@/lib/chatEvidence';
import type { ChatEvidence } from '@/lib/types';

export function EvidenceCard({ ev }: { ev: ChatEvidence }) {
  const rows = redRows(ev.red);
  const hasHyp = evidenceHasHypothesis(ev);
  const suspect = ev.problems.find(p => p.topSuspect)?.topSuspect;
  return (
    <section className="ev-card" aria-label={`Kanıt · ${ev.service}`}>
      <div className="ev-head">Kanıt · <span className="mono">{ev.service}</span> · {fmtRangeTR(ev.rangeS)}</div>
      <div className={hasHyp ? 'ev-verdict' : 'ev-verdict ev-verdict--none'}>
        <span aria-hidden>{hasHyp ? '●' : '○'}</span>{' '}
        {ev.verdict}
        {suspect && <> · <Link to={serviceHref(suspect)} className="ev-link">{suspect} ↗</Link></>}
      </div>
      {rows.length > 0 && (
        <table className="ev-table" aria-label="RED şimdi / taban">
          <thead><tr><th>RED</th><th className="num">şimdi</th><th className="num">taban</th><th>Δ</th></tr></thead>
          <tbody>
            {rows.map(r => (
              <tr key={r.key}>
                <td>{r.label}</td>
                <td className="num mono">{r.now}</td>
                <td className="num mono">{r.base}</td>
                <td className={`ev-delta ev-delta--${r.delta.dir}`}>{r.delta.text}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="ev-sub">Değişiklikler (pencere)</div>
      {ev.changes.length === 0 ? <div className="ev-empty">kayıtlı değişiklik yok</div> : (
        <ul className="ev-list">
          {ev.changes.map((c, i) => (
            <li key={i}>
              <span className="mono">{fmtEvidenceTime(c.timeUnixNs)}</span>{' '}
              <span className="badge b-gray">{c.source}</span>{' '}
              {c.workload || c.service}{c.version ? ` → ${c.version}` : ''}{c.status ? ` · ${c.status}` : ''}{c.namespace ? ` · ns ${c.namespace}` : ''}
            </li>
          ))}
        </ul>
      )}
      <div className="ev-sub">Log desenleri</div>
      {ev.logPatterns.length === 0 ? <div className="ev-empty">yeni ya da patlayan desen yok (örneklem)</div> : (
        <ul className="ev-list">
          {ev.logPatterns.map((p, i) => (
            <li key={i}>
              <span className="mono">{p.pattern}</span> ×{fmtNum(p.currentCount)}{' '}
              <span className={p.kind === 'new' ? 'badge b-warn' : 'badge b-err'}>{p.kind}</span>
              {p.baselineCount > 0 ? ` · taban ${fmtNum(p.baselineCount)}, ×${p.ratio.toFixed(1)}` : ''}
            </li>
          ))}
        </ul>
      )}
      {ev.problems.length > 0 && (
        <div className="ev-foot">
          Açık problemler: {ev.problems.length}
          {ev.problems.slice(0, 3).map(p => (
            <Link key={p.id} to={`/problems?problem=${encodeURIComponent(p.id)}`} className="ev-link" title={p.ruleName}>
              [{p.ruleName || p.id}]
            </Link>
          ))}
        </div>
      )}
    </section>
  );
}
