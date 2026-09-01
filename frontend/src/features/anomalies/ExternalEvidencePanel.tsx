// ExternalEvidencePanel — v0.10.230 (Influx D5, audit §4 UI).
//
// kind=external problemde RootCausePanel'in yerine geçer: topoloji/servis
// tabanlı analiz yok, D4'ün root_cause_hypotheses(anchor=problem)'e
// yazdığı kanıt zinciri çizilir — metrik şeridi (metric_points `ext:*`
// serisi, TimeChart/uPlot), trace tablosu, pod tablosu, log imzaları.
// Hepsi mevcut atomlarla (useDataTable, badge, Spinner/Empty); yeni
// primitive yok. Hipotez /rootcause zarfından okunur (aynı FINAL satır).
// Fetch yalnız bu sayfa açıkken; polling yok (kanıt 5 dk'da bir güncellenir).

import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { TimeChart } from '@/components/charts/TimeChart';
import { fmtDateTime } from '@/lib/utils';
import { fmtDur } from '@/components/traces/shared';
import { traceHref } from '@/lib/traceHref';
import type { Problem, PodHit, LogSignature } from '@/lib/types';
import { traceRows, evidenceCounts, pickSeries, labelKeys, labelValues, toChart, sevTone, type ExternalTraceRow } from './externalEvidence';

const BASELINE_LOOKBACK_NS = 3 * 3600e9;

const TRACE_COLS: DataTableColumn<ExternalTraceRow>[] = [
  { id: 'time', label: 'Zaman', width: 176, sortValue: r => r.startNs, numeric: true },
  { id: 'trace', label: 'Trace', width: 150 },
  { id: 'root', label: 'Kök servis · op', width: 260, sortValue: r => `${r.rootService ?? ''} ${r.rootOp ?? ''}` },
  { id: 'err', label: 'Hata servis · op', width: 260, sortValue: r => `${r.errorService ?? ''} ${r.errorOp ?? ''}` },
  { id: 'dur', label: 'Süre', width: 90, numeric: true, sortValue: r => r.durationNs },
  { id: 'spans', label: 'Span', width: 70, numeric: true, sortValue: r => r.spans },
  { id: 'status', label: 'Durum', width: 100, sortValue: r => (r.missing ? 2 : r.errorSpans > 0 ? 0 : 1), numeric: true },
];

const POD_COLS: DataTableColumn<PodHit>[] = [
  { id: 'pod', label: 'Pod (INSTANCEID)', width: 320, sortValue: r => r.pod },
  { id: 'count', label: 'Satır', width: 90, numeric: true, sortValue: r => r.count },
  { id: 'last', label: 'Son görülme', width: 176, numeric: true, sortValue: r => r.lastSeenNs },
];

const SIG_COLS: DataTableColumn<LogSignature>[] = [
  { id: 'sev', label: 'Şiddet', width: 90, sortValue: r => r.severity },
  { id: 'tpl', label: 'İmza (örnek mesaj başlıkta)', width: 520, sortValue: r => r.template },
  { id: 'count', label: 'Kayıt', width: 80, numeric: true, sortValue: r => r.count },
  { id: 'traces', label: 'Trace', width: 80, numeric: true, sortValue: r => r.traceCount },
];

export function ExternalEvidencePanel({ problem, window: win }: {
  problem: Problem;
  window?: { fromNs: number; toNs: number };
}) {
  const rc = useQuery({
    queryKey: ['problem-rootcause', problem.id],
    queryFn: () => api.problemRootCause(problem.id),
    staleTime: 30_000,
  });
  const deep = rc.data?.hypothesis?.deep;
  const ext = deep?.external;
  const keys = useMemo(() => labelKeys(ext?.labels), [ext?.labels]);
  const values = useMemo(() => labelValues(ext?.labels), [ext?.labels]);

  // Şerit penceresi: problem penceresi + 3 saat baseline geriye. probWindow
  // basamaklı (problemWindowNs) — anahtar her render değişmez.
  const range = useMemo(() => {
    const to = win?.toNs ?? ext?.windowToNs ?? problem.startedAt;
    const anchor = Math.min(win?.fromNs ?? to, ext?.windowFromNs ?? to, problem.startedAt);
    return { from: anchor - BASELINE_LOOKBACK_NS, to };
  }, [win?.fromNs, win?.toNs, ext?.windowFromNs, ext?.windowToNs, problem.startedAt]);

  const mq = useQuery({
    queryKey: ['ext-evidence-series', ext?.source, ext?.query, values, range.from, range.to],
    enabled: !!ext,
    staleTime: 30_000,
    queryFn: ({ signal }) => api.metricQueryFull({
      name: `ext:${ext!.query}`,
      service: ext!.source,
      filters: JSON.stringify(keys.map((k, i) => ({ k, op: '=', v: [values[i]] }))),
      agg: 'max',
      step: 60,
      from: range.from,
      to: range.to,
    }, signal),
  });
  const chart = useMemo(() => toChart(pickSeries(mq.data?.series ?? null, [])), [mq.data]);

  const tRows = useMemo(() => traceRows(deep), [deep]);
  const pods = deep?.affectedPods ?? [];
  const sigs = deep?.logSignatures ?? [];
  const dtT = useDataTable<ExternalTraceRow>({ storageKey: 'ext-evidence-traces', columns: TRACE_COLS, rows: tRows });
  const dtP = useDataTable<PodHit>({ storageKey: 'ext-evidence-pods', columns: POD_COLS, rows: pods });
  const dtS = useDataTable<LogSignature>({ storageKey: 'ext-evidence-sigs', columns: SIG_COLS, rows: sigs });

  if (rc.isPending) return <Spinner />;
  if (rc.error) {
    return <div style={{ fontSize: 12, color: 'var(--err)' }}>Kanıt zinciri yüklenemedi — {String(rc.error)}</div>;
  }
  if (!ext) {
    return (
      <Empty icon="⏳" title="Kanıt henüz toplanmadı">
        Dış kaynak anomalisi açıldığında kanıt (SORGU 2 → trace/pod/log) ilk poll'da toplanır ve 5 dk'da bir tazelenir.
        Kaynağın enrich sorgusu tanımlı değilse yalnız metrik kanıtı gelir.
      </Empty>
    );
  }
  const c = evidenceCounts(deep);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      {/* 1. Metrik kararı */}
      <div>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap' }}>
          <span className="pb-headline" style={{ fontSize: 24 }}>{Math.round(ext.current)}</span>
          <span className="mono" style={{ color: 'var(--text3)', fontSize: 13 }}>
            / baseline {ext.median.toFixed(1)} · MAD {ext.mad.toFixed(2)} · {ext.z.toFixed(1)}σ
          </span>
          <span className="mono" style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 'auto' }}>
            {ext.source} · {ext.query} · güncellendi {fmtDateTime(new Date(ext.updatedNs / 1e6))}
          </span>
        </div>
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginTop: 6 }}>
          {keys.map((k, i) => (
            <span key={k} className="pb-pill"><span className="mono">{k}={values[i]}</span></span>
          ))}
        </div>
        <div style={{ marginTop: 8 }}>
          {mq.isPending ? <Spinner /> : chart ? (
            <TimeChart
              times={chart.times}
              series={[{ key: 'ext', label: ext.query, data: chart.data, color: 'var(--err)', type: 'line' }]}
              height={140}
              leftUnit=""
              hideLegend
            />
          ) : (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>Seri okunamadı — metric_points'te bu etiket kümesi için nokta yok.</div>
          )}
        </div>
      </div>

      {/* 2. Dürüstlük şeridi */}
      <div style={{ fontSize: 12, color: 'var(--text2)' }}>
        SORGU 2: {c.rows} satır · {c.traces} trace id{c.invalid > 0 ? ` (${c.invalid} geçersiz düşürüldü)` : ''} · {c.withSpans} trace CH'de bulundu · {c.pods} pod · {c.signatures} log imzası
        {(ext.notes ?? []).length > 0 && (
          <ul style={{ margin: '4px 0 0 16px', padding: 0, color: 'var(--text3)' }}>
            {ext.notes!.map((n, i) => <li key={i}>{n}</li>)}
          </ul>
        )}
      </div>

      {/* 3. Trace'ler */}
      <EvidenceBlock title="İlgili trace'ler" count={tRows.length}>
        {tRows.length === 0 ? <Muted>Trace kanıtı yok.</Muted> : (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dtT} />
              <DataTableHead dt={dtT} />
              <tbody>
                {dtT.sortedRows.map(r => (
                  <tr key={r.traceId}>
                    <td className="mono">{r.startNs > 0 ? fmtDateTime(new Date(r.startNs / 1e6)) : '—'}</td>
                    <td className="mono"><Link to={traceHref(r.traceId)} className="sec" title={r.traceId}>{r.traceId.slice(0, 12)}…</Link></td>
                    <td title={`${r.rootService ?? ''} ${r.rootOp ?? ''}`} style={ellipsis}>{r.rootService ? <>{r.rootService} <span style={{ color: 'var(--text3)' }}>· {r.rootOp}</span></> : '—'}</td>
                    <td title={`${r.errorService ?? ''} ${r.errorOp ?? ''}`} style={ellipsis}>{r.errorService ? <>{r.errorService} <span style={{ color: 'var(--text3)' }}>· {r.errorOp}</span></> : '—'}</td>
                    <td className="num mono">{r.durationNs > 0 ? fmtDur(r.durationNs / 1e6) : '—'}</td>
                    <td className="num">{r.spans || '—'}</td>
                    <td>{r.missing
                      ? <span className="badge b-gray" title="Trace id Influx'tan geldi ama bu pencerede CH'de span'i yok (retention ya da henüz gelmedi)">CH'de yok</span>
                      : <span className={`badge ${r.errorSpans > 0 ? 'b-err' : 'b-ok'}`}>{r.errorSpans > 0 ? `${r.errorSpans} hata` : 'ok'}</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </EvidenceBlock>

      {/* 4. Pod'lar */}
      <EvidenceBlock title="Etkilenen pod'lar" count={pods.length}>
        {pods.length === 0 ? <Muted>Pod kanıtı yok (SORGU 2 INSTANCEID döndürmedi).</Muted> : (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dtP} />
              <DataTableHead dt={dtP} />
              <tbody>
                {dtP.sortedRows.map(r => (
                  <tr key={r.pod}>
                    <td className="mono" title={r.pod} style={ellipsis}>{r.pod}</td>
                    <td className="num">{r.count}</td>
                    <td className="mono">{r.lastSeenNs > 0 ? fmtDateTime(new Date(r.lastSeenNs / 1e6)) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </EvidenceBlock>

      {/* 5. Log imzaları */}
      <EvidenceBlock title="Log imzaları (WARN+)" count={sigs.length}>
        {sigs.length === 0 ? <Muted>Log imzası yok.</Muted> : (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dtS} />
              <DataTableHead dt={dtS} />
              <tbody>
                {dtS.sortedRows.map(r => (
                  <tr key={r.hash}>
                    <td><span className={`badge ${sevTone(r.severity)}`}>{r.severity || '—'}</span></td>
                    <td className="mono" title={r.sample} style={ellipsis}>{r.template}</td>
                    <td className="num">{r.count}</td>
                    <td className="num">{r.traceCount}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </EvidenceBlock>
    </div>
  );
}

const ellipsis = { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const;

function Muted({ children }: { children: React.ReactNode }) {
  return <div style={{ fontSize: 12, color: 'var(--text3)' }}>{children}</div>;
}

function EvidenceBlock({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return (
    <div>
      <div style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: 0.4, textTransform: 'uppercase', color: 'var(--text3)', marginBottom: 6 }}>
        {title} <span className="mono" style={{ fontWeight: 400 }}>· {count}</span>
      </div>
      {children}
    </div>
  );
}
