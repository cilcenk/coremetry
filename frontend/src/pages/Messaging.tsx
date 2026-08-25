import { useCallback, useEffect, useMemo } from 'react';
import { useEscLayer } from '@/lib/escLayer';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { DependenciesTable, type DepRow } from '@/components/DependenciesTable';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { encodeDestinationParam, decodeDestinationParam } from './messaging/destinationParam';
import { depRowKey } from '@/lib/depsTable';
import type { MessagingInstance, MessagingOverview } from '@/lib/types';
import { PageShell } from '@/components/ui/PageShell';

// /messaging — top-level queue / topic technologies overview.
// Kafka brokers, RabbitMQ vhosts, IBM MQ queues, NATS subjects,
// SQS / Kinesis streams — anything OTel's messaging.system
// semconv touches.
//
// useQuery + keepPreviousData: revisits land instantly, range
// changes paint the old data underneath while the new payload
// fetches. staleTime aligns with the backend's 30s cache TTL
// so most refetches hit the warm slot.
//
// v0.8.364 (Stage-2 M1):
//   • Produce/min + Consume/min + P50 columns (producer/consumer
//     split off messaging_caller_summary_5m; p50 off the existing
//     TDigest state).
//   • "Compare vs prior" toggle → TrendDelta badges (the endpoints
//     v0.5.404 pattern; opt-in, doubles the backend scan).
//   • ?destination= URL param drives the topic detail drawer
//     (URL-first house rule; replace:true, Esc/✕ clears).
export default function MessagingPage() {
  const [range, setRange] = useUrlRange('1h');
  const [params, setParams] = useSearchParams();
  // Prior-window comparison — off by default (second CH scan);
  // session-local like the endpoints toggle.
  // v0.9.399 (desen paritesi) — compare oturum state'inden URL'e
  // (?compare=prior, Endpoints deseniyle birebir): copy-link ve saved
  // view artık compare durumunu da taşıyor.
  const [mparams, setMparams] = useSearchParams();
  const compare = mparams.get('compare') === 'prior';
  const setCompare = (v: boolean) => setMparams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('compare', 'prior'); else next.delete('compare');
    return next;
  }, { replace: true });
  // Memoize on range identity — without this, a relative range
  // resolved fresh every render reshuffles the useQuery key
  // and the table refetches on every paint.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['messaging', from, to, compare],
    queryFn: () => api.messaging(from, to, compare ? 'prior' : undefined).then(r => r ?? []),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });

  // v0.8.364 — URL-first topic detail drawer. ?destination=
  // encodes the row's full identity (system|cluster|destination,
  // each field URI-encoded — see destinationParam.ts); a copied
  // link reopens the same drawer. replace:true so drawer churn
  // doesn't pile history entries.
  const destRef = useMemo(
    () => decodeDestinationParam(params.get('destination')),
    [params],
  );
  const setOpenRow = useCallback((row: DepRow | null) => {
    setParams(prev => {
      const next = new URLSearchParams(prev);
      if (row) {
        next.set('destination', encodeDestinationParam({
          system: row.system,
          cluster: row.cluster ?? '(default)',
          destination: row.destination ?? '',
        }));
      } else {
        next.delete('destination');
      }
      return next;
    }, { replace: true });
  }, [setParams]);
  // v0.9.821 — anahtar TEK NÜSHADAN (depRowKey). Elle yazılmış kopya
  // DependenciesTable'ın ürettiği anahtarla ayrışabilirdi ve ayrışsa
  // drawer HİÇ AÇILMAZDI: sessiz, tip hatası vermeyen bir kırılma.
  // (Anahtar db.name alanı kazandı; messaging satırları onu taşımadığı
  // için burada boş kalıyor ve şekil eskisiyle uyumlu.)
  const openRowKey = destRef
    ? depRowKey({
        system: destRef.system,
        cluster: destRef.cluster,
        destination: destRef.destination,
      })
    : null;

  // Esc clears the drawer param (✕ inside the drawer does the same
  // through onOpenRowChange(null)).
  // v0.9.950 (E2/Ö28) — KATMAN. Çekmecenin İÇİNDE bir popover açıkken
  // ilk Esc ona ait; çekmece ancak en üstteyken kapanır.
  useEscLayer(!!destRef, () => setOpenRow(null));

  // The backend ships raw window counts (window-independent,
  // compare-safe); the page owns the window so it derives the
  // /min rates here. Prior windows are equal-length by
  // construction, so the same divisor applies.
  const windowMins = Math.max((to - from) / 60e9, 1 / 60);
  // v0.9.813 — zarf. `?? []` HER İKİ eksikliği karşılar: yanıt yok ve
  // rows yok. Tavan bilgisi ayrı okunur — şerit tabloyu değil listeyi
  // niteliyor, bu yüzden filtrelenmiş satır sayısına DEĞİL sunucunun
  // döndürdüğü ham sayıya bakar.
  const ov = q.data as MessagingOverview | undefined;
  const list = useMemo(() => ov?.rows ?? [], [ov]);
  const rowsCapped = ov?.rowsCapped === true;
  const rowLimit = ov?.rowLimit ?? 0;
  const rows = useMemo<DepRow[]>(() => {
    return list.map((d: MessagingInstance) => {
      const hasPrior = d.priorSpanCount !== undefined;
      return {
        system: d.system,
        cluster: d.cluster,
        destination: d.destination,
        spanCount: d.spanCount,
        errorCount: d.errorCount,
        errorRate: d.errorRate,
        avgDurationMs: d.avgDurationMs,
        p99DurationMs: d.p99DurationMs,
        p50DurationMs: d.p50DurationMs,
        p95DurationMs: d.p95DurationMs,
        producePerMin: (d.produceCount ?? 0) / windowMins,
        consumePerMin: (d.consumeCount ?? 0) / windowMins,
        produceCount: d.produceCount,
        consumeCount: d.consumeCount,
        produceErrors: d.produceErrors,
        consumeErrors: d.consumeErrors,
        // v0.9.816 — gecikme ayrışması (publish vs process p95).
        produceP95Ms: d.produceP95Ms,
        consumeP95Ms: d.consumeP95Ms,
        // Prior fields only when the row had a prior twin —
        // otherwise the delta badges must stay hidden (a zeroed
        // prior would render a bogus NEW badge). omitempty on the
        // backend guarantees priorSpanCount is present iff matched.
        priorSpanCount: d.priorSpanCount,
        priorErrorCount: hasPrior ? (d.priorErrorCount ?? 0) : undefined,
        priorProducePerMin: hasPrior ? (d.priorProduceCount ?? 0) / windowMins : undefined,
        priorConsumePerMin: hasPrior ? (d.priorConsumeCount ?? 0) / windowMins : undefined,
        priorAvgMs: d.priorAvgMs,
        priorP50Ms: d.priorP50Ms,
        priorP99Ms: d.priorP99Ms,
        callers: d.callers ?? [],
      };
    });
  }, [list, windowMins]);

  return (
    <>
      <Topbar title="Messaging" range={range} onRangeChange={setRange} />
      <PageShell>
        <div style={{ marginBottom: 14, fontSize: 12, color: 'var(--text2)' }}>
          Every queue / topic the platform's services produced to or consumed from
          in the selected window. Derived from spans with a populated
          {' '}<code>messaging.system</code> attribute. {/* v0.9.256 — eski metin
          "Click a row to drill into matching traces" diyordu; satır DRAWER
          açıyor, trace'e gitmiyor. Yanlış vaat, operatörün "trace'e
          erişemiyorum" şikayetinin bir parçasıydı. */}
          Satıra tıkla → yayıncılar, tüketiciler ve uçtan uca gecikme;
          trace'ler için satırdaki destination bağlantısını kullan.
          {/* v0.9.405 — çakışan yüzey artık birbirine bağlı: consumer
              span'leri endpoint (giriş) perspektifiyle de listelenir. */}
          {' '}Consumer'ları giriş-noktası görünümünde incelemek için{' '}
          <Link to="/endpoints?entry=rpc">Endpoints · RPC →</Link>
        </div>
        {/* v0.9.405 — URL-state taşıyan sayfa görünüm kaydedebilmeli
            (saved_views şeması hazır; Endpoints emsali). */}
        <SavedViewsBar page="messaging" />
        {/* v0.9.834 — v0.9.814'ün KPI şeridi + üç grafiği KALDIRILDI
            (operatör: bu metrikler gereksiz). Sayfa tabloya döndü;
            /api/messaging/series ve okuyucusu da silindi — tüketicisi
            kalmayan bir uç, ödenen ama okunmayan bir CH taramasıdır. */}
        {q.isPending && <TableSkeleton rows={8} cols={9} wideFirst />}
        {q.isError && (
          <Empty icon="⚠" title="Couldn't load messaging overview">
            The messaging query failed. Check ClickHouse connectivity and retry —
            the range selector above re-runs the fetch.
          </Empty>
        )}
        {q.data && list.length === 0 && (
          <Empty icon="◯" title="No messaging activity in this window">
            No spans with a <code>messaging.system</code> attribute landed in the
            selected range. Widen the time range, or instrument a producer /
            consumer with the OTel messaging semconv to see queues and topics here.
          </Empty>
        )}
        {/* v0.9.813 — TAVAN ŞERİDİ. Sunucu LIMIT'e dayandığında liste
            EKSİK ve bunu söylemeyen bir tablo "estate'in tamamı bu"
            diye okunur. Şerit yalnız kesme GERÇEKTEN olduğunda çıkar;
            eylem de veriyor (arama daraltır → sunucu değil istemci
            filtresi, ama operatörün gördüğü küme netleşir). */}
        {rowsCapped && (
          <div className="badge b-warn" style={{
            display: 'block', marginBottom: 10, padding: '7px 11px',
            fontSize: 11.5, lineHeight: 1.45, textTransform: 'none', letterSpacing: 0,
          }}>
            ⚠ Yalnız en yoğun {rowLimit} destination gösteriliyor — bu pencerede
            daha fazlası var. Liste çağrı hacmine göre kesildi; aradığın topic
            listede yoksa aramayı daralt ya da pencereyi kısalt.
          </div>
        )}
        {q.data && list.length > 0 && (
          <DependenciesTable
            rows={rows}
            kind="queue"
            range={range}
            compare={compare}
            openRowKey={openRowKey}
            onOpenRowChange={setOpenRow}
            extraControls={
              <label style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}
                title="Compare current window against the immediately-preceding equal-length window. Adds a second backend scan; off by default.">
                <input type="checkbox"
                  checked={compare}
                  onChange={e => setCompare(e.target.checked)} />
                Compare vs prior
              </label>
            } />
        )}
      </PageShell>
    </>
  );
}
