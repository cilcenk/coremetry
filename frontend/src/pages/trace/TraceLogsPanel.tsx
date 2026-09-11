import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Spinner, Empty } from '@/components/Spinner';
import { LogTable } from '@/components/LogTable';
import { traceServicesWithoutTraceField } from '@/lib/traceEventLogs';
import type { LogRow } from '@/lib/types';

// pages/trace/TraceLogsPanel.tsx — v0.10.675 (trace kiosk modu Dilim 4):
// Trace.tsx'ten AYNEN taşındı (davranış değişmedi) — TraceKiosk sayfası
// aynı paneli çizer; iki kopya olmasın. Gövde ve yorumlar orijinal.

// TraceLogsPanel — flat list of log entries that share this trace
// id, ordered chronologically. Layout mirrors Uptrace's trace→logs
// tab: timestamp · severity · service · message preview, with the
// span_id shown as a smaller tag so the operator can correlate
// "this log line belongs to that span".
// v0.8.332 — `degraded` (a reason string) marks the pivot Phase 2 partial-
// result contract: warn chip instead of the "no logs" empty state (which
// would misread as an instrumentation gap), table still renders, tab never
// blocks.
export function TraceLogsPanel({ logs, degraded, logsTotal, eventRows, oracleRows, oracleError, hiddenGrpcMsgs, showGrpcMsgs, onToggleGrpcMsgs, traceServices }: {
  logs: LogRow[] | null | undefined;
  degraded?: string | null;
  // v0.9.461 (dürüstlük A5) — sunucunun gerçek toplamı ("ilk N / M").
  logsTotal?: number;
  // v0.8.407 — pseudo rows from the trace's own span events (zero-ES
  // leg): merged into the list, shown alone when the backend has
  // nothing / fails, so the tab is useful even without shipped logs.
  //
  // v0.10.577 — bu liste ARTIK SÜZÜLMÜŞ gelir (gRPC SENT/RECEIVED gizli).
  // Ham liste sayfada kalır ve waterfall çiplerini beslemeye devam eder.
  eventRows: LogRow[];
  // v0.10.602 — Oracle hata tablosu satırları (origin 'oracle'), üçüncü dizi.
  // Boş dizi = satır yok YA DA kaynak yok; ikisi de listeyi değiştirmez.
  oracleRows: LogRow[];
  oracleError?: boolean;
  hiddenGrpcMsgs: number;
  showGrpcMsgs: boolean;
  onToggleGrpcMsgs: () => void;
  traceServices: string[];
}) {
  // v0.10.577 — gizlenen event'leri geri getiren çip. Sayfanın kendi dili
  // (.facet + aria-pressed + klavye dalı); emsali "Critical path focus".
  // Yalnız gizlenen VARSA ya da tercih açıkken çizilir — hiçbir gRPC event'i
  // olmayan bir trace'te ekranda anlamsız bir düğme durmasın.
  const grpcChip = (hiddenGrpcMsgs > 0 || showGrpcMsgs) ? (
    <span className={'facet' + (showGrpcMsgs ? ' on' : '')}
      role="button" tabIndex={0} aria-pressed={showGrpcMsgs}
      title="Bilgi taşımayan span event'leri: gRPC SENT/RECEIVED mesajları ve hiç attribute taşımayan işaretçiler (redis.encode.start gibi). ERROR ve üstü asla gizlenmez."
      onClick={onToggleGrpcMsgs}
      onKeyDown={e => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onToggleGrpcMsgs(); }
      }}>
      Gürültü event'leri
      {hiddenGrpcMsgs > 0 && <span className="n">{hiddenGrpcMsgs}</span>}
    </span>
  ) : null;

  if (logs === undefined) return <Spinner />;
  if (logs === null) {
    // Backend errored — the span events still tell part of the story.
    if (eventRows.length > 0 || oracleRows.length > 0) {
      return (
        <>
          <div style={{ display: 'flex', gap: 10, alignItems: 'center', padding: '0 10px 6px' }}>
            <span className="badge b-err">⚠ Failed to load logs — showing the trace's span events only</span>
            {/* v0.10.577 — bu dalda da çip lazım: gizlenen event'ler
                yüzünden liste boş görünebilir ve operatörün geri getirecek
                hiçbir yolu kalmazdı. */}
            {grpcChip}
          </div>
          <LogTable logs={[...eventRows, ...oracleRows].sort((a, b) => a.timestamp - b.timestamp)} hideTraceColumn />
        </>
      );
    }
    return <Empty icon="⚠" title="Failed to load logs" />;
  }
  if (logs.length === 0 && eventRows.length === 0 && oracleRows.length === 0 && !degraded) {
    // v0.10.577 — gizlenmiş gRPC event'i varken "hiç log yok" teşhisi YALAN
    // olur: liste boş değil, süzülmüş. Çipi göster, teşhisi gösterme.
    if (hiddenGrpcMsgs > 0) {
      return (
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', padding: '6px 10px', fontSize: 11, color: 'var(--text3)' }}>
          <span>{hiddenGrpcMsgs} gürültü event'i gizlendi, başka log satırı yok</span>
          {grpcChip}
        </div>
      );
    }
    return <TraceLogsEmptyDiagnostics traceServices={traceServices} />;
  }
  // Ascending chronological order — lines up with the trace
  // waterfall above. Display reuses the shared <LogTable> so
  // severity colouring / row expand layout / attribute tables
  // stay consistent with the /logs page; operators don't
  // re-learn a second viewer when they drill in from a trace.
  const sorted = [...logs, ...eventRows, ...oracleRows].sort((a, b) => a.timestamp - b.timestamp);
  return (
    <>
      {degraded && (
        <div style={{ padding: '0 10px 6px' }}>
          <span className="badge b-warn" title={degraded}>
            ⚠ Log backend slow/unreachable — partial results
          </span>
        </div>
      )}
      <div style={{
        display: 'flex', gap: 10, alignItems: 'center', padding: '6px 10px',
        fontSize: 11, color: 'var(--text3)',
      }}>
        <span>
          {/* v0.9.461 (dürüstlük A5) — sunucu toplamı sayfadan büyükse
              "ilk N / M": limit'e çarpan sayfa tam envanter değil.
              v0.10.577 — metnin tamamı Türkçe: iki dal iki DİLDEYDİ, yani
              ekran verinin durumuna göre dil değiştiriyordu. */}
          {logsTotal && logsTotal > logs.length
            ? `ilk ${logs.length} / ${logsTotal.toLocaleString()} log satırı`
            : `${logs.length} log satırı`}
          {eventRows.length > 0 && ` + ${eventRows.length} span event'i`}
          {oracleRows.length > 0 && ` + ${oracleRows.length} Oracle satırı`}
          {oracleError && ' (Oracle satırları yüklenemedi)'}
          {hiddenGrpcMsgs > 0 && ` (${hiddenGrpcMsgs} gürültü event'i gizlendi)`}
        </span>
        {grpcChip}
      </div>
      <LogTable logs={sorted} hideTraceColumn />
    </>
  );
}

// TraceLogsEmptyDiagnostics (v0.8.407, D leg) — "0 log" has two very
// different causes and the old static hint couldn't tell them apart:
// the services genuinely logged nothing, or the shipper writes logs
// WITHOUT a trace field (correlation broken → every trace's tab is
// empty). The logstore trace-context report (field_caps + sampled
// coverage, 5-min server cache; viewer-safe route) names the broken
// services so the operator gets an actionable answer. Fetched ONLY
// when the tab is open AND empty — never on the hot path.
function TraceLogsEmptyDiagnostics({ traceServices }: { traceServices: string[] }) {
  const q = useQuery({
    queryKey: ['logstore', 'trace-context'],
    queryFn: () => api.logstoreTraceContext(),
    staleTime: 300_000, // matches the server's 5-min serveCached TTL
    retry: false,
  });
  const rep = q.data?.report;
  const broken = rep?.available
    ? traceServicesWithoutTraceField(traceServices, rep.services ?? [])
    : [];
  return (
    <Empty icon="≡" title="No logs for this trace">
      {broken.length > 0 ? (
        <>
          The log backend HAS log lines for{' '}
          <b>{broken.join(', ')}</b> — but none of them carry a trace
          field, so trace→log correlation can never match. Ship logs
          with the W3C trace context (trace.id / trace_id) populated,
          e.g. via the OTel Collector's log pipeline or your logging
          library's OTel appender.
        </>
      ) : rep?.available && !rep.pivotReady ? (
        <>
          The log backend's trace field looks unusable
          (field: <code>{rep.effectiveField || 'none'}</code>, type:{' '}
          <code>{rep.effectiveType || 'absent'}</code>). Check
          Settings → Elasticsearch → field mapping.
        </>
      ) : (
        <>Make sure your collector ships logs with the W3C trace context (trace_id + span_id) populated.</>
      )}
    </Empty>
  );
}
