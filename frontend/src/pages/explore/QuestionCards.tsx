import { useNavigate } from 'react-router-dom';
import type { QueryHistoryEntry } from './useQueryHistory';
import { encodeBuilder } from './urlCodec';
import { blankQuery, type BuilderState } from './model';

// fmtAgo — coarse "how long ago" for a recent-query epoch-ms stamp.
// (utils.tsLong takes nanoseconds; history stamps are ms, so format
// locally rather than mis-scale by 1e6.)
function fmtAgo(ms: number): string {
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return `${s}sn önce`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}dk önce`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}sa önce`;
  return `${Math.round(h / 24)}g önce`;
}

// QuestionCards — the paramless /explore entry screen.
//
// New in Phase-1 (explore-v2). When the operator lands on /explore with
// NO search params, instead of a blank builder we show six "what are you
// trying to find out?" cards. Each is the answer to a real triage
// question and deep-links to an EXISTING Explore param shape — so the
// builder, fetch, and URL-write effects all behave exactly as if the
// operator had configured that query by hand. (Phase-2 will swap these
// to ?q= once the compact-JSON builder codec lands.)
//
// Labels are operator-specified Turkish (page content). Explore.tsx
// renders its own user-visible text inline today — there is no t() on
// this page — so these stay inline to match. Styling uses the existing
// .card class + design tokens only (no new colors/fonts).
//
// Below the cards: "Son sorgular" (recent queries) from useQueryHistory.
// A click restores by navigating to the stored search string.

interface Card {
  // Operator-facing question (Turkish).
  title: string;
  // One-line description (Turkish, --text2).
  desc: string;
  // Mono preset summary — the param shape the operator gets.
  pre: string;
  // The /explore search string this card deep-links to. ALWAYS a shape
  // the existing Explore decode path already understands (verified
  // against Explore.tsx's URL initializers).
  to: string;
}

// builderTo — Phase-2: metric cards deep-link with the canonical ?q= codec
// (the workspace decodes legacy shapes forever, but cards emit the v2 form).
function builderTo(st: BuilderState): string {
  return `?q=${encodeURIComponent(encodeBuilder(st))}`;
}
const mkSpanCard = (agg: string, splitBy: string[], viz: BuilderState['viz'] = 'line'): string =>
  builderTo({
    queries: [{ ...blankQuery('A'), agg, splitBy }],
    formula: '', viz, step: 0,
  });

const CARDS: Card[] = [
  {
    title: 'Neden yavaş?',
    desc: 'Gecikme yoğunluğu — zaman × süre ısı haritası, yavaş kuyruk üstte.',
    pre: 'viz=heatmap · duration_ms',
    to: mkSpanCard('count', [], 'heatmap'),
  },
  {
    title: 'Hatalar nereden geliyor?',
    desc: 'Servis başına hata oranı — hangi servis kanıyor?',
    pre: 'A: error_rate · split=service.name',
    to: mkSpanCard('error_rate', ['service.name']),
  },
  {
    title: 'Ne değişti?',
    desc: 'Servis başına P95 gecikme — temel çizgiden sapmayı yakala.',
    pre: 'A: p95 · split=service.name',
    to: mkSpanCard('p95', ['service.name']),
  },
  {
    title: 'N+1 / tekrar var mı?',
    desc: 'Tek trace içinde aynı db.statement ≥ 5 kez — klasik ORM N+1.',
    pre: 'result=repeats · db.statement · ≥5',
    to: '?result=repeats&groupBy=db.statement&minRepeats=5',
  },
  {
    title: 'Trafik nasıl?',
    desc: 'Servis başına saniyelik istek hızı — yük dağılımı.',
    pre: 'A: rate · split=service.name',
    to: mkSpanCard('rate', ['service.name']),
  },
  {
    title: "Tek tek trace'lere bakacağım",
    desc: 'Eşleşen trace listesi — filtrele, sırala, içine in.',
    pre: 'result=traces',
    to: '?result=traces',
  },
];

export function QuestionCards({ history }: { history: QueryHistoryEntry[] }) {
  const navigate = useNavigate();

  return (
    <div>
      <div style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 13, color: 'var(--text2)' }}>
          Ne öğrenmek istiyorsun? Bir soru seç — her kart dolu bir sorgu
          tezgâhı açar; oradan her şeyi değiştirebilirsin.
        </div>
      </div>

      <div className="cards" style={{ marginBottom: 18 }}>
        {CARDS.map(c => (
          <div key={c.title} className="card"
            onClick={() => navigate(`/explore${c.to}`)}
            role="button" tabIndex={0}
            onKeyDown={ev => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); navigate(`/explore${c.to}`); } }}>
            <div className="t" style={{ fontSize: 14, fontWeight: 600, marginBottom: 5 }}>
              {c.title}
            </div>
            <div className="d" style={{ fontSize: 12, color: 'var(--text2)', lineHeight: 1.5, marginBottom: 9 }}>
              {c.desc}
            </div>
            <div className="pre" style={{
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              fontSize: 11, color: 'var(--text3)',
            }}>
              {c.pre}
            </div>
          </div>
        ))}
      </div>

      {history.length > 0 && (
        <div>
          <div style={{
            fontSize: 10.5, fontWeight: 700, letterSpacing: '.5px',
            color: 'var(--text3)', textTransform: 'uppercase', marginBottom: 8,
          }}>
            Son sorgular
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {history.map((h, i) => {
              // Phase-1 stores the full search string as the opaque state.
              const search = typeof h.state === 'string' ? h.state : '';
              return (
                <button key={`${h.desc}|${i}`} type="button"
                  onClick={() => navigate(`/explore${search}`)}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    display: 'flex', alignItems: 'baseline', gap: 10,
                    padding: '7px 10px', borderRadius: 6,
                    border: '1px solid var(--border)', background: 'var(--bg1)',
                  }}
                  title={search || h.desc}>
                  <span style={{ fontSize: 13, color: 'var(--text)' }}>↻</span>
                  <span style={{ fontSize: 12.5, color: 'var(--text2)', flex: 1, minWidth: 0,
                                 overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {h.desc}
                  </span>
                  <span style={{ fontSize: 10.5, color: 'var(--text3)', whiteSpace: 'nowrap' }}>
                    {fmtAgo(h.tm)}
                  </span>
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
