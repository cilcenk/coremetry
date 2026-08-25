import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Badge, Drawer } from '@/components/ui';
import { ClusterChips } from '@/components/ClusterChips';
import { CopilotExplain } from '@/components/CopilotExplain';
import { RootCauseRibbon } from '@/components/RootCauseRibbon';
import { LogsHistogram } from '@/components/LogsHistogram';
import { fmtNum, tsLong } from '@/lib/utils';
import { tracesPivotHref } from '@/lib/pivotHref';
import type { AnomalyEvent, BehaviorChangeDetails } from '@/lib/types';
import { serviceHref } from '@/lib/serviceHref';
import { logsHref } from '@/lib/logsUrl';

// AnomalyDetailDrawer — v0.8.267, operator-requested: "Anomalies
// sayfasında üzerine tıklayınca ne zaman spike oldu ve benzeri
// detay görmek iyi olurdu, problems gibi." Right-side slide-in
// mirroring the Problems TriageDrawer shell: spike timeline facts
// (started / last seen / duration / peak ×), the service's log
// volume around the spike, deploy chip, root-cause ribbon, AI
// explain, and the cross-signal deep links.
//
// ES-cost contract (operator: "log anomalies elastic backend
// kullanıldığında çok fazla sorgu yapmasın"): the ONLY backend
// fetch this drawer triggers is ONE bounded /api/logs/timeseries
// call, and only (a) when the drawer is actually open and (b) for
// log-shaped kinds. It rides the endpoint's existing 30s server
// cache; trace_op anomalies fetch nothing at all. Rows in the
// table never prefetch.

function fmtDuration(ns: number): string {
  const s = Math.max(0, Math.round(ns / 1e9));
  if (s < 90) return `${s}s`;
  if (s < 90 * 60) return `${Math.round(s / 60)}m`;
  if (s < 36 * 3600) return `${(s / 3600).toFixed(1)}h`;
  return `${(s / 86400).toFixed(1)}d`;
}

function Fact({ k, v, title }: { k: string; v: React.ReactNode; title?: string }) {
  return (
    <div style={{ minWidth: 0 }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', fontWeight: 600,
        textTransform: 'uppercase', letterSpacing: '.05em',
      }}>{k}</div>
      <div className="mono" style={{
        fontSize: 12, color: 'var(--text)', marginTop: 2,
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }} title={title}>{v}</div>
    </div>
  );
}

const KIND_LABEL: Record<AnomalyEvent['kind'], string> = {
  log_pattern: 'LOG PATTERN',
  trace_op: 'TRACE OP',
  elastic_ml: 'ELASTIC ML',
  log_template_new: 'NEW LOG SHAPE',
  behavior_change: 'BEHAVIOR',
};

// v0.9.936 — davranış olaylarının `sample` alanı serbest metin DEĞİL,
// yapılandırılmış kanıt (BehaviorChangeDetails JSON). Ham JSON'u <pre>
// içinde göstermek teknik olarak "boş panel değil" ama operasyonel
// olarak okunamaz; bu kutu onu tek bakışta okunur hâle getiriyor.
//
// AYRIŞTIRMA HER ZAMAN KORUMALI: eski bir satır, elle düzenlenmiş bir
// kayıt ya da ileride değişen bir şekil geçerli JSON olmayabilir.
// Ayrıştırılamazsa null döner ve çağıran ham <pre>'ye düşer — çekmece
// asla patlamaz, en kötü ihtimalle daha az güzel görünür.
function parseBehaviorDetails(sample: string): BehaviorChangeDetails | null {
  try {
    const d = JSON.parse(sample) as BehaviorChangeDetails;
    if (!d || typeof d.metric !== 'string' || typeof d.ratio !== 'number') return null;
    return d;
  } catch {
    return null;
  }
}

// hourOfWeekLabel — 0..167 kovasını insan diline çevirir. Kova UTC'de
// hesaplanıyor (Go ve SQL tarafı da öyle), etiket de öyle diyor:
// operatörün "10:00 dedin ama bizde 13:00'tü" demesi bir hata raporu
// değil, bir birim karışıklığı olurdu.
const HOW_DAYS = ['Pzt', 'Sal', 'Çar', 'Per', 'Cum', 'Cmt', 'Paz'];
function hourOfWeekLabel(how: number): string {
  if (!Number.isFinite(how) || how < 0 || how > 167) return '—';
  const day = HOW_DAYS[Math.floor(how / 24)] ?? '—';
  return `${day} ${String(how % 24).padStart(2, '0')}:00 UTC`;
}

function BehaviorDetailsBox({ d }: { d: BehaviorChangeDetails }) {
  const up = d.direction === 'up';
  const signalLabel = d.signal === 'regime'
    ? 'Regime shift — sustained against the same hour-of-week baseline'
    : 'Seasonal deviation — far from its own hour-of-week baseline';
  const fmt = (v: number) => `${fmtNum(Math.round(v * 100) / 100)}${d.unit}`;
  return (
    <div style={{
      border: '1px solid var(--border)', borderRadius: 6,
      padding: '10px 12px', marginBottom: 12, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 12, color: 'var(--text)', marginBottom: 8 }}>
        {signalLabel}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16, fontSize: 12 }}>
        <div>
          <div style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 600 }}>BASELINE</div>
          <div className="mono">{fmt(d.baseline)}</div>
        </div>
        <div>
          <div style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 600 }}>NOW</div>
          <div className="mono" style={{ color: up ? 'var(--err)' : 'var(--warn)' }}>{fmt(d.current)}</div>
        </div>
        <div>
          <div style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 600 }}>CHANGE</div>
          <div className="mono">{up ? '↑' : '↓'} {fmtNum(Math.round(d.ratio * 100) / 100)}×</div>
        </div>
        <div>
          <div style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 600 }}>ROBUST z</div>
          <div className="mono">{fmtNum(Math.round(d.z * 10) / 10)}σ</div>
        </div>
        <div>
          <div style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 600 }}>SUSTAINED</div>
          <div className="mono">{d.dwell} × 5 min</div>
        </div>
        <div>
          <div style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 600 }}>BASELINE BUCKET</div>
          <div className="mono">{hourOfWeekLabel(d.hourOfWeek)}</div>
        </div>
      </div>
      {d.deploy && (
        <div style={{ fontSize: 12, color: 'var(--text2)', marginTop: 8 }}>
          ⬇ Deploy <b className="mono">{d.deploy.version}</b> landed{' '}
          <b>{Math.max(1, Math.round(d.deploy.ageSeconds / 60))}m before</b> the shift started.
        </div>
      )}
    </div>
  );
}

export function AnomalyDetailDrawer({ event, onClose }: {
  event: AnomalyEvent;
  onClose: () => void;
}) {
  const isLogKind = event.kind === 'log_pattern' || event.kind === 'log_template_new'
    || event.kind === 'elastic_ml';
  const durationNs = Math.max(0, event.lastSeen - event.startedAt);

  // v0.9.936 — davranış olayının yapılandırılmış kanıtı. Memo: her
  // render'da JSON.parse etmek gereksiz, ve yeni bir nesne kimliği
  // aşağıdaki koşulu her seferinde yeniden değerlendirtirdi.
  const behaviorDetails = useMemo(
    () => (event.kind === 'behavior_change' && event.sample
      ? parseBehaviorDetails(event.sample)
      : null),
    [event.kind, event.sample],
  );

  // Chart window: 3× the spike duration of lead-in (min 30 min) so
  // the baseline is visible left of the spike, plus a 10-minute
  // tail. Memoised — a fresh object each render would refire the
  // histogram fetch (v0.5.184 class).
  const chartRange = useMemo(() => {
    const lead = Math.max(3 * durationNs, 30 * 60 * 1e9);
    return {
      from: event.startedAt - lead,
      to: event.lastSeen + 10 * 60 * 1e9,
    };
  }, [event.startedAt, event.lastSeen, durationNs]);
  const chartFilter = useMemo(() => ({
    service: event.service, search: '', severity: 0, traceId: '', spanId: '',
  }), [event.service]);

  // /logs deep link scoped to the service + the spike window.
  //
  // v0.9.1348 — el-yapımı `custom:` kopyası logsHref üreticisine indi. Bu
  // site pencereyi Math.round ile kodluyordu: yuvarlama İKİ kenarı da
  // içeri çekebilir, yani kodlanmış pencere istenenden DAR olabilir ve
  // spike'ın ilk/son milisaniyesindeki log pivottan düşer. Üretici
  // floor/ceil kullanır (urlState.ts:28 kuralı), pencere asla daralmaz.
  //
  // v0.9.1381 — `service=`, `q=` DEĞİL. Eski şerh v0.8.521'i "sunucu
  // serbest-metin sorgusunu kolonla DA eşliyor" diye genel bir kural gibi
  // aktarıyordu; o kural yalnız TRACE ID'leri için doğru (CH'de
  // `isBareHexID` dalı). Servis adı için karşılığı yok: `q` yalnız
  // gövdede arar ve `service.name:"x"` hiçbir gövdede geçmez.
  // Ölçüldü: 0 satır → 535.
  const logsLink = useMemo(() => logsHref({
    window: { fromNs: chartRange.from, toNs: chartRange.to },
    service: event.service || undefined,
  }), [event.service, chartRange]);

  // v0.9.213 — the error-traces pivot used to carry only the service, so
  // /traces opened on the operator's sticky range (useUrlRange) instead of
  // the spike. On an anomaly older than that range the list came back EMPTY,
  // which reads as "no error traces" rather than "wrong window". Same spike
  // bounds as logsLink above.
  const tracesHref = useMemo(() => tracesPivotHref({
    window: { fromNs: chartRange.from, toNs: chartRange.to },
    service: event.service,
    hasError: true,
  }), [event.service, chartRange]);

  // v0.8.499 (sadeleştirme #2, 5/5) — kabuk ui/Drawer'a taşındı:
  // overlay/Esc/✕ tek evden; başlık ve gövde (ES-cost sözleşmesi
  // dahil — histogram yalnız açıkken, tek 30s-cache'li çağrı) birebir.
  return (
    <Drawer onClose={onClose} width={560} header={
      <>
        <Badge tone={event.status === 'active' ? 'danger' : 'success'} style={{ fontSize: 10 }}>
          {event.status === 'active' ? 'ACTIVE' : 'CLEARED'}
        </Badge>
        <span className="badge b-gray" style={{ fontSize: 10 }}>{KIND_LABEL[event.kind]}</span>
        {event.service && (
          /* v0.9.860 (UX denetimi K1) — kardeş logs/traces linkleri (yukarıda)
             spike penceresini v0.9.213'ten beri taşırken servis linki
             taşımıyordu: aynı bileşende iki standart. */
          <Link to={serviceHref(event.service, { range: { fromNs: chartRange.from, toNs: chartRange.to } })}
            style={{ fontWeight: 700, fontSize: 14 }}>
            {event.service}
          </Link>
        )}
        <ClusterChips clusters={event.clusters} />
      </>
    }>
        <div style={{ paddingTop: 10 }}>
          <div style={{
            fontWeight: 700, fontSize: 14, marginBottom: 10,
            overflowWrap: 'anywhere',
          }} title={event.pattern}>{event.pattern}</div>

          {/* Spike timeline — the "ne zaman spike oldu" answer. */}
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))',
            gap: 12, padding: 12, marginBottom: 12,
            background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 8,
          }}>
            <Fact k="Spike started" v={tsLong(event.startedAt)} />
            <Fact k="Last seen" v={tsLong(event.lastSeen)} />
            <Fact k="Duration" v={event.status === 'active'
              ? `${fmtDuration(durationNs)} · ongoing`
              : fmtDuration(durationNs)} />
            <Fact k="Peak ratio" v={`×${event.peakRatio.toFixed(1)}`}
              title="Peak count vs the pre-spike baseline window" />
            {event.currentRatio > 0 && (
              <Fact k="Current ratio" v={`×${event.currentRatio.toFixed(1)}`} />
            )}
            {event.currentCount > 0 && (
              <Fact k="Count in window" v={fmtNum(event.currentCount)} />
            )}
          </div>

          {event.recentDeploy && (
            <div style={{
              fontSize: 12, padding: '8px 12px', marginBottom: 12,
              borderRadius: 6,
              background: 'color-mix(in srgb, var(--warn) 10%, transparent)',
              border: '1px solid color-mix(in srgb, var(--warn) 35%, transparent)',
            }}>
              ⬇ Deploy <b className="mono">{event.recentDeploy.version}</b> landed{' '}
              <b>{Math.max(1, Math.round(event.recentDeploy.ageSeconds / 60))}m before</b> the spike
              ({tsLong(event.recentDeploy.timeUnixNs)}) — likely-cause window ≤ 5m.
            </div>
          )}

          {/* v0.9.936 — davranış olayının kanıtı yapılandırılmış; ham
              JSON yerine okunur kutu. Ayrıştırılamazsa aşağıdaki <pre>
              devreye girer (şekil değişse bile kanıt kaybolmaz). */}
          {behaviorDetails && <BehaviorDetailsBox d={behaviorDetails} />}

          {event.sample && !behaviorDetails && (
            <pre style={{
              fontSize: 11, fontFamily: 'ui-monospace, SFMono-Regular, monospace',
              whiteSpace: 'pre-wrap', overflowWrap: 'anywhere',
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 6, padding: '8px 10px', marginBottom: 12,
              color: 'var(--text2)', maxHeight: 120, overflowY: 'auto',
            }} title="Sample line captured at detection">{event.sample}</pre>
          )}

          {/* Service log volume around the spike — mounted only while
              the drawer is open, one 30s-cached timeseries call, log
              kinds only (ES-cost contract in the header comment). */}
          {isLogKind && event.service && (
            <div style={{ marginBottom: 4 }}>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>
                {event.service} log volume around the spike
                (window {tsLong(chartRange.from)} → {tsLong(chartRange.to)})
              </div>
              <LogsHistogram range={chartRange} filter={chartFilter} />
            </div>
          )}

          {/* Root cause + AI — same affordances the row had, in situ. */}
          <div style={{ marginBottom: 12 }}>
            <RootCauseRibbon anchor="anomaly" id={event.id} summary={event.rootCause}
          window={{ fromNs: chartRange.from, toNs: chartRange.to }} />
          </div>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            {/* v0.9.477 — BİLEREK satır-içi kaldı (tek istisna). Bu yüzey
                zaten bir ui/Drawer; AI çekmecesi üstüne ikinci bir çekmece
                açardı ve iki kabuk da window'da Escape dinlediğinden tek
                ESC ikisini birden kapatırdı. Detay çekmecesinde açıklamanın
                yeri zaten burası. */}
            <CopilotExplain kind="anomaly" id={event.id} label="✨ Explain this anomaly" />
            {/* v0.10.6 — v0.9.1372'nin İKİZİ. O sürüm detay sayfalarının
                pivotlarını `.accent`e taşımıştı; bu çekmecedeki aynı türden
                pivot gözden kaçmıştı. */}
            {isLogKind && event.service && (
              <Link to={logsLink} className="accent"
                style={{ fontSize: 12, padding: '4px 10px', textDecoration: 'none' }}
                title="Open /logs scoped to the service + spike window">
                ≡ Logs in spike window ↗
              </Link>
            )}
            {/* v0.8.585 — Operator-reported: rootOnly default'u TRUE
                olduğundan hata izleri (çoğu non-root span) boş
                listeleniyordu; hasError linki root filtresini kapatır. */}
            {event.kind === 'trace_op' && event.service && (
              <Link to={tracesHref}
                className="sec"
                style={{ fontSize: 12, padding: '4px 10px', textDecoration: 'none' }}
                title="Open error traces for this service, scoped to the spike window">
                ⋮ Error traces ↗
              </Link>
            )}
          </div>
        </div>
    </Drawer>
  );
}
