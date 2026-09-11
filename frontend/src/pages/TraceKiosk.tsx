import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { CopyButton } from '@/components/CopyButton';
import { SvcBadge } from '@/components/traces/shared';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { TraceLogsPanel } from './trace/TraceLogsPanel';
import { KioskSpanPanel } from './trace/KioskSpanPanel'; // v0.10.681 — alt span detayı
import { useEscLayer } from '@/lib/escLayer';
import { pickRootSpan, bundleLogsState, KIOSK_LOG_LIMIT_DEFAULT, KIOSK_LOG_LIMIT_MAX } from './trace/kioskModel';
import { useTraceBundle } from '@/lib/queries';
import { perSpanLogSignals, spanEventLogRows, splitGrpcMessageEvents } from '@/lib/traceEventLogs';
import { STORAGE_KEYS, getRaw, setRaw } from '@/lib/storage';
import { spanHasError } from '@/lib/otel';
import { fmtNs, tsLong, displaySpanName } from '@/lib/utils';
import { logsHref } from '@/lib/logsUrl';
import { useBranding } from '@/lib/branding';
import { TelescopeIcon } from '@/components/TelescopeIcon';
import { Wordmark } from '@/components/Wordmark';

// TraceKiosk — v0.10.675 (trace kiosk modu Dilim 4; audit §2.4 yol B, §9).
//
// /trace?id=…&kiosk=1: kromsuz tam ekran şelale + loglar, yeni pencerede
// açılmak için. Kabuk dalı AppShell'de (v0.10.673: Sidebar / CopilotChat /
// kısayollar / Toaster mount edilmez, /api/events aboneliği kapalı). Veri
// TEK istekte (useTraceBundle → /api/traces/{id}/bundle): log/Oracle
// penceresi SUNUCUDA span'lerden kurulur, istemci penceresiz istek atmaz.
//
// Trace.tsx'e dokunulmadı (yalnız ?kiosk=1 dalı). Bu sayfa Topbar,
// breadcrumb, eylem şeridi, AI/dış-link düğmeleri ve SpanDetail ÇİZMEZ
// (v1 — audit §11 soru 2: şelale + loglar). Seçili span (?span=) yalnız
// vurgulanır ve URL'e yazılır; yazıcı Trace.tsx'inki gibi yalnız kendi
// param'ına dokunur (kiosk=1, id, range korunur).
export function TraceKiosk() {
  const [searchParams] = useSearchParams();
  const id = searchParams.get('id') ?? '';
  // v0.10.679 (operatör: "paylaşım görünümündeki gibi Coremetry adı ve logo da
  // olsa") — marka şeridi; özel marka (Settings → Branding) varsa onun
  // logosu/adı, yoksa OTel işareti + iki tonlu Wordmark (PublicTrace/Sidebar
  // ile aynı öğeler).
  const brand = useBranding();
  const [selectedId, setSelectedId] = useState<string | null>(() => searchParams.get('span'));
  const [revealSpanId] = useState<string | null>(() => searchParams.get('span'));
  // "Daha fazla" = limit 500 → 1000 (sunucu tavanı); anahtar değişir, eski
  // sayfa cache'te kalır; ötesi /logs bağlantısı (audit §9).
  const [logLimit, setLogLimit] = useState(KIOSK_LOG_LIMIT_DEFAULT);
  const bundleQ = useTraceBundle(id || undefined, { logLimit });
  const bundle = bundleQ.data;
  const spans = useMemo(() => bundle?.spans ?? [], [bundle]);
  const analysis = bundle?.analysis;
  const root = useMemo(() => pickRootSpan(spans, analysis), [spans, analysis]);
  // v0.10.681 — seçili span'in alt paneli (Tempo düzeni); Esc kapatır.
  const selectedSpan = useMemo(
    () => (selectedId ? spans.find(s => s.spanId === selectedId) ?? null : null),
    [spans, selectedId]);
  useEscLayer(!!selectedSpan, () => setSelectedId(null));

  useEffect(() => {
    if (typeof window === 'undefined' || !id) return;
    const url = new URL(window.location.href);
    if (selectedId) url.searchParams.set('span', selectedId);
    else url.searchParams.delete('span');
    window.history.replaceState({}, '', url.toString());
  }, [selectedId, id]);

  useEffect(() => {
    document.title = root ? `${displaySpanName(root)} · Trace kiosk` : 'Trace kiosk';
  }, [root]);

  // Span event'leri (sıfır-ES bacağı) + gRPC gürültü süzgeci — Trace.tsx ile
  // aynı tercih anahtarı (küresel, trace başına değil).
  const eventRows = useMemo(() => spanEventLogRows(spans), [spans]);
  const [showGrpcMsgs, setShowGrpcMsgs] = useState(
    () => getRaw(STORAGE_KEYS.traceShowGrpcMsgs) === '1');
  const toggleGrpcMsgs = () => setShowGrpcMsgs(v => {
    setRaw(STORAGE_KEYS.traceShowGrpcMsgs, v ? '0' : '1');
    return !v;
  });
  const { visible: shownEventRows, hidden: hiddenGrpcMsgs } = useMemo(
    () => (showGrpcMsgs ? { visible: eventRows, hidden: 0 } : splitGrpcMessageEvents(eventRows)),
    [eventRows, showGrpcMsgs]);
  const logsState = useMemo(() => bundleLogsState(bundle, bundleQ.isError), [bundle, bundleQ.isError]);
  const logSignals = useMemo(
    () => perSpanLogSignals(logsState.logs ?? undefined, eventRows),
    [logsState.logs, eventRows]);

  if (!id) return <Empty icon="?" title="Trace id yok" />;
  if (bundleQ.isLoading) return <Spinner />;
  if (bundleQ.isError || !bundle) return <Empty icon="⚠" title="Failed to load trace" />;
  if (spans.length === 0) {
    return bundle.source === 'mv_only'
      ? <Empty icon="≡" title="Trace aged out of raw spans">Ham span'ler saklama süresini aştı; yalnız 5 dk özet kaldı.</Empty>
      : <Empty icon="≡" title="Trace not found" />;
  }

  // Trace.tsx:397-408 ile aynı türetimler; spread yerine döngü (50k span).
  let minT = Infinity;
  let maxT = -Infinity;
  for (const s of spans) {
    if (s.startTime > 0 && s.startTime < minT) minT = s.startTime;
    if (s.endTime > maxT) maxT = s.endTime;
  }
  const totalNs = Number.isFinite(minT) && Number.isFinite(maxT) ? Math.max(0, maxT - minT) : 0;
  const hasErr = spans.some(s => spanHasError(s));
  const errSpans = spans.filter(s => spanHasError(s)).length;
  const svcCount = new Set(spans.map(s => s.serviceName)).size;
  const traceServices = [...new Set(spans.map(s => s.serviceName).filter(Boolean))];
  const canMore = bundle.truncated.logs && logLimit < KIOSK_LOG_LIMIT_MAX;
  const allLogsHref = logsHref({
    window: bundle.window ? { fromNs: bundle.window.from, toNs: bundle.window.to } : null,
    traceId: id,
  });

  return (
    <div className="trace-kiosk">
      <div className="trace-kiosk__brand">
        {brand.logoDataUri
          ? <img className="trace-kiosk__logo" src={brand.logoDataUri} alt="" />
          : <TelescopeIcon size={26} />}
        <div>
          <div className="trace-kiosk__brand-name"><Wordmark name={brand.appName} /></div>
          <div className="trace-kiosk__brand-sub">Trace kiosk · salt-okunur görünüm</div>
        </div>
        <div className="trace-kiosk__brand-spacer" />
        {/* v0.10.680 (operatör: "sağ tarafta log izleme linki de olsa") — bu
            trace'in logları tam Logs sayfasında, yeni pencerede; URL üreticiden
            (logsHref: traceId + span penceresi ±). */}
        <a className="trace-kiosk__brand-link" href={allLogsHref} target="_blank" rel="noopener noreferrer"
          title="Bu trace'in loglarını Logs sayfasında aç (yeni pencere)">≡ Logs ↗</a>
      </div>
      <div className="trace-kiosk__head">
        {root && <SvcBadge name={root.serviceName} />}
        <span className="trace-kiosk__title" title={root ? displaySpanName(root) : id}>
          {root ? displaySpanName(root) : 'Trace'}
        </span>
        <span className={`badge ${hasErr ? 'b-err' : 'b-ok'}`}>{hasErr ? 'ERROR' : 'OK'}</span>
        {errSpans > 0 && <span className="cell-hint">{errSpans} error span{errSpans === 1 ? '' : 's'}</span>}
        <span className="trace-summary__dur" title="Trace toplam süresi: ilk span başlangıcından son span bitişine">⏱ {fmtNs(totalNs)}</span>
        <span>{spans.length} spans · {svcCount} service{svcCount === 1 ? '' : 's'}</span>
        {root && <span title="Trace başlangıcı (kök span)">{tsLong(root.startTime)}</span>}
        <code className="trace-kiosk__id">{id}<CopyButton value={id} title="Copy trace ID" /></code>
        {bundle.source === 'tempo' && <span className="badge b-info" title="source: Tempo fallback">Tempo</span>}
        {bundle.spanCapped && (
          <span className="badge b-warn" title="50k span tavanı doldu; şelale kesik">
            span tavanı{bundle.spanTotal ? ` · ${bundle.spanTotal.toLocaleString()}` : ''}
          </span>
        )}
      </div>
      <TraceWaterfall
        spans={spans}
        selectedId={selectedId}
        onSelect={setSelectedId}
        analysis={analysis}
        revealSpanId={revealSpanId}
        logSignals={logSignals}
        renderDetail={id => (selectedSpan && selectedSpan.spanId === id ? (
          /* v0.10.682 — panel tıklanan satırın HEMEN altında (Tempo), şelalenin
             altında değil (operatör düzeltmesi). */
          <KioskSpanPanel
            span={selectedSpan}
            traceStartNs={Number.isFinite(minT) ? minT : selectedSpan.startTime}
            logs={logsState.logs ?? []}
            eventRows={eventRows}
            onClose={() => setSelectedId(null)}
          />
        ) : null)}
      />
      <div className="trace-kiosk__logs">
        <div className="trace-kiosk__logs-head">
          <span>Logs</span>
          {bundle.truncated.logs && (canMore
            ? (
              <Button variant="secondary" size="xs" loading={bundleQ.isFetching}
                onClick={() => setLogLimit(KIOSK_LOG_LIMIT_MAX)}>
                Daha fazla (ilk {KIOSK_LOG_LIMIT_MAX})
              </Button>
            ) : (
              <a href={allLogsHref} target="_blank" rel="noopener">Tümünü Logs'ta aç ↗</a>
            ))}
        </div>
        <TraceLogsPanel
          logs={logsState.logs}
          degraded={logsState.degraded}
          logsTotal={logsState.logsTotal}
          eventRows={shownEventRows}
          oracleRows={logsState.oracleRows}
          oracleError={logsState.oracleError}
          hiddenGrpcMsgs={hiddenGrpcMsgs}
          showGrpcMsgs={showGrpcMsgs}
          onToggleGrpcMsgs={toggleGrpcMsgs}
          traceServices={traceServices}
        />
      </div>
    </div>
  );
}
