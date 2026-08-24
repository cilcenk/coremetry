import { Suspense, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { computeCriticalPath } from '@/lib/criticalPath';
import { alignTraces, type AlignedPair } from '@/lib/spanAlign';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { PageShell } from '@/components/ui/PageShell';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { fmtNs } from '@/lib/utils';
import type { SpanRow, TraceDetailResponse } from '@/lib/types';
import { traceHref } from '@/lib/traceHref';

type CompareTab = 'split' | 'diff';

// /trace/compare?a=<traceId>&b=<traceId>
//
// Side-by-side waterfall view for performance regression
// debugging. The two traces load in parallel via two useQuery
// calls; the page renders nothing until either both arrive or
// one errors. Each side gets its OWN computed critical path so
// the highlight reflects what was slow in THAT trace, not the
// other.
//
// Header summary: per-side total duration + the delta (B − A)
// in ms with a colour cue (red = slower, green = faster). For
// the operator that's the headline number — "is the new build
// faster or slower, by how much".
//
// Span matching across traces (advanced — not in this round):
// future work could align spans by name+depth so the operator
// sees per-operation deltas. For now the side-by-side
// waterfalls are independent; the operator scans both at the
// same x-scale to spot the divergent operation.
//
// Performance: each useQuery caches per traceId; tabbing back
// to the same comparison is instant. TraceWaterfall already
// virtualises long traces internally; rendering two of them
// is ~2x the cost of one, not the (#A × #B) the naive merge
// would have.

export default function TraceComparePage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp] = useSearchParams();
  const a = sp.get('a') ?? '';
  const b = sp.get('b') ?? '';

  const aQ = useQuery<TraceDetailResponse>({
    queryKey: ['trace', a],
    queryFn: () => api.trace(a),
    enabled: !!a,
    staleTime: 5 * 60_000, // traces are immutable; cache aggressively
  });
  const bQ = useQuery<TraceDetailResponse>({
    queryKey: ['trace', b],
    queryFn: () => api.trace(b),
    enabled: !!b,
    staleTime: 5 * 60_000,
  });

  // Local input state for the second-trace picker. Operators
  // typically open compare from a single trace ("Compare ↔"
  // button) and then paste the other ID into this field.
  const [bDraft, setBDraft] = useState(b);
  const [tab, setTab] = useState<CompareTab>('split');

  if (!a) {
    return (
      <>
        <Topbar title="Trace compare" />
        <PageShell>
          <Empty icon="↔" title="No trace selected">
            Open a trace and click <b>Compare ↔</b> to start a side-by-side comparison.
          </Empty>
        </PageShell>
      </>
    );
  }

  // v0.9.1000 (denetim DALGA 9, kapanış) — kap `<PageShell>`e geçti ve
  // şelale kutusunun `height: calc(100vh - 220px)` sihirli sayısı SİLİNDİ.
  //
  // VARYANT SEÇİMİ — `default`, `full` DEĞİL. İkisi de ölçüldü:
  //   · `full` `#content`e `padding: 0; overflow: hidden` basıyor. İki
  //     sonucu var ve ikisi de bu sayfada yanlış: (1) `overflow: hidden`
  //     "Aligned diff" sekmesini KESERDİ — o tablo normal akışta kalıyor
  //     ve sayfa kaydırmasına muhtaç (brief kararı), (2) dolgu sıfırlanınca
  //     `[data-density]`nin dört değeri (6/10/20/22px) ve ≤640px'in 12px'i
  //     sayfa İÇİNDE yeniden kurulmak zorunda kalırdı — yani D9'un
  //     kaldırdığı sihirli sayı sınıfının aynısı, sadece yeri değişmiş.
  //   · `default` hiçbirini gerektirmiyor: `#content` zaten
  //     `flex: 1; overflow: auto` ve yüksekliği KESİN. Kabın içine tek bir
  //     `min-height: 100%` flex kolonu (`.tc-fill`) koymak yetiyor; split
  //     ızgarası `flex: 1; min-height: 0` ile kalan yüksekliği alıyor,
  //     şelale kutusu da onun içinde. Yükseklik artık gerçek yerleşimden
  //     geliyor: yoğunluk değişince dolgu değişiyor, kutu kendiliğinden
  //     uyuyor. AdminSql'in v0.9.981'de aldığı kararın (`height: 100%;
  //     minHeight: 0`) aynısı.
  return (
    <>
      <Topbar title="Trace compare" />
      <PageShell>
        <div className="tc-fill">
        <div className="controls" style={{ marginBottom: 12, alignItems: 'center' }}>
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>
            Trace A:{' '}
            <Link to={traceHref(a)}
                  style={{ fontFamily: 'monospace' }}>
              {a.slice(0, 12)}…
            </Link>
          </span>
          <span style={{ fontSize: 12, color: 'var(--text2)', marginLeft: 12 }}>
            Trace B:
          </span>
          <input value={bDraft}
                 onChange={e => setBDraft(e.target.value.trim())}
                 placeholder="paste trace ID…"
                 style={{ width: 260, fontFamily: 'monospace', fontSize: 12 }} />
          <Link to={`/trace/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(bDraft)}`}
                aria-disabled={!bDraft}
                className="sec"
                style={{
                  fontSize: 12, padding: '3px 10px',
                  textDecoration: 'none', color: 'var(--text)',
                  border: '1px solid var(--border)', borderRadius: 6,
                  opacity: bDraft ? 1 : 0.5,
                  pointerEvents: bDraft ? 'auto' : 'none',
                }}>
            Load
          </Link>
          <span style={{ marginLeft: 'auto' }} />
        </div>

        {!b && (
          <Empty icon="↔" title="Pick a second trace">
            Paste a trace ID to compare against <code>{a.slice(0, 12)}…</code>.
            The two will render side-by-side with their own critical paths
            highlighted, so a regression jumps off the page.
          </Empty>
        )}

        {b && (
          <>
            <div className="tab-strip" style={{ marginBottom: 10 }}>
              <button onClick={() => setTab('split')}
                      className={tab === 'split' ? 'active' : ''}>
                Split waterfall
              </button>
              <button onClick={() => setTab('diff')}
                      className={tab === 'diff' ? 'active' : ''}>
                Aligned diff
              </button>
            </div>

            {/* Split ızgarası: `.grid-2` (D8'in paylaşılan sınıfı) ≤640px'te
                tek kolona düşüyor — iki 50% kolon 390px'lik bir telefonda
                şelale için kullanılamaz. `.tc-split` de kalan yüksekliği
                alan flex satırı. "Aligned diff" sekmesi BİLEREK normal
                akışta kalıyor: tablo uzadıkça sayfa kaydırır. */}
            {tab === 'split' && (
              <div className="grid-2 tc-split" style={{ gap: 12 }}>
                <TraceSide label="A" id={a} q={aQ} otherQ={bQ} />
                <TraceSide label="B" id={b} q={bQ} otherQ={aQ} />
              </div>
            )}

            {tab === 'diff' && (
              <AlignedDiff aQ={aQ} bQ={bQ} />
            )}
          </>
        )}
        </div>
      </PageShell>
    </>
  );
}

function TraceSide({ label, id, q, otherQ }: {
  label: 'A' | 'B';
  id: string;
  q: ReturnType<typeof useQuery<TraceDetailResponse>>;
  otherQ: ReturnType<typeof useQuery<TraceDetailResponse>>;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  const spans: SpanRow[] = q.data?.spans ?? [];
  const totalNs = useMemo(() => {
    if (spans.length === 0) return 0;
    const minT = Math.min(...spans.map(s => s.startTime));
    const maxT = Math.max(...spans.map(s => s.endTime));
    return maxT - minT;
  }, [spans]);

  const critical = useMemo(() => {
    if (spans.length === 0) return null;
    return computeCriticalPath(spans.map(s => ({
      spanId: s.spanId,
      parentId: s.parentSpanId ?? '',
      startTime: s.startTime,
      duration: s.endTime - s.startTime,
    })));
  }, [spans]);

  // Delta vs the other side — only meaningful once both
  // queries have data. Positive (B − A) > 0 → B is slower (red);
  // < 0 → B faster (green); 0 → identical.
  const otherSpans: SpanRow[] = otherQ.data?.spans ?? [];
  const otherTotalNs = useMemo(() => {
    if (otherSpans.length === 0) return 0;
    const minT = Math.min(...otherSpans.map(s => s.startTime));
    const maxT = Math.max(...otherSpans.map(s => s.endTime));
    return maxT - minT;
  }, [otherSpans]);
  const deltaNs = label === 'B' && otherTotalNs > 0 ? totalNs - otherTotalNs : 0;
  const deltaColor = deltaNs > 0 ? 'var(--err)' : deltaNs < 0 ? 'var(--ok)' : 'var(--text3)';

  return (
    <div className="tc-side">
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 10,
        display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap',
      }}>
        <span style={{ fontSize: 14, fontWeight: 700 }}>Trace {label}</span>
        <Link to={traceHref(id)}
              style={{ fontFamily: 'monospace', fontSize: 11 }}>
          {id.slice(0, 12)}…
        </Link>
        {q.isLoading && <span style={{ color: 'var(--text3)', fontSize: 12 }}>loading…</span>}
        {q.isError && <span style={{ color: 'var(--err)', fontSize: 12 }}>load failed</span>}
        {q.data && (
          <>
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>
              {spans.length} spans · {fmtNs(totalNs)}
            </span>
            {critical && (
              <span style={{ color: 'var(--text3)', fontSize: 11 }}>
                critical {fmtNs(critical.totalNs)}
              </span>
            )}
            {label === 'B' && otherQ.data && (
              <span style={{ marginLeft: 'auto', color: deltaColor, fontSize: 12, fontWeight: 600 }}>
                {deltaNs >= 0 ? '+' : '−'}{fmtNs(Math.abs(deltaNs))}
                <span style={{ color: 'var(--text3)', fontWeight: 400, marginLeft: 4 }}>
                  vs A
                </span>
              </span>
            )}
          </>
        )}
      </div>
      {/* v0.9.1000 — `height: calc(100vh - 220px)` SİLİNDİ. O sayı tam
          genişlikte topbar + kontroller + sekme şeridine göre ELLE
          kalibre edilmişti ve `[data-density]` dolgusuna (6/10/20/22px)
          da ≤640px bloğuna da KÖRDÜ: yoğunluk değiştiren operatörde kutu
          ya taşıyor ya boşluk bırakıyordu. Yükseklik artık `.tc-wf`
          (`flex: 1; min-height: 0`) ile GERÇEK yerleşimden geliyor. */}
      {q.data && spans.length > 0 && (
        <div className="tc-wf">
          <TraceWaterfall spans={spans} selectedId={selected} onSelect={setSelected}
                          criticalPathIds={critical?.ids} />
        </div>
      )}
      {q.isLoading && <Spinner />}
    </div>
  );
}

// AlignedDiff renders the per-operation delta table — the
// payoff of trace alignment. Pairs are pre-sorted by abs
// delta so the biggest regressions are at the top.
//
// Each row has a colour-coded delta cell (red = slower in B,
// green = faster), the matched path label, and the absolute
// duration on each side. "Only in A" / "Only in B" rows fall
// to the bottom of the list because they're informational —
// most useful when refactoring (operations renamed) rather
// than performance regression.
//
// The list is virtual-scroll-ready in shape (fixed height
// rows, sortable) but at the typical trace size (<1k spans
// per side) we render straight to DOM. Future: switch to
// VirtualList if traces routinely exceed 1k spans/side.
// v0.9.877 (tutarlılık denetimi BT7) — elle yazılmış <thead> paylaşılan
// primitife taşındı. Kolon kümesi, sıra, etiketler ve hücre içerikleri AYNEN
// korundu; kazanılan sıralama + yeniden boyutlandırma + kalıcı genişlik.
// Δ / % / A / B artık numeric: başlıklar da hücreler gibi sağa hizalı
// (eskiden Δ ve % başlıkları solda, sayıları sağdaydı).
const DIFF_COLS: DataTableColumn<AlignedPair>[] = [
  { id: 'delta', label: 'Δ duration', sortValue: p => p.deltaNs,   numeric: true, width: 130 },
  { id: 'pct',   label: '%',          sortValue: p => p.pctChange, numeric: true, width: 85 },
  { id: 'path',  label: 'Path',       sortValue: p => p.pathLabel, naturalDir: 'asc', flex: true },
  // Tek yanı olan satırlarda süre null → sortRows onları direction'dan
  // bağımsız DİBE atıyor; hücrede basılan "—" ile aynı anlam.
  { id: 'a',     label: 'A',          sortValue: p => p.a?.duration ?? null, numeric: true, width: 110 },
  { id: 'b',     label: 'B',          sortValue: p => p.b?.duration ?? null, numeric: true, width: 110 },
];

// Sabit boş dizi — `?? []` her render'da yeni referans üretip sortedRows
// memosunu boşuna geçersiz kılardı.
const NO_PAIRS: AlignedPair[] = [];

function AlignedDiff({ aQ, bQ }: {
  aQ: ReturnType<typeof useQuery<TraceDetailResponse>>;
  bQ: ReturnType<typeof useQuery<TraceDetailResponse>>;
}) {
  const a: SpanRow[] = aQ.data?.spans ?? [];
  const b: SpanRow[] = bQ.data?.spans ?? [];

  const aligned = useMemo(() => {
    if (a.length === 0 && b.length === 0) return null;
    return alignTraces(
      a.map(s => ({
        spanId: s.spanId, parentId: s.parentSpanId ?? '',
        service: s.serviceName, name: s.name,
        startTime: s.startTime, duration: s.endTime - s.startTime,
        statusCode: s.statusCode,
      })),
      b.map(s => ({
        spanId: s.spanId, parentId: s.parentSpanId ?? '',
        service: s.serviceName, name: s.name,
        startTime: s.startTime, duration: s.endTime - s.startTime,
        statusCode: s.statusCode,
      })),
    );
  }, [a, b]);

  // Koşulsuz hook — erken dönüşlerin ÜSTÜNDE (rules-of-hooks).
  // initialSort YOK: alignTraces çiftleri MUTLAK Δ'ya göre azalan sıralı
  // döndürüyor ve bu, görünür hiçbir kolonun tek başına üretebileceği bir
  // düzen değil (işaretli Δ değil, |Δ|). id:null → satırlar geldiği gibi.
  const dt = useDataTable<AlignedPair>({
    storageKey: 'trace-compare-diff', columns: DIFF_COLS,
    rows: aligned?.pairs ?? NO_PAIRS,
  });

  if (aQ.isLoading || bQ.isLoading) return <Spinner />;
  if (!aligned || aligned.pairs.length === 0) {
    return (
      <Empty icon="↔" title="No spans to align">
        Both traces returned without spans, or one of them failed to load.
      </Empty>
    );
  }

  return (
    <div>
      <div style={{
        display: 'flex', gap: 16, fontSize: 12, color: 'var(--text2)',
        marginBottom: 10, flexWrap: 'wrap',
      }}>
        <span>{aligned.matched} matched</span>
        <span style={{ color: 'var(--warn)' }}>{aligned.onlyInA} only in A</span>
        <span style={{ color: 'var(--warn)' }}>{aligned.onlyInB} only in B</span>
        {/* v0.9.869 (tutarlılık denetimi MT3) — "click a row to focus the
            path in either waterfall" cümlesi SİLİNDİ. Satırlarda onClick
            yoktu, cursor:pointer bile yoktu: var olmayan bir etkileşim vaat
            ediliyordu. Operatör tıklar, hiçbir şey olmaz, UI'ın bozuk
            olduğunu sanar. Tıkı BAĞLAMIYORUZ (kapsam kararı) — yanlış olan
            vaatti, eksik olan özellik değil. */}
        {/* v0.9.877 (tutarlılık denetimi BT7) — bu ipucu artık yalnız
            VARSAYILAN düzende basılıyor. Tablo sıralanabilir olduğu an,
            operatör bir başlığa tıkladıktan sonra sabit duran "Sorted by
            absolute Δ desc" cümlesi yalan söylerdi. */}
        {dt.sort.id === null && (
          <span style={{ marginLeft: 'auto', color: 'var(--text3)' }}>
            Sorted by absolute Δ desc
          </span>
        )}
      </div>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(p => {
              const isOnlyA = p.a && !p.b;
              const isOnlyB = !p.a && p.b;
              const delta = p.deltaNs;
              const tone = delta == null
                ? 'var(--text3)'
                : delta > 0
                  ? 'var(--err)'
                  : delta < 0
                    ? 'var(--ok)'
                    : 'var(--text3)';
              // v0.9.236 — one row per aligned span pair across two FULL
              // traces, uncapped on either side; a 2000-span pair blocked
              // first paint. The file's own note proposed VirtualList, but
              // these rows are variable-height so content-visibility is the
              // right tool — same as TraceWaterfall on this data.
              return (
                <tr key={p.pathKey}
                  style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 28px' }}>
                  <td className="mono num" style={{ color: tone, whiteSpace: 'nowrap' }}>
                    {delta == null
                      ? '—'
                      : `${delta >= 0 ? '+' : '−'}${fmtNs(Math.abs(delta))}`}
                  </td>
                  <td className="mono num" style={{ color: tone }}>
                    {p.pctChange == null
                      ? '—'
                      : `${p.pctChange >= 0 ? '+' : ''}${(p.pctChange * 100).toFixed(0)}%`}
                  </td>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                    {p.pathLabel}
                    {isOnlyA && (
                      <span className="badge b-warn" style={{ marginLeft: 6, fontSize: 9 }}>
                        only in A
                      </span>
                    )}
                    {isOnlyB && (
                      <span className="badge b-warn" style={{ marginLeft: 6, fontSize: 9 }}>
                        only in B
                      </span>
                    )}
                  </td>
                  <td className="num mono" style={{ color: 'var(--text2)' }}>
                    {p.a ? fmtNs(p.a.duration) : '—'}
                  </td>
                  <td className="num mono" style={{ color: 'var(--text2)' }}>
                    {p.b ? fmtNs(p.b.duration) : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
