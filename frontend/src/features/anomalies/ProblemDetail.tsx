import { useEffect, useMemo, useState } from 'react';
import { useEscLayer } from '@/lib/escLayer';
import { Link, useNavigate } from 'react-router-dom';
import { rolloutEvidenceHref } from '@/lib/rolloutRow';
import { useQuery } from '@tanstack/react-query';
import { ArrowLeft, ArrowDownToLine } from 'lucide-react';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { fmtFixed, fmtNum, tsLong } from '@/lib/utils';
import { Spinner, Empty } from '@/components/Spinner';
import { AIExplainButton } from '@/components/ai/AIExplainButton';
import { RenderedMarkdown } from '@/components/Markdown';
import { useAiEvidence } from '@/components/ai/aiEvents';
import { RootCausePanel } from '@/components/RootCausePanel';
import { ProblemRunbookPanel } from '@/components/ProblemRunbookPanel';
import { ProblemNotifyPanel } from './ProblemNotifyPanel';
import { IconSparkles } from '@/components/icons';
import { TimeChart } from '@/components/charts/TimeChart';
import type { ChartTimeRegion } from '@/lib/chart/overlays';
import { statusColor } from '@/lib/statusColor';
import { fmtDurationNs, fmtStartedTs } from './problemTime';
import { emptySamplesNote } from './exceptionSamples';
import { ExceptionPodsPanel } from './ExceptionPodsPanel';
import { ExternalEvidencePanel } from './ExternalEvidencePanel';
import type { ExceptionGroup, ExceptionGroupState, Problem, RolloutEvidence } from '@/lib/types';
import { Button } from '@/components/ui/Button';
import { PageShell } from '@/components/ui/PageShell';
import { ShareButton } from '@/components/ShareButton';
import { copyToClipboard } from '@/lib/clipboard';
import { QueryErrorInline } from '@/components/QueryError';
import { serviceHref, eventLifespanWindow } from '@/lib/serviceHref';
import { logsHref } from '@/lib/logsUrl';
import { tracesPivotHref } from '@/lib/pivotHref';
import { latencyThresholdMs, slowTracesHref } from '@/features/anomalies/slowTracesHref';
import { problemWindowNs, topOffenders } from '@/features/anomalies/problemOffenders';
import { operationTracesHref } from '@/lib/pivotHref';
import { traceHref } from '@/lib/traceHref';
import { SubjectLink } from '../../components/SubjectLink';
import { subjectKind, derivedTeamTitle } from '../../lib/problemSubject';

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
  // v0.9.950 (E2/Ö28) — Esc KATMAN. Detayın ÜSTÜNDE açılan bir modal
  // (exemplar, paylaşım) Esc'i önce alır; geri gitmek en alttaki iş.
  useEscLayer(true, onBack);
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

// ProblemOffenders — v0.9.962 (UX denetimi G5 / Ö8). "Hangi endpoint
// yavaşlattı / hata verdi" sorusunun SAYFADAKİ cevabı.
//
// Alarm servis seviyesinde geliyor; sorumlusunu bulmak için operatör
// /service'e geçip pencereyi ELLE kurup Operations tablosunu sıralamak
// zorundaydı — denetimin "ideal yol farkının en büyük parçası" dediği
// adım. Veri zaten var ve zaten MV'den geliyor; YENİ BACKEND UCU YOK,
// yalnız problem penceresiyle soruluyor.
//
// PENCERE BASAMAKLI (problemOffenders.problemWindowNs): açık bir
// problemde üst uç `Date.now()`tur ve ham hâliyle sorgu anahtarına
// girerse her render yeni anahtar üretir — durmadan refetch, ve
// sunucudaki serveCached anahtarı sınırsız değere dağılır. v0.5.184'ün
// render'da now() tıklatan sınıfı.
function ProblemOffenders({ problem }: { problem: Problem }) {
  // now'u BİR KEZ oku: problem kimliği/çözülme anı değişmedikçe pencere
  // de sabit kalsın (basamak zaten dakikada bir ilerletiyor).
  const win = useMemo(
    () => problemWindowNs(problem.startedAt, problem.resolvedAt, Date.now()),
    [problem.startedAt, problem.resolvedAt],
  );
  const opsQ = useQuery({
    queryKey: ['problem-offenders', problem.service, win.fromNs, win.toNs],
    queryFn: () => api.serviceOperations(problem.service, { from: win.fromNs, to: win.toNs }),
    enabled: !!problem.service,
    staleTime: 60_000,
  });
  const rows = useMemo(() => topOffenders(opsQ.data ?? []), [opsQ.data]);
  return (
    <Sect title="Top offenders" sub="problem window · total time">
      {opsQ.isPending && <Spinner />}
      {/* Hata dalı AYRI: boşa ezmek "bu pencerede operasyon yok" diye
          okunur ve operatör yanlış yerde arar (K6 sınıfı). */}
      {opsQ.isError && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>
          Operations could not be loaded — this is a failed read, not an idle service.
        </div>
      )}
      {opsQ.data && rows.length === 0 && (
        <Empty compact icon="◯" title="No operations in the problem window" />
      )}
      {rows.length > 0 && (
        <table style={{ width: '100%', tableLayout: 'fixed' }}>
          <colgroup><col /><col style={{ width: 72 }} /><col style={{ width: 80 }} /><col style={{ width: 76 }} /></colgroup>
          <thead>
            <tr>
              <th style={{ textAlign: 'left' }}>Operation</th>
              <th className="num">Calls</th>
              <th className="num">P99</th>
              <th className="num">Err %</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(o => (
              <tr key={o.name}>
                <td style={{ overflow: 'hidden' }}>
                  <Link
                    to={operationTracesHref({
                      window: { fromNs: win.fromNs, toNs: win.toNs },
                      operation: o.name, service: problem.service,
                    })}
                    className="mono"
                    style={{
                      display: 'block', overflow: 'hidden', textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap', textDecoration: 'none', color: 'var(--accent2)',
                    }}
                    title={o.name}>
                    {o.name}
                  </Link>
                </td>
                <td className="num">{fmtNum(o.spanCount)}</td>
                <td className="num mono">{o.p99DurationMs.toFixed(0)} ms</td>
                <td className="num">
                  <span className={`badge ${o.errorRate > 5 ? 'b-err' : o.errorRate > 1 ? 'b-warn' : 'b-ok'}`}>
                    {o.errorRate.toFixed(1)}%
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Sect>
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

// ProblemTeamChips — bir PROBLEMİN ekip rozetleri (v0.9.1345).
//
// TeamChips'ten ayrı, çünkü kaynağı farklı ve o fark GÖRÜNÜR olmak
// zorunda. TeamChips kataloğu SERVİS ADIYLA sorar; bir db konusunun
// (`db:oracle@corebank-scan.prod`) kataloğda satırı yok, dolayısıyla o
// bileşen db problemlerinde SESSİZCE hiçbir şey çizmiyordu — ekibi olan
// bir alarm sahipsiz görünüyordu.
//
// Sıra bilinçli:
//  1. Problemin KENDİ alanları (sunucuda EnrichProblemsWithTeams
//     dolduruyor; db konularında türetilerek, servis konularında
//     doğrudan katalogdan). Tek sorgu, satırla birlikte gelmiş.
//  2. Yedek: katalog. Zenginleştirme geçici olarak düşerse servis
//     problemleri bugünkü davranışını korur.
//
// problem.teamsVia doluysa değer TÜRETİLMİŞTİR ve çekince hem GÖRÜNÜR
// (≈ öneki) hem de title'da yazılıdır — yalnız tooltip'e saklamak,
// üstüne gelmeyen operatör için kesin bir atıftan farksız olurdu.
function ProblemTeamChips({ problem }: { problem: Problem }) {
  const fallback = useServiceTeams(problem.service);
  const via = problem.teamsVia || '';
  const ug = problem.ownerTeam || (via ? '' : fallback.ug) || '';
  const sy = problem.sreTeam || (via ? '' : fallback.sy) || '';
  if (!ug && !sy) return null;
  const title = via ? derivedTeamTitle(via, problem.service) : undefined;
  const mark = via ? '≈ ' : '';
  return (
    <>
      {ug && (
        <span className="pb-pill" title={title}>
          <span className="dot" /> ug <span className="mono">{mark}{ug}</span>
        </span>
      )}
      {sy && (
        <span className="pb-pill" title={title}>
          <span className="dot" /> sy <span className="mono">{mark}{sy}</span>
        </span>
      )}
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
    <PageShell>
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
            <Button variant="primary" onClick={() => act('resolved')}
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
      {/* v0.9.983 (D5.2 / A1) — oran satır içiydi ve satır-içi stili
          `@media` YENEMEZ, dolayısıyla 366px'lik telefonda bu ızgara
          210px + 140px iki kolona çöküyordu (stack trace 210px'lik bir
          kolonda monospace). Değerler AYNEN sınıfa taşındı; masaüstü
          görünümü bit bit aynı, yalnız <640px'te tek kolona iniyor. */}
      <div className="pd-cols pd-cols-14">
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
                    onClick={() => s.traceId && navigate(traceHref(s.traceId))}>
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

      {/* v0.10.138 (DETAY SAYFALARI adım 4) — oluşumların pod/node dağılımı +
          pod pivotu; bayrak kapalı → hiç çizilmez. v0.10.173 (operatör, prod):
          EN ALTA — 12 pod'luk tablo grafiğin ve stack'in önüne geçip sayfayı
          itiyordu; dağılım ikincil bağlam. */}
      <ExceptionPodsPanel fingerprint={group.fingerprint} service={group.service} groupOccurrences={group.occurrences} />
    </PageShell>
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

  // v0.9.860 (UX denetimi K1) — problem penceresi, servis/pod pivotları için
  // (logs/traces linkleriyle AYNI sınırlar; ns cinsinden).
  //
  // v0.9.966 — sınırlar lib/serviceHref.eventLifespanWindow'a çıkarıldı:
  // /problems listesi, /anomalies akışı ve /incident aynı pencereyi
  // kuruyordu ve dördü elle senkron tutuluyordu. Detay ile listenin farklı
  // saate bakması, tam da bu ailenin önlemek için var olduğu hata.
  // A row with no usable startedAt is broken, not zero-length — the helper
  // returns undefined rather than pinning a window at the epoch, and the
  // page falls back to the same shape anchored on the end it does know.
  // v0.10.243 — Problem↔Rollout D3: RootCausePanel'in /rootcause yanıtından
  // rollout kanıtı; zaman çizgisine "Rollout" işareti (ikinci istek yok).
  const [rcRollouts, setRcRollouts] = useState<RolloutEvidence[]>([]);
  useEffect(() => { setRcRollouts([]); }, [problem.id]);
  const probWindow = eventLifespanWindow(problem)
    ?? { fromNs: endNs - 60 * 60 * 1e9, toNs: endNs + 10 * 60 * 1e9 };
  // v0.9.1348 — el-yapımı log linki logsHref üreticisine indi; v0.9.1356 —
  // el-yapımı /traces linki de tracesPivotHref'e indi. Aradaki `logsFrom` /
  // `logsTo` (ms) ara değişkenleri BUNUNLA BİRLİKTE SİLİNDİ: ikisi de
  // Math.round'du ve yuvarlama pencereyi İKİ UÇTAN daraltabiliyordu, yani
  // problemin ilk/son milisaniyesindeki log ya da trace pivottan düşerdi.
  // Her iki üretici de probWindow'u DOĞRUDAN alıp floor/ceil uyguluyor,
  // dolayısıyla ms'e önceden çevrilmiş bir ara değere hiç ihtiyaç yok —
  // o değişkenlerin varlığı kusurun kendisiydi.
  //
  // v0.9.1381 — `service=` (yapısal kolon filtresi), `q=` DEĞİL.
  //
  // Buradaki eski şerh "q= bilinçli, v0.8.521 gerekçesi logsUrl.ts'te"
  // diyordu ve o gerekçe YANLIŞ GENELLEŞTİRİLMİŞTİ. v0.8.521 TRACE
  // ID'leriyle ilgiliydi: sunucunun id-şekilli bir `q`yi kolonla DA
  // eşlediği doğru (CH tarafında `isBareHexID` dalı, çıplak 32/16-hex
  // iğneyi trace_id/span_id kolonuna yükseltir). SERVİS ADI için o dalın
  // karşılığı YOK — `q` yalnız gövdede arar.
  //
  // Ölçüldü (lokal CH, 6h): `q=service.name:"x"` → 0 satır (HTTP 200,
  // hata yok); `service=x` → 535. Yani bu link sessizce boş dönüyordu ve
  // operatör "log yok" okuyordu. ClickHouse VARSAYILAN arka uç.
  const logsLink = logsHref({ window: probWindow, service: problem.service });
  // v0.10.419 (log arama denetimi C2) — hata pivotu: trace linki hasError
  // taşırken log linki seviye süzgeci taşımıyordu. EK link; mevcut "tüm
  // loglar" linki olduğu gibi kalır (davranış değişmez). 17 = ERROR+
  // (OTel severity-number tabanı, copilot_followup ile aynı sözleşme).
  const logsErrorLink = logsHref({ window: probWindow, service: problem.service, severity: 17 });
  const isExternal = subjectKind(problem.service, problem.kind) === 'external';

  return (
    <PageShell>
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

      {/* v0.9.983 (D5.1 / A2) — aynı gerekçe: bildirim linkinden gelen
          operatörün telefonda gördüğü İLK ekran bu (RootCausePanel + AI
          özeti solda, offender listesi sağda). */}
      <div className="pd-cols pd-cols-15">
        {/* ── Left column ── */}
        <div style={{ minWidth: 0 }}>
          <Sect title="Root cause analysis" accent>
            <div style={{ fontSize: 13, marginBottom: 8 }}>
              {isAnomaly && <span className="badge b-info" style={{ marginRight: 6 }}>ANOMALY</span>}
              <b>{problem.ruleName}</b>
            </div>
            {/* v0.10.230 (Influx D5) — dış kaynak öznesinde topoloji/servis
                tabanlı analiz yok; kanıt zinciri (metrik şeridi, trace'ler,
                pod'lar, log imzaları) D4'ün yazdığı hipotezden çizilir. */}
            {isExternal
              ? <ExternalEvidencePanel problem={problem} window={probWindow} />
              : <RootCausePanel problemId={problem.id} service={problem.service} window={probWindow}
                  onLoaded={rc => setRcRollouts(rc?.hypothesis?.deep?.rollouts ?? [])} />}
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

          {/* Top offenders operation_summary_5m'i SERVİS adıyla sorar; dış
              öznede boş tablo "suçlu yok" diye okunurdu. */}
          {!isExternal && <ProblemOffenders problem={problem} />}

          <Sect title="Problem timeline">
            <ul className="pb-tl">
              {rcRollouts.map(ev => (
                <li key={`${ev.clusterId}/${ev.namespace}/${ev.workload}@${ev.revision}`} className={ev.band === 'high' ? 'warn' : ''}>
                  <b>Rollout</b>{' '}
                  <Link to={rolloutEvidenceHref(ev)} className="mono" title={ev.reason}>
                    {ev.namespace}/{ev.workload}{ev.imageTag ? ` → ${ev.imageTag}` : ''}
                  </Link>
                  {ev.matchedBy === 'pod' && <span className="badge b-warn" style={{ marginLeft: 6 }}>POD</span>}
                  <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8 }}>
                    {fmtStartedTs(ev.startedAtNs)}
                  </span>
                </li>
              ))}
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
                  (probWindow) zaten hesaplı, yalnız logs/traces
                  linklerine konuyordu. */}
              <SubjectLink service={problem.service} subjectKind={problem.kind}
                href={serviceHref(problem.service, { range: probWindow })}
                className={`pb-pill${problem.status === 'open' ? ' err' : ''}`}
                style={{ textDecoration: 'none', color: 'var(--accent2)' }} />
              {/* v0.9.740 (operatör): ug/sy ekip rozetleri problem
                  detayında da.
                  v0.9.1345 — kaynak DEĞİŞTİ. Eskiden tek kaynak katalogdu
                  ("Problem.ownerTeam boş kalabiliyor" gerekçesiyle), ama
                  katalog SERVİS ADIYLA anahtarlı: db konularının orada
                  satırı yok, dolayısıyla db alarmları sahipsiz görünüyordu.
                  Artık problemin kendi alanı önce (sunucuda türetiliyor),
                  katalog yalnız yedek. Türetilmiş değer ≈ ile işaretli. */}
              <ProblemTeamChips problem={problem} />
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
            {/* v0.9.1339 (entity-model Faz 4b) — bu bölümün TAMAMI bir
                SERVİS ADI varsayıyor: /logs `service.name:"…"`, /traces
                `?service=`, /service-map `?focus=`. Özne bir veritabanı
                örneğiyse (db-capacity alarmları) üçü de BOŞ açılır ve
                operatör bunu "olay yok" diye okur — v0.9.1331'in
                "boş liste yanlış cevaptır" dersinin aynısı.
                Ölçüldü 2026-08-24: receiver instance'ı hiçbir
                spans.service_name ile eşleşmiyor (kesişim 0 satır).
                Çalışmayan dört link yerine NEDEN olmadığını söyleyen tek
                satır. */}
            {subjectKind(problem.service, problem.kind) !== 'service' ? (
              <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                {isExternal
                  ? 'Bu alarmın öznesi bir dış metrik kaynağı serisi, bir servis değil — ilgili trace/pod/log kanıtı yukarıdaki kanıt zincirinde.'
                  : 'Bu alarmın öznesi bir veritabanı örneği, bir servis değil — log/trace/servis-haritası pivotları bir servis adı gerektiriyor.'}
              </div>
            ) : (<>
            <SignalLink to={logsLink} label="≡ Logs" sub="service, problem window" />
            <SignalLink to={logsErrorLink} label="≡ Logs (yalnız hatalar)" sub="service, problem window, severity ≥ ERROR" />
            {/* v0.8.512 (perf raporu #12) — pivot problem penceresini
                taşır: logs ile aynı custom range, yoksa /traces global
                range'iyle açılıp problemle ilgisiz trace gösteriyordu. */}
            {/* v0.8.585 — Operator-reported: rootOnly default'u TRUE
                olduğundan hata izleri (çoğu non-root span) boş
                listeleniyordu; hasError linki root filtresini kapatır. */}
            {/* v0.9.1356 — el-yapımı `custom:` dizesi aile üreticisine indi.
                Pencere PROBLEMİN penceresi (probWindow), yani bu sayfanın
                öznesinin penceresi — kardeş üç link (Logs / Slow traces /
                Service) zaten aynısını taşıyor. Yan etkisi bir kusuru da
                kapattı: elle kurulan hâl logsFrom/logsTo'yu Math.round ile
                üretiyordu ve yuvarlama pencereyi İKİ UÇTAN yarım ms
                daraltabiliyordu; üretici floor/ceil uyguluyor. */}
            <SignalLink to={tracesPivotHref({
              window: probWindow, service: problem.service,
              hasError: true, rootOnly: false,
            })} label="⋮ Error traces" sub="service, problem window" />
            {/* v0.9.961 (UX denetimi G4/Ö7) — GECİKME alarmının kendi
                pivotu. Tek trace linki `hasError=true` sabitliydi ve bir
                p99-eşik alarmında yavaşlık çoğu zaman HATASIZ span'lerde
                olur: operatör boş bir "Error traces" listesi görüp yavaş
                trace'i servis→endpoint→traces turuyla arıyordu.
                Link yalnız birimi ADINDA beyan eden (`*_ms`) metriklerde
                çizilir — yanlış birimle kıstırılmış bir liste "yavaş
                trace yok" diye okunurdu (v0.6.36 sınıfı). */}
            {(() => {
              const thr = latencyThresholdMs(problem);
              if (thr === null) return null;
              return (
                // v0.9.1331 — probWindow DOĞRUDAN geçiyor (gerçek ns). Önce
                // logsFrom/logsTo (ms) geçiliyordu ve imza fromNs diyordu:
                // çalışıyordu ama ad yalan söylüyordu.
                <SignalLink
                  to={slowTracesHref(problem.service, thr, probWindow)}
                  label="◷ Slow traces"
                  sub={`≥ ${Math.round(thr)} ms (eşik), problem window`} />
              );
            })()}
            <SignalLink to={`/service-map?focus=${encodeURIComponent(problem.service)}`}
              label="◉ Service map" sub="focused" />
            </>)}
          </Sect>

          {/* v0.9.1344 — "bu problemden kimin haberi var?" Triyaj eden
              operatörün bu soruyu soracağı yer burası; cevabı bugüne
              kadar YALNIZ /events'te, elle pencere daraltarak
              bulunabiliyordu — yani pratikte hiç bulunamıyordu. */}
          <Sect title="Bildirim">
            <ProblemNotifyPanel problemId={problem.id} />
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
    </PageShell>
  );
}
