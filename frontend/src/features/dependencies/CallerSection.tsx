// CallerSection.tsx — v0.10.575. `DetailDrawer.tsx`ten AYNEN çıkarıldı
// (davranış bayt-bayt korunuyor); yeni olan yalnız iki OPSİYONEL prop.
//
// NEDEN ÇIKARILDI: /messaging/topic detay sayfası aynı (servis, pod) kırılımını
// çiziyor. Nüsha almak, `deps-callers-*` kolon sözleşmesini iki gövdeye
// yaymak olurdu — ve bu ailenin geçmişi tam olarak bunu söylüyor: aynı tablonun
// dört nüshası `Impact` kolonunu iki AYRI anlamda taşıyor
// (`pages/databases/detailSections.tsx` başlığındaki itiraf). Beşincisi
// doğmadan tek eve çekildi.
//
// ÇEKMECE DAVRANIŞI DEĞİŞMEDİ, ve bu bir iddia değil ölçü: `storageKey`
// verilmezse eskisi gibi `deps-callers-${tone}`, `showReset` verilmezse
// "Reset columns" düğmesi YOK. Çekmece iki propu da geçmiyor.
import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { DBCallerBreakdown, TimeRange } from '@/lib/types';
import { serviceHref } from '@/lib/serviceHref';
import { podDetailPath } from '@/pages/service/podDetailPath';
import { encodeRange } from '@/lib/urlState';
import { fmtNum } from '@/lib/utils';

// CallerSection renders one labelled table of (service, pod) rows
// with their RED metrics. Tone colours the header so producers /
// consumers / DB clients read at a glance. Empty sections render
// a one-line placeholder so the operator sees that we did look
// for the data and there just isn't any in this window.
//
// Sort headers added in v0.4.92 — sorting is client-side because
// the backend caps the result at LIMIT 100, so a 100-row sort
// is O(n log n) ≈ 700 comparisons on the operator's machine.
// At enterprise scale (5k+ pods) the same cap protects: the
// drawer always shows the top-100-by-calls cohort and the
// operator sorts within that page, no full-fleet re-fetch
// trip. Default sort is Calls desc so the heaviest caller
// keeps surfacing on first paint.
//
// v0.7.x — the last bespoke sort-header variant in the app
// retired: these nested tables now ride the shared useDataTable
// primitive (same depCols + DataTableHead/DataTableColgroup
// contract the page-level table above uses). The client-side
// search filter is preserved and feeds filtered rows into the
// primitive; sort + column-resize layout persist per-tone.
export function CallerSection({ title, rows, emptyMessage, tone, range, storageKey, showReset }: {
  title: string;
  rows: DBCallerBreakdown[];
  emptyMessage: string;
  tone: 'producer' | 'consumer' | 'other' | 'db';
  // v0.9.967 — the drawer's own window, threaded down purely so the caller
  // pill can carry it. These rows were COMPUTED over this window; opening
  // the service on a different one contradicts the numbers just read.
  range: TimeRange;
  /**
   * v0.10.575 — düzen anahtarını ÇAĞIRAN belirleyebilir. Varsayılan eski
   * `deps-callers-${tone}`. Sayfa kendi anahtarını (`msg-topic-producers`)
   * veriyor: çekmece ve sayfa aynı tonu paylaşıyor ama farklı genişliklerde
   * yaşıyorlar (sayfa geniş, çekmece dar) — tek anahtar altında biri
   * diğerinin sürüklediği genişlikleri miras alırdı.
   */
  storageKey?: string;
  /** v0.10.575 — sayfada "Reset columns" görünür; çekmecede (varsayılan) YOK. */
  showReset?: boolean;
}) {
  type Caller = DBCallerBreakdown;
  const dotColor =
    tone === 'producer' ? 'var(--accent2)' :
    tone === 'consumer' ? 'var(--ok)' :
    tone === 'other'    ? 'var(--text3)' :
                          'var(--accent2)';
  const hasRole = rows.some(r => r.role);
  // Client-side search across service / pod / role so an
  // operator with hundreds of pods hitting one DB can pinpoint
  // a specific instance fast. Backend caps the result set at
  // LIMIT 500 (v0.5.12) so filtering 500 rows stays
  // sub-millisecond.
  const [search, setSearch] = useState('');
  const filtered = useMemo(() => {
    const t = search.trim().toLowerCase();
    if (!t) return rows;
    return rows.filter(r =>
      r.service.toLowerCase().includes(t) ||
      r.pod.toLowerCase().includes(t) ||
      (r.role ?? '').toLowerCase().includes(t));
  }, [rows, search]);

  // Caller columns mirror the prior CallerSortTh sort keys +
  // CALLER_NATURAL directions. Role is conditional on hasRole
  // (messaging-only). Default sort = Calls desc (preserved).
  const callerCols = useMemo<DataTableColumn<Caller>[]>(() => [
    { id: 'service', label: 'Service',    sortValue: r => r.service, naturalDir: 'asc', width: 200 },
    { id: 'pod',     label: 'Pod / host', sortValue: r => r.pod,     naturalDir: 'asc', width: 200 },
    ...(hasRole
      ? [{ id: 'role', label: 'Role', sortValue: (r: Caller) => r.role ?? '', naturalDir: 'asc', width: 110 } as DataTableColumn<Caller>]
      : []),
    { id: 'calls',   label: 'Calls', sortValue: r => r.spanCount,     numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'errRate', label: 'Err %', sortValue: r => r.errorRate,     numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'avg',     label: 'Avg',   sortValue: r => r.avgDurationMs, numeric: true, naturalDir: 'desc', width: 84 },
    // v0.9.273 — P50, and v0.9.263 P95, on the shared DBCallerBreakdown. BOTH
    // producer queries project them; if only one did, the other drawer would
    // render a hard 0.0ms here. Order matches the aggregate strip above:
    // Avg → P50 → P95 → P99.
    { id: 'p50',     label: 'P50',   sortValue: r => r.p50DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p95',     label: 'P95',   sortValue: r => r.p95DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p99',     label: 'P99',   sortValue: r => r.p99DurationMs, numeric: true, naturalDir: 'desc', width: 84 },
  ], [hasRole]);

  // Distinct storageKey per tone so the Publishers / Consumers /
  // Other / DB instances rendered side-by-side don't share (and
  // clobber) each other's sort + width layout.
  const dt = useDataTable<Caller>({
    storageKey: storageKey ?? `deps-callers-${tone}`,
    columns: callerCols,
    rows: filtered,
    initialSort: { id: 'calls', dir: 'desc' },
  });

  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8,
        fontSize: 12, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
      }}>
        <span aria-hidden style={{
          width: 8, height: 8, borderRadius: 2, background: dotColor,
        }} />
        {title}
        {rows.length > 10 && (
          <input value={search} onChange={e => setSearch(e.target.value)}
            placeholder="Search service / pod / role…"
            style={{
              marginLeft: 'auto', fontSize: 11, padding: '3px 8px',
              width: 200, fontWeight: 400,
            }} />
        )}
        {search && (
          <span style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 400 }}>
            {dt.sortedRows.length} of {rows.length}
          </span>
        )}
        {/* Arama kutusu zaten `margin-left:auto` alıyor; İKİ auto marj boş
            alanı BÖLÜŞÜR ve kutuyu ortaya iterdi. Bu yüzden itme yalnız
            kutunun olmadığı hâlde (≤10 satır). */}
        {showReset && (
          <span style={{ marginLeft: rows.length > 10 ? undefined : 'auto' }}>
            <ResetLayoutButton dt={dt} />
          </span>
        )}
      </div>
      {rows.length === 0 ? (
        emptyMessage && <div style={{ fontSize: 12, color: 'var(--text3)' }}>{emptyMessage}</div>
      ) : (
        // v0.7.x — adopting the shared primitive retires the inner-scroll
        // wrapper (its sticky <thead> went with the bespoke header). Rows
        // are capped at LIMIT 500 server-side; content-visibility:auto on
        // each row lets the browser skip off-screen ones per CLAUDE.md's
        // "tables > 100 rows" guidance, matching the Service.tsx (v0.7.54)
        // adopter pattern.
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((c, i) => {
                const errCls = c.errorRate > 5 ? 'err' : c.errorRate > 0 ? 'warn' : 'ok';
                return (
                  <tr key={`${c.service}|${c.pod}|${c.role ?? ''}|${i}`}
                      style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                    <td>
                      <Link to={serviceHref(c.service, { range })}
                            style={{ fontFamily: 'monospace', fontSize: 12 }}>
                        {c.service}
                      </Link>
                    </td>
                    <td style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text2)' }}>
                      {/* v0.10.551 — pod hücresi pivot (audit E11): host_name = pod adı;
                          bilinmeyen/boş pod düz metin kalır. */}
                      {c.pod && c.pod !== '(unknown)' ? (
                        <Link to={podDetailPath({ pod: c.pod, service: c.service, range: encodeRange(range) || null })}
                              style={{ color: 'inherit' }} title="Pod detayı">
                          {c.pod}
                        </Link>
                      ) : c.pod}
                    </td>
                    {hasRole && (
                      <td>
                        {c.role && <RoleBadge role={c.role} />}
                      </td>
                    )}
                    <td className="num mono">{fmtNum(c.spanCount)}</td>
                    <td className="num mono">
                      <span className={`badge b-${errCls}`} style={{ fontSize: 9 }}>
                        {c.errorRate.toFixed(2)}%
                      </span>
                    </td>
                    <td className="num mono">{c.avgDurationMs.toFixed(1)}ms</td>
                    <td className="num mono">
                      {c.p50DurationMs === undefined
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : <>{c.p50DurationMs.toFixed(1)}ms</>}
                    </td>
                    <td className="num mono">
                      {c.p95DurationMs === undefined
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : <>{c.p95DurationMs.toFixed(1)}ms</>}
                    </td>
                    <td className="num mono">{c.p99DurationMs.toFixed(1)}ms</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function RoleBadge({ role }: { role: string }) {
  const r = role.toLowerCase();
  const tone =
    r === 'producer' ? { bg: 'color-mix(in srgb, var(--accent) 15%, transparent)', fg: 'var(--accent2)' } :
    r === 'consumer' ? { bg: 'color-mix(in srgb, var(--ok) 15%, transparent)', fg: 'var(--ok)' } :
    r === 'client'   ? { bg: 'var(--bg3)',            fg: 'var(--text2)' } :
                       { bg: 'var(--bg3)',            fg: 'var(--text3)' };
  return (
    <span style={{
      fontSize: 10, padding: '1px 6px', borderRadius: 3, fontWeight: 600,
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      background: tone.bg, color: tone.fg,
      textTransform: 'uppercase', letterSpacing: '.5px',
    }}>{role}</span>
  );
}
