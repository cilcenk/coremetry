import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ArrowLeft, ArrowDownToLine } from 'lucide-react';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { fmtFixed, tsLong } from '@/lib/utils';
import { Spinner } from '@/components/Spinner';
import { AIExplainButton } from '@/components/ai/AIExplainButton';
import { RenderedMarkdown } from '@/components/Markdown';
import { useAiEvidence } from '@/components/ai/aiEvents';
import { RootCausePanel } from '@/components/RootCausePanel';
import { ProblemRunbookPanel } from '@/components/ProblemRunbookPanel';
import { IconSparkles } from '@/components/icons';
import { TimeChart } from '@/components/charts/TimeChart';
import type { ChartTimeRegion } from '@/lib/chart/overlays';
import { statusColor } from '@/lib/statusColor';
import { fmtDurationNs, fmtStartedTs } from './problemTime';
import { emptySamplesNote } from './exceptionSamples';
import type { ExceptionGroup, ExceptionGroupState, Problem } from '@/lib/types';
import { Button } from '@/components/ui/Button';
import { ShareButton } from '@/components/ShareButton';
import { copyToClipboard } from '@/lib/clipboard';
import { QueryErrorInline } from '@/components/QueryError';
import { serviceHref } from '@/lib/serviceHref';

// ProblemDetail — Variant B (Dynatrace problem feed) full-page details.
// Two surfaces share one skeleton: a top triage bar (badges + ID +
// started/duration + actions) over a 1.5fr/1fr grid — left column
// root-cause card → metric card → vertical timeline; right column
// blast radius → correlated signals → sample pre block. Exception
// groups (ProblemDetail) and firing alert problems (AlertProblemDetail,
// which replaced the v0.5.80 TriageDrawer) render through it.
//
// All colors ride globals.css tokens (.pb-* helpers) so dark / light /
// redhat themes drive them; deploy correlation renders ONLY when the
// row carries recentDeploy — no placeholder, no extra fetch.

const STATE_LABEL: Record<ExceptionGroupState, string> = {
  // 'new' renders OPEN (v0.8.382): NEW is reserved for the yellow
  // first-seen-recently badge on the list — same rule as StateBadge.
  new: 'OPEN', regressed: 'REGRESSED', acknowledged: 'ACK', resolved: 'RESOLVED', ignored: 'IGNORED',
};
const STATE_BADGE: Record<ExceptionGroupState, string> = {
  new: 'b-err', regressed: 'b-err', acknowledged: 'b-warn', resolved: 'b-ok', ignored: 'b-gray',
};

// ShareButton (shared, v0.8.540 — was a local copy here) copies the
// current address-bar URL. The URL is already the canonical shareable
// link: both detail views keep ?problem=<id> / ?exc=<fingerprint> in
// sync via problemLink.ts, so this is a one-click affordance on top of
// "copy from the address bar" for an operator pasting into Slack.

// Esc = back — same muscle memory the old drawer had.
function useEscBack(onBack: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onBack(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onBack]);
}

function Sect({ title, accent, sub, children }: {
  title: string; accent?: boolean; sub?: React.ReactNode; children: React.ReactNode;
}) {
  return (
    <div className="pb-sect">
      <div className={accent ? 'h accent' : 'h'}>
        {title}
        {sub && <span style={{ marginLeft: 'auto', fontWeight: 400, fontSize: 11, color: 'var(--text3)' }}>{sub}</span>}
      </div>
      <div className="b">{children}</div>
    </div>
  );
}

function SignalLink({ to, label, sub }: { to: string; label: string; sub?: string }) {
  return (
    <Link to={to} style={{
      display: 'flex', alignItems: 'baseline', gap: 8,
      padding: '7px 10px', marginBottom: 6,
      border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
      background: 'var(--bg2)', textDecoration: 'none',
      color: 'var(--accent2)', fontSize: 12,
    }}>
      <span style={{ fontWeight: 600 }}>{label} ↗</span>
      {sub && <span style={{ color: 'var(--text3)', fontSize: 11 }}>{sub}</span>}
    </Link>
  );
}

// DeployBox — renders ONLY when the row carries a recentDeploy (spec:
// no placeholder, no "no deploy detected", no extra fetch).
function DeployBox({ version, ageSeconds }: { version: string; ageSeconds: number }) {
  return (
    <div style={{
      fontSize: 12, padding: '8px 12px', marginTop: 10,
      borderRadius: 'var(--radius-sm)',
      background: 'color-mix(in srgb, var(--warn) 10%, transparent)',
      border: '1px solid color-mix(in srgb, var(--warn) 35%, transparent)',
    }}>
      <span style={{ fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <ArrowDownToLine size={13} strokeWidth={1.75} /> Deploy correlation
      </span>{' — '}
      <code className="mono">{version}</code> landed{' '}
      <b>{Math.max(1, Math.round(ageSeconds / 60))}m before</b> this problem opened.
    </div>
  );
}

// ── Exception-group detail (classic, pre-v0.8.426 layout) ───────────────────
//
// v0.8.510 (operator decision): the Variant-B redesign detail (triage bar
// over a 1.5fr/1fr root-cause / blast-radius grid, v0.8.426) is reverted
// here to the classic layout the operator preferred — detail bar (state +
// occurrences + actions) → red exception header → meta chips → AI Analizi →
// full-width occurrences histogram → a 1.4fr/1fr grid (Stack trace LEFT —
// the v0.8.61 ratio fix — | Sample traces). ONLY this exception-group
// surface reverts; AlertProblemDetail (firing alert problems) keeps the
// Variant-B full page below, and AnomaliesPage still owns the ?exc= URL
// open/close flow untouched.
//
// Data access stays on today's code: occurrences are the real server-side
// gap-filled COUNT (v0.8.309), state actions go through
// api.setExceptionGroupState. Two deliberate carry-overs from Variant-B:
// occTimes/occSeries are memoized so a Copy/state re-render doesn't tear
// down the uPlot (v0.5.184 unstable-input class), and the detail bar keeps
// ShareButton + Esc-back as the affordances for the kept shareable link.

// v0.9.740 (operatör) — ug/sy ekip rozetleri: service_metadata
// katalogundan (60s cache'li ortak sorgu; sayfayla aynı queryKey,
// RQ tekilleştirir — ek istek yok). Boşsa rozet çizilmez.
function useServiceTeams(service: string): { ug?: string; sy?: string } {
  const catalogQ = useServicesMetadata();
  const m = catalogQ.data?.[service];
  return { ug: m?.ownerTeam || undefined, sy: m?.sreTeam || undefined };
}

function TeamChips({ service }: { service: string }) {
  const { ug, sy } = useServiceTeams(service);
  if (!ug && !sy) return null;
  return (
    <>
      {ug && <span className="chip"><span className="k">ug-team</span><b className="mono">{ug}</b></span>}
      {sy && <span className="chip"><span className="k">sy-team</span><b className="mono">{sy}</b></span>}
    </>
  );
}

export function ProblemDetail({ group, isAdmin, onBack, onChanged }: {
  group: ExceptionGroup;
  isAdmin: boolean;
  onBack: () => void;
  onChanged: () => void;
}) {
  const navigate = useNavigate();
  const [state, setState] = useState<ExceptionGroupState>(group.state);
  const [copied, setCopied] = useState(false);
  // v0.9.414 — Explain'in deterministik kanıt trace'leri: örnek-trace
  // satırları kutulanır (Explain trace'in waterfall kutulaması gibi).
  const [evTraces, setEvTraces] = useState<string[]>([]);
  useEffect(() => { setEvTraces([]); }, [group.fingerprint]);
  useEscBack(onBack);

  const samplesQ = useQuery({
    queryKey: ['exc-samples-detail', group.fingerprint],
    queryFn: () => api.exceptionGroupSamples(group.fingerprint, 100),
    staleTime: 30_000,
  });
  // v0.9.477 — kanıt trace'leri artık AI çekmecesinden window köprüsüyle
  // geliyor (eski onEvidenceTraces prop'unun yerine); v0.9.414 kutulama
  // sözleşmesi + bayat-liste tazeleme aynen korundu.
  useAiEvidence(d => {
    const ids = d.traceIds;
    if (!ids?.length) return;
    setEvTraces(ids);
    // Sıcak grupta backend en YENİ örneği kanıt seçer; mount'taki liste
    // bayatsa kutulanacak satır listede olmayabilir — kanıt listede yoksa
    // örnekleri tazele (v0.9.414 verify bulgusu).
    const have = new Set((samplesQ.data?.samples ?? []).map(sm => sm.traceId));
    if (ids.some(tid => !have.has(tid))) void samplesQ.refetch();
  });
  // (Kanıt satırına tıklanınca kaydırma tarafı DOM üzerinden çalışıyor —
  // aşağıdaki satırların `data-trace-id`'si + aiEvents.scrollToAttr; bu
  // bileşenin ek bir dinleyiciye ihtiyacı yok.)
  const samples = samplesQ.data?.samples ?? [];
  // v0.9.463 (dürüstlük A11) — sahte-boş ayrımı: liste boş kalabilir ama bu
  // "örnek yok" demek değildir. v0.9.795: tarama partili, üç ayrı son var
  // (tavan / pencere bitti / gerçekten yok) ve üçü ayrı cümle.
  const emptyNote = emptySamplesNote(samplesQ.data, 'No sample traces.');

  // Occurrences-over-time is a real server-side, gap-filled COUNT over the
  // group's whole window (v0.8.309) — NOT bucketed from the sampled
  // timestamps, which clustered near last_seen and mis-rendered any busy
  // group as a single right-edge spike.
  const occQ = useQuery({
    queryKey: ['exc-occ-detail', group.fingerprint],
    queryFn: () => api.exceptionGroupOccurrences(group.fingerprint),
    staleTime: 30_000,
  });
  const occRaw = occQ.data ?? [];
  // v0.9.488 (operatör: "occurrences sağında solunda zaman boşluğu olursa ne
  // zaman başladı bitti ya da devam ediyor mu daha net anlaşılır") — sunucu
  // serisi first→last occurrence aralığını doldurur; grafik veri aralığına
  // kırpılınca "burada BAŞLADI mı yoksa pencere mi burada başlıyor" ve
  // "bitti mi / hâlâ akıyor mu" okunamıyordu. İki taraf sıfırla dolar:
  //   • sol: 3 sessiz bucket — ilk bar'ın öncesi görünür ("burada başladı").
  //   • sağ: ŞİMDİye kadar sıfır dolgu (300 bucket tavanı — çok eski bir grup
  //     yine uzun boş kuyrukla "bitti" okunur, uPlot'a 40k nokta basılmaz).
  //     Bar'lar sağ kenara dayanıyorsa grup hâlâ ateşliyor demektir.
  const occAll = useMemo(() => {
    if (occRaw.length === 0) return occRaw;
    const step = occRaw.length >= 2
      ? Math.max(1, occRaw[1].time - occRaw[0].time)
      : 60e9;
    const out = [...occRaw];
    for (let i = 1; i <= 3; i++) out.unshift({ time: occRaw[0].time - i * step, count: 0 });
    const nowNs = Date.now() * 1e6;
    let t = occRaw[occRaw.length - 1].time + step;
    for (let n = 0; t <= nowNs && n < 300; t += step, n++) out.push({ time: t, count: 0 });
    return out;
  }, [occRaw]);
  // Madde 4 sweep — histogram üstünde drag-brush = YEREL zoom penceresi
  // (M3 regions'ın devamı). Occurrences ucu range parametresi almaz (grubun
  // tüm penceresi tek fetch'te gelir) → sayfa range'i yok; zoom, gelen
  // bucket'ların istemci tarafında pencereye kırpılmasıdır. Çift-tık tam
  // aralığa döner; grup değişince pencere sıfırlanır. 2'den az bucket
  // bırakan aşırı dar brush yok sayılır (chart ≥2 x-noktası ister).
  const [zoomMs, setZoomMs] = useState<{ from: number; to: number } | null>(null);
  useEffect(() => { setZoomMs(null); }, [group.fingerprint]);
  const occ = useMemo(() => {
    if (!zoomMs) return occAll;
    const f = zoomMs.from * 1e6, t = zoomMs.to * 1e6; // ms → ns
    const v = occAll.filter(p => p.time >= f && p.time <= t);
    return v.length >= 2 ? v : occAll;
  }, [occAll, zoomMs]);
  // Memoize the TimeChart inputs on occQ.data: the effect deps include
  // times/series, so per-render arrays would tear down and rebuild the
  // uPlot on EVERY state change (e.g. the Copy button's `copied` flip) —
  // the v0.5.184 unstable-input class. x ticks use TimeChart's default
  // house day-boundary formatter (v0.8.402 fix), same as classic.
  const occTimes = useMemo(() => occ.map(p => p.time / 1e9), [occ]);
  const occSeries = useMemo(() => [{
    key: 'occ', label: 'occurrences', data: occ.map(p => p.count),
    color: statusColor('warn'), type: 'bar' as const,
  }], [occ]);
  // Grafana-parite M3 — problemin penceresi (firstSeen → resolvedAt | grafik
  // sonu) histograma x-bölgesi olarak biner: kırmızı gölge + üst şerit +
  // durum etiketi (mockup "P1 problem penceresi" dili). Çözülmüş grupta bölge
  // resolvedAt'te BİTER — kuyruktaki temiz kesim "burada çözüldü" okunur.
  // Açık grupta uç, grafiğin son bucket'ına sabit ('now' imza churn'ü yok);
  // memo'lu — Copy/state re-render'ı TimeChart rebuild'i tetiklemez.
  const probRegions = useMemo<ChartTimeRegion[] | undefined>(() => {
    if (occTimes.length === 0) return undefined;
    const endSec = group.resolvedAt
      ? group.resolvedAt / 1e9
      : occTimes[occTimes.length - 1];
    const r: ChartTimeRegion = {
      fromSec: group.firstSeen / 1e9,
      toSec: endSec,
      color: 'var(--err)',
      label: STATE_LABEL[state] ?? 'OPEN',
    };
    return r.toSec > r.fromSec ? [r] : undefined;
  }, [occTimes, group.firstSeen, group.resolvedAt, state]);

  // Representative stack = the first sample that carries one.
  const stack = samples.find(s => s.stacktrace)?.stacktrace ?? '';
  const stackLines = stack ? stack.split('\n') : [];

  // v0.9.882 (Dalga 2, W2.2) — bu dört buton bugüne dek HİÇBİR çift-tık
  // koruması taşımıyordu: ikinci tık ikinci PUT ve backend tarafında ikinci
  // audit girdisi üretiyordu. `acting` hangi aksiyonun uçtuğunu tutuyor —
  // yalnız ona basılan buton dönerken diğerleri de kilitleniyor, çünkü
  // dördü de AYNI durumu yazıyor ve yarış hâlinde son yanıt kazanırdı.
  const [acting, setActing] = useState<ExceptionGroupState | null>(null);
  const act = async (next: ExceptionGroupState) => {
    if (acting) return;
    setActing(next);
    setState(next);
    try {
      await api.setExceptionGroupState(group.fingerprint, next);
      onChanged();
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err));
      setState(group.state);
    } finally {
      setActing(null);
    }
  };
  // v0.8.548 — was `navigator.clipboard?.writeText(stack).then(…)`: the
  // optional chain guards the CALL but not the `.then`, so on a plain-HTTP
  // install (no secure context → clipboard undefined) this threw a
  // TypeError instead of copying. Now it routes through the shared helper,
  // which falls back to a textarea, and only flashes if the copy landed.
  const copyStack = async () => {
    if (!stack) return;
    if (await copyToClipboard(stack)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }
  };

  return (
    <div id="content">
      {/* Detail bar */}
      <div className="rb-bar">
        <Button variant="secondary" onClick={onBack} leftIcon={<ArrowLeft size={14} strokeWidth={1.75} />}>
          Problems
        </Button>
        <span className={`badge ${STATE_BADGE[state]}`}>{STATE_LABEL[state]}</span>
        <span className="badge b-gray">{group.occurrences.toLocaleString()} occurrences</span>
        <span className="spacer" />
        <ShareButton copiedLabel="Copied" />
        {isAdmin && (state === 'new' || state === 'regressed' || state === 'acknowledged') && (
          <>
            {state !== 'acknowledged' && <Button variant="secondary" onClick={() => act('acknowledged')}
              loading={acting === 'acknowledged'} disabled={acting !== null}>Acknowledge</Button>}
            <Button variant="secondary" onClick={() => act('ignored')}
              loading={acting === 'ignored'} disabled={acting !== null}>Ignore</Button>
            <Button onClick={() => act('resolved')}
              loading={acting === 'resolved'} disabled={acting !== null}>Resolve</Button>
          </>
        )}
        {isAdmin && (state === 'resolved' || state === 'ignored') && (
          <Button variant="secondary" onClick={() => act('new')}
            loading={acting === 'new'} disabled={acting !== null}>Reopen</Button>
        )}
      </div>

      {/* Exception header (no card) */}
      <div className="mono" style={{ fontSize: 13.5, fontWeight: 700, color: 'var(--err)', marginBottom: 4, wordBreak: 'break-all' }}>
        {group.type}
      </div>
      <div className="mono" style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16, wordBreak: 'break-word' }}>
        {group.message || '—'}
      </div>

      {/* Meta chips */}
      <div className="meta-row" style={{ marginBottom: 18 }}>
        {/* v0.9.740 (operatör): servis adı TIKLANABİLİR + ug/sy rozetleri. */}
        {/* v0.9.860 (UX denetimi K1) — grubun kendi penceresi (ilk→son
            görülme) linke biner: aksi hâlde servis sayfası "şimdi" ile
            açılır ve exception'ın izi görünmez. */}
        <Link to={serviceHref(group.service, { range: { fromNs: group.firstSeen, toNs: group.lastSeen } })}
          className="chip" style={{ textDecoration: 'none' }}
          title="Servis sayfasını aç">
          <span className="k">service</span><b className="mono" style={{ color: 'var(--accent2)' }}>{group.service}</b>
        </Link>
        <TeamChips service={group.service} />
        <span className="chip"><span className="k">first seen</span><b className="mono">{tsLong(group.firstSeen)}</b></span>
        <span className="chip"><span className="k">last seen</span><b className="mono">{tsLong(group.lastSeen)}</b></span>
      </div>

      {/* v0.9.415 — arka plan ExceptionExplainer'ın proaktif kök-sebep
          özeti (P1 gruplara otomatik dolar; problems'taki ai_summary
          bloğunun ikizi). Explain butonu yine durur — taze soruşturma. */}
      {group.aiSummary && (
        <div style={{
          fontSize: 12, color: 'var(--text2)', marginBottom: 12,
          padding: '8px 10px', borderRadius: 'var(--radius-sm)',
          background: 'var(--accent-soft)',
          borderLeft: '2px solid var(--accent)',
        }}>
          {/* v0.9.696 — pre-wrap KALKTI: RenderedMarkdown zaten <p>/<ul>
              üretiyor, ikisi birlikte satır aralarını ikiye katlıyor
              (v0.9.641'de CopilotExplain'de öğrenildi). */}
          <IconSparkles size={11} /> <RenderedMarkdown text={group.aiSummary} />
        </div>
      )}

      {/* v0.9.414 (operatör istegi) — exception kök-sebep: backend örnek
          trace + trace loglarını + deploy penceresini otomatik toplar;
          kanıt trace'leri sağdaki örnek satırlarını kutular. */}
      {/* v0.9.477 — cevap tek sağ AI çekmecesinde (?ai=exception:<fp>);
          kanıt trace'leri window köprüsüyle gelip örnek satırlarını
          kutulamaya devam ediyor. */}
      <div style={{ marginBottom: 16 }}>
        <AIExplainButton subject={{ kind: 'exception', id: group.fingerprint }}
          label={<><IconSparkles /> <span style={{ marginLeft: 6 }}>Explain root cause</span></>} />
      </div>

      {/* v0.9.432 (operatör kararı) — AIAnalysisPanel bu sayfadan
          KALDIRILDI: "Explain root cause" (v0.9.414, exception'a özgü
          trace+log soruşturmalı) ile yan yana iki AI affordance'ı kafa
          karıştırıyordu; servis-bağlamlı genel analiz Service
          sayfasında yaşamaya devam ediyor. */}

      {/* Occurrences over time — real server-side, gap-filled COUNT over the
          group's whole window (v0.8.309). */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="ov-card-h">
          <h3>Occurrences over time</h3>
          <span className="ov-sub">{group.occurrences.toLocaleString()} total</span>
        </div>
        <div className="ov-card-b">
          {occ.length === 0 ? (
            /* v0.9.858 (UX denetimi K6) — hata "No occurrences to chart."
               oluyordu: sorgu düştüğünde grup hiç patlamamış gibi
               okunuyordu. */
            occQ.isError ? (
              <QueryErrorInline
                text={`Occurrence series could not be loaded${occQ.error instanceof Error ? ` — ${occQ.error.message}` : ''}`}
                onRetry={() => occQ.refetch()} />
            ) : (
              <div style={{ color: 'var(--text3)', fontSize: 12 }}>
                {occQ.isLoading ? 'Loading…' : 'No occurrences to chart.'}
              </div>
            )
          ) : (
            <TimeChart times={occTimes} series={occSeries} height={110} regions={probRegions}
              onBrush={(fromMs, toMs) => setZoomMs({ from: fromMs, to: toMs })}
              onZoomReset={() => setZoomMs(null)} />
          )}
        </div>
      </div>

      {/* Stack trace (left) · Sample traces (right). minWidth:0 on the columns
          so the long Java stack frames don't force the left column past 1.4fr
          (the v0.8.61 ratio fix — stack trace forced into the left column). */}
      <div style={{ display: 'grid', gridTemplateColumns: '1.4fr 1fr', gap: 16 }}>
        {/* Stack trace */}
        <div className="card" style={{ minWidth: 0 }}>
          <div className="ov-card-h">
            <h3>Stack trace</h3>
            <span className="ov-sub">representative sample</span>
            <span className="ov-right">
              <Button variant="secondary" size="sm" onClick={copyStack} disabled={!stack}>
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </span>
          </div>
          <div className="ov-card-b" style={{ background: 'var(--bg2)', borderRadius: '0 0 8px 8px' }}>
            {stackLines.length === 0 ? (
              // v0.9.795 — stack kutusu örneklerden beslenir, o yüzden
              // örnek YOKKEN "No stack trace on the sampled occurrences"
              // demek yanlış-boş: taranmış örnek yoktu ki. Örnek varken
              // (hepsinin stack'i boşsa) eski dürüst mesaj kalır.
              /* v0.9.858 (UX denetimi K6) — örnek sorgusunun hatası
                 "No sample traces." olarak sunuluyordu. */
              samplesQ.isError ? (
                <QueryErrorInline
                  text={`Samples could not be loaded${samplesQ.error instanceof Error ? ` — ${samplesQ.error.message}` : ''}`}
                  onRetry={() => samplesQ.refetch()} />
              ) : (
                <div style={{ color: samples.length === 0 && emptyNote.warn ? 'var(--warn)' : 'var(--text3)', fontSize: 12 }}>
                  {samplesQ.isLoading ? 'Loading…'
                    : samples.length === 0 ? emptyNote.text
                    : 'No stack trace on the sampled occurrences.'}
                </div>
              )
            ) : (
              <pre className="mono" style={{ margin: 0, fontSize: 11.5, lineHeight: 1.7, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
                {stackLines.map((l, i) => (
                  <div key={i} style={{ color: i === 0 ? 'var(--err)' : 'var(--text2)' }}>{l}</div>
                ))}
              </pre>
            )}
          </div>
        </div>

        {/* Sample traces */}
        <div className="card" style={{ minWidth: 0 }}>
          <div className="ov-card-h"><h3>Sample traces</h3>{samples.length > 0 && <span className="ov-sub">{Math.min(samples.length, 14)}{samples.length > 14 ? ` / ${samples.length}` : ''}</span>}</div>
          <div className="table-wrap">
            <table>
              <tbody>
                {samplesQ.isLoading && <tr><td style={{ padding: 12 }}><Spinner /></td></tr>}
                {!samplesQ.isLoading && samples.length === 0 && (
                  <tr><td style={{ padding: 12, color: emptyNote.warn ? 'var(--warn)' : 'var(--text3)', fontSize: 12 }}>
                    {emptyNote.text}
                  </td></tr>
                )}
                {samples.slice(0, 14).map((s, i) => {
                  const isEv = !!s.traceId && evTraces.includes(s.traceId);
                  return (
                  // data-trace-id (v0.9.477): AI çekmecesindeki kanıt satırı
                  // tıklanınca buraya kaydırılır.
                  <tr key={i} data-trace-id={s.traceId || undefined}
                    className={isEv ? 'wf-evidence' : undefined}
                    title={isEv ? 'Explain kanıtı — kök neden bu trace üzerinden soruşturuldu' : undefined}
                    style={{ cursor: s.traceId ? 'pointer' : 'default' }}
                    onClick={() => s.traceId && navigate(`/trace?id=${encodeURIComponent(s.traceId)}`)}>
                    <td className="mono" style={{ paddingLeft: 14 }}>
                      <span style={{ color: 'var(--accent)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block', maxWidth: 150 }}>
                        {s.traceId ? s.traceId.slice(0, 16) + '…' : '—'}
                      </span>
                    </td>
                    <td><span className="badge b-err">ERROR</span>{isEv && <span className="badge b-warn" style={{ marginLeft: 6 }}>kanıt</span>}</td>
                    <td className="mono" style={{ textAlign: 'right', paddingRight: 14, fontSize: 11, color: 'var(--text3)' }}>{tsLong(s.time)}</td>
                  </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  );
}

// ── Firing alert-problem detail (ex-TriageDrawer, now full page) ────────────

export function AlertProblemDetail({ problem, isAdmin, onBack, onChanged }: {
  problem: Problem;
  isAdmin: boolean;
  onBack: () => void;
  onChanged: () => void;
}) {
  useEscBack(onBack);
  const [acking, setAcking] = useState(false);
  const isAnomaly = problem.ruleId?.startsWith('anomaly:');
  const endNs = problem.resolvedAt || Date.now() * 1e6;
  const sevCls = problem.severity === 'critical' ? 'b-err' : problem.severity === 'warning' ? 'b-warn' : 'b-info';

  const ack = async () => {
    setAcking(true);
    try {
      await api.acknowledgeProblems([problem.id]);
      onChanged();
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err));
    } finally {
      setAcking(false);
    }
  };

  const logsFrom = Math.round((problem.startedAt - 60 * 60 * 1e9) / 1e6);
  const logsTo = Math.round((endNs + 10 * 60 * 1e9) / 1e6);
  // v0.9.860 (UX denetimi K1) — problem penceresi, servis/pod pivotları için
  // (logs/traces linkleriyle AYNI sınırlar; ns cinsinden).
  const probWindow = { fromNs: problem.startedAt - 60 * 60 * 1e9, toNs: endNs + 10 * 60 * 1e9 };
  const logsHref = `/logs?q=${encodeURIComponent(`service.name:"${problem.service.replace(/"/g, '\\"')}"`)}&range=${encodeURIComponent(`custom:${logsFrom}-${logsTo}`)}`;

  return (
    <div id="content">
      <div className="rb-bar">
        <Button variant="secondary" onClick={onBack} leftIcon={<ArrowLeft size={14} strokeWidth={1.75} />}>
          Problems
        </Button>
        <span className={`badge ${sevCls}`}>{problem.severity.toUpperCase()}</span>
        {problem.status === 'open' && <span className="badge b-err">OPEN</span>}
        {problem.status === 'acknowledged' && <span className="badge b-warn">ACK</span>}
        {problem.status === 'resolved' && <span className="badge b-ok">RESOLVED</span>}
        {problem.priority && <span className={`badge ${problem.priority === 'P1' ? 'b-err' : problem.priority === 'P2' ? 'b-warn' : 'b-gray'}`}
          title={problem.priorityReason ? `${problem.priority} — ${problem.priorityReason}` : problem.priority}>{problem.priority}</span>}
        <span className="badge b-gray mono">{problem.id.slice(0, 12)}</span>
        <span className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
          Started {fmtStartedTs(problem.startedAt)} · {fmtDurationNs(endNs - problem.startedAt)}
          {problem.status !== 'resolved' ? ' · ongoing' : ''}
        </span>
        <span className="spacer" />
        <ShareButton copiedLabel="Copied" />
        {isAdmin && problem.status === 'open' && (
          <Button variant="secondary" size="sm" onClick={() => { void ack(); }} loading={acking}>
            Acknowledge
          </Button>
        )}
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1.5fr 1fr', gap: 14, alignItems: 'start' }}>
        {/* ── Left column ── */}
        <div style={{ minWidth: 0 }}>
          <Sect title="Root cause analysis" accent>
            <div style={{ fontSize: 13, marginBottom: 8 }}>
              {isAnomaly && <span className="badge b-info" style={{ marginRight: 6 }}>ANOMALY</span>}
              <b>{problem.ruleName}</b>
            </div>
            <RootCausePanel problemId={problem.id} service={problem.service} window={probWindow} />
            {/* Background problemExplainer's persisted first-look blurb —
                full prose here (the feed card only tooltips it). */}
            {problem.aiSummary && (
              <div style={{
                fontSize: 12, color: 'var(--text2)', marginTop: 10,
                padding: '8px 10px', borderRadius: 'var(--radius-sm)',
                background: 'var(--accent-soft)',
                borderLeft: '2px solid var(--accent)',
              }}>
                {/* v0.9.696 — exception ikiziyle aynı: markdown basılıyor. */}
                <IconSparkles size={11} /> <RenderedMarkdown text={problem.aiSummary} />
              </div>
            )}
            {problem.recentDeploy && (
              <DeployBox version={problem.recentDeploy.version} ageSeconds={problem.recentDeploy.ageSeconds} />
            )}
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 10 }}>
              {/* v0.9.477 — iki AI affordance'ı da tek sağ çekmeceye açılır;
                  ikisi aynı anda satır-içi açıldığında kart iki uzun metin
                  bloğuyla şişiyordu. */}
              <AIExplainButton subject={{ kind: 'problem', id: problem.id }}
                label={<><IconSparkles /> <span>Explain</span></>} />
              <AIExplainButton subject={{ kind: 'runbook', id: problem.id }}
                label={<><IconSparkles /> <span>Runbook AI</span></>} />
            </div>
          </Sect>

          <Sect title="Metric" sub={<span className="mono">{problem.metric}</span>}>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 10 }}>
              <span className="pb-headline" style={{ fontSize: 24 }}>{problem.value.toFixed(2)}</span>
              <span className="mono" style={{ color: 'var(--text3)', fontSize: 13 }}>
                / threshold {fmtFixed(problem.threshold, 2)}
              </span>
            </div>
            {problem.priorityReason && (
              <div style={{ fontSize: 12, color: 'var(--text2)', marginTop: 6 }}>{problem.priorityReason}</div>
            )}
          </Sect>

          <Sect title="Problem timeline">
            <ul className="pb-tl">
              {problem.recentDeploy && (
                <li className="warn">
                  <b>Deploy</b> <code className="mono">{problem.recentDeploy.version}</code>
                  <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8 }}>
                    {fmtStartedTs(problem.startedAt - problem.recentDeploy.ageSeconds * 1e9)}
                  </span>
                </li>
              )}
              <li className="err">
                <b>Detected</b> — {problem.ruleName}
                <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8 }}>{fmtStartedTs(problem.startedAt)}</span>
              </li>
              {problem.status === 'resolved' && problem.resolvedAt ? (
                <li className="ok">
                  <b>Resolved</b>
                  <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8 }}>{fmtStartedTs(problem.resolvedAt)}</span>
                </li>
              ) : null}
            </ul>
          </Sect>
        </div>

        {/* ── Right column ── */}
        <div style={{ minWidth: 0 }}>
          <Sect title="Blast radius">
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {/* v0.9.860 (UX denetimi K1) — triage yolculuğunun BELKEMİĞİ.
                  Gece 03:14 alarmının "Blast radius" pill'i servis sayfasını
                  sticky "şimdi" penceresiyle açıyordu: alarm anının izi yok,
                  "sorun geçmiş" yanılgısı. Pencere AYNI bileşende
                  (logsFrom/logsTo) zaten hesaplı, yalnız logs/traces
                  linklerine konuyordu. */}
              <Link to={serviceHref(problem.service, { range: probWindow })}
                className={`pb-pill${problem.status === 'open' ? ' err' : ''}`}
                style={{ textDecoration: 'none', color: 'var(--accent2)' }}>
                <span className="dot" /> <span className="mono">{problem.service}</span>
              </Link>
              {/* v0.9.740 (operatör): ug/sy ekip rozetleri problem
                  detayında da — katalogdan; Problem.ownerTeam alanı
                  boş kalabildiği için tek kaynak katalog. */}
              <TeamChips service={problem.service} />
              {/* v0.9.401 (operator-reported) — runtime pod problemleri artık
                  gerçek servis adı taşıyor; pod kimliği deterministik ID'nin
                  son segmentinde (runtime:<check>:<svc>:<pod> — evaluator
                  runtimeProblemID). Tek yerde çözülür, yapısal `pod` kolonu
                  ayrı dilim. Link Pods sekmesine ?jpod= ile gider —
                  v0.9.533'ten beri ilgili pod SATIRI otomatik açılıp
                  görünüme kaydırılır (eski JMX-bölümü daraltması
                  kaldırıldı; satır genişletmesi aynı JMX'i veriyor). */}
              {(() => {
                // v0.9.403 — yapısal alan öncelikli; ID-parse yalnız 401
                // öncesi ESKİ satırlar için köprü (pod kolonu boş gelir).
                let pod = problem.pod ?? '';
                if (!pod && problem.id?.startsWith('runtime:')) {
                  const segs = problem.id.split(':');
                  pod = segs.length >= 4 ? segs[segs.length - 1] : '';
                }
                if (!pod || pod === problem.service) return null;
                return (
                  <Link to={serviceHref(problem.service, { range: probWindow, tab: 'pods', params: { jpod: pod } })}
                    className="pb-pill"
                    style={{ textDecoration: 'none', color: 'var(--accent2)' }}
                    title="Bu podun JMX/runtime grafikleri (Pods sekmesi, pod daraltmalı)">
                    <span className="dot" /> pod <span className="mono">{pod}</span> →
                  </Link>
                );
              })()}
              {(problem.clusters ?? []).map(c => (
                <span key={c} className="pb-pill"><span className="dot" /> <span className="mono">{c}</span></span>
              ))}
            </div>
          </Sect>

          <Sect title="Correlated signals">
            <SignalLink to={logsHref} label="≡ Logs" sub="service, problem window" />
            {/* v0.8.512 (perf raporu #12) — pivot problem penceresini
                taşır: logs ile aynı custom range, yoksa /traces global
                range'iyle açılıp problemle ilgisiz trace gösteriyordu. */}
            {/* v0.8.585 — Operator-reported: rootOnly default'u TRUE
                olduğundan hata izleri (çoğu non-root span) boş
                listeleniyordu; hasError linki root filtresini kapatır. */}
            <SignalLink to={`/traces?service=${encodeURIComponent(problem.service)}&hasError=true&rootOnly=false&range=${encodeURIComponent(`custom:${logsFrom}-${logsTo}`)}`}
              label="⋮ Error traces" sub="service, problem window" />
            <SignalLink to={`/service-map?focus=${encodeURIComponent(problem.service)}`}
              label="◉ Service map" sub="focused" />
          </Sect>

          <Sect title="Runbook">
            {problem.runbookUrl && (
              <a href={problem.runbookUrl} target="_blank" rel="noopener"
                style={{
                  display: 'inline-block', marginBottom: 8,
                  fontSize: 12, padding: '4px 12px', borderRadius: 'var(--radius-sm)',
                  background: 'var(--accent-soft)', border: '1px solid var(--accent)',
                  color: 'var(--accent2)', textDecoration: 'none',
                }}>
                Runbook ↗
              </a>
            )}
            {/* Problem→Runbook bridge: run an operational runbook against this
                fire (tagged with problemId) + the runs already attached. */}
            <ProblemRunbookPanel problemId={problem.id} />
          </Sect>

          {problem.description && !isAnomaly && (
            <Sect title="Description">
              <pre className="mono" style={{
                margin: 0, fontSize: 11.5, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere',
                color: 'var(--text2)', background: 'var(--bg2)',
                borderRadius: 'var(--radius-sm)', padding: '8px 10px',
              }}>{problem.description}</pre>
            </Sect>
          )}
        </div>
      </div>
    </div>
  );
}
