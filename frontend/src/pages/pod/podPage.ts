// podPage.ts — v0.10.160: /pod sayfasının saf çekirdekleri (A anatomisi;
// mockup scratchpad/pod-detail/option-A.html, brief §3-§4). Sözleşme
// podPage.test.ts'te pinli. Hiçbiri fetch etmez; sayfa bileşenleri bunları
// mevcut uçlara (spanMetricBatch, /api/traces, /api/entity, /api/clusters/pods)
// bağlar — YENİ uç yok.
import type { TracesParams } from '@/lib/api';
import type { EntityRecord, ClusterPodRow } from '@/lib/types';

/** ?spans= — trace listesi süzgeci. errors varsayılan ve URL'de YAZILMAZ. */
export type SpansMode = 'errors' | 'slow' | 'all';

export function parseSpansParam(raw: string | null): SpansMode {
  return raw === 'slow' || raw === 'all' ? raw : 'errors';
}

/** Yerinde yazar (replace:true ile kullanılır); yabancı paramlara dokunmaz. */
export function writeSpansParam(p: URLSearchParams, mode: SpansMode): URLSearchParams {
  if (mode === 'errors') p.delete('spans'); else p.set('spans', mode);
  return p;
}

type Pt = { time: number; value: number };

// Kova adımı (s): sunucu zarfı `stepSeconds` verirse o; yoksa ardışık
// noktaların en küçük pozitif aralığı. «Sonraki noktaya uzaklık» DEĞİL (inceleme #4):
// sunucu kovaları SEYREK yayar (GROUP BY, WITH FILL yok) — bir istek + bir
// saat sessizlik, son kovayı bütün boşlukla çarpardı. rate = count/step
// olduğundan Σ rate·step kesindir. İki noktadan az seri → adım BİLİNMİYOR.
function bucketStep(points: Pt[], stepSec?: number): number | null {
  if (stepSec !== undefined && stepSec > 0) return stepSec;
  if (points.length < 2) return null;
  // En KÜÇÜK pozitif Δt: kovalar adımın katlarına oturur, boşluklar adımın
  // katı olur → minimum, kafes adımının en güvenli kestirimi (medyan iki
  // noktalı seride boşluğu seçebilir).
  let m = Infinity;
  for (let i = 1; i < points.length; i++) {
    const d = (points[i].time - points[i - 1].time) / 1e9;
    if (d > 0 && d < m) m = d;
  }
  return Number.isFinite(m) ? m : null;
}

// Zamana göre eşle (inceleme #3): avg/p95 AYRI bir batch'ten gelir
// (messaging.system != kafka) ve kovalar seyrek — indeksle eşlemek kafka
// taşıyan pod'da yanlış kovaları çiftlerdi.
function byTime(points: Pt[]): Map<number, number> {
  const m = new Map<number, number>();
  for (const p of points) m.set(p.time, p.value);
  return m;
}

export interface WindowTotals {
  /** pencere toplam çağrı (Σ rate·Δt); seri yetersizse null */
  calls: number | null;
  /** rate-ağırlıklı hata yüzdesi; trafik 0 ise null (0 DEĞİL) */
  errPct: number | null;
  /** rate-ağırlıklı ortalama süre (ms); trafik 0 ise null */
  avgMs: number | null;
}

/**
 * KPI şeridi RED grafiklerinin serilerinden türer — ayrı bir "pencere
 * toplamı" ucu YOK (brief §3; tasarımcı riski: "seriden toplama yapılmalı,
 * ayrı endpoint eklenmemeli"). error_rate/avg noktaları rate ile ZAMANA göre
 * eşlenir; o kovada nokta yoksa 0 (hata) / ağırlığa girmez (avg).
 */
export function windowTotals(rate: Pt[], errorRate: Pt[], avg: Pt[], stepSec?: number): WindowTotals {
  const step = bucketStep(rate, stepSec);
  if (step === null) return { calls: null, errPct: null, avgMs: null };
  const er = byTime(errorRate), av = byTime(avg);
  let calls = 0, errs = 0, wAvg = 0, wAvgCalls = 0;
  for (const p of rate) {
    const c = Math.max(0, p.value) * step;
    calls += c;
    errs += c * Math.max(0, er.get(p.time) ?? 0) / 100;
    const a = av.get(p.time);
    if (a !== undefined) { wAvg += c * Math.max(0, a); wAvgCalls += c; }
  }
  if (calls <= 0) return { calls: 0, errPct: null, avgMs: null };
  return { calls, errPct: (100 * errs) / calls, avgMs: wAvgCalls > 0 ? wAvg / wAvgCalls : null };
}

/** «Yavaş» çipinin eşiği — p95 serisinin rate-ağırlıklı (zamana göre eşlenmiş) ortalaması; rate yoksa düz ortalama. */
export function windowP95(p95: Pt[], rate: Pt[]): number | null {
  if (p95.length === 0) return null;
  const rt = byTime(rate);
  let w = 0, s = 0;
  for (const p of p95) {
    const r = rt.get(p.time);
    const wt = r !== undefined && r > 0 ? r : 0;
    w += wt; s += wt * p.value;
  }
  if (w > 0) return s / w;
  return p95.reduce((a, p) => a + p.value, 0) / p95.length;
}

export const POD_TRACE_PAGE = 50;

export interface PodTraceCtx {
  pod: string;
  from: number;
  to: number;
  /** span tarafı cluster değeri (?cluster= — boşsa anahtar yazılmaz) */
  cluster: string;
  service: string;
  mode: SpansMode;
  /** «Yavaş» eşiği (ms); null = henüz bilinmiyor → yavaş modu SORGU ÜRETMEZ */
  p95Ms: number | null;
}

/**
 * /api/traces parametreleri. ExceptionPodsPanel'in k8s.pod.name süzgeciyle
 * aynı sözleşme (features/anomalies/ExceptionPodsPanel.tsx:65-66). Sayfa 50,
 * count=skip (toplam sayım yok — «daha fazla var» hasMore'dan). Yavaş modu
 * p95 gelmeden null döner: sabit bir ms'e düşmek yanlış kapsam olurdu.
 */
export function podTraceParams(ctx: PodTraceCtx, offset: number): TracesParams | null {
  if (ctx.mode === 'slow' && (ctx.p95Ms === null || !(ctx.p95Ms > 0))) return null;
  const p: TracesParams = {
    filters: JSON.stringify([{ k: 'k8s.pod.name', op: '=', v: [ctx.pod] }]),
    from: ctx.from, to: ctx.to,
    limit: POD_TRACE_PAGE, offset, count: 'skip',
  };
  if (ctx.service) p.service = ctx.service;
  if (ctx.cluster) p.cluster = ctx.cluster;
  if (ctx.mode === 'errors') p.hasError = true;
  if (ctx.mode === 'slow') p.minMs = Math.round(ctx.p95Ms as number);
  return p;
}

export interface SiblingRow {
  rec: EntityRecord;
  name: string;
  /** clusterPods (topk 500) listesinde bulundu mu — bulunmadıysa faz/restart/cpu/mem BİLİNMİYOR */
  known: boolean;
  phase: string | null;
  restarts: number | null;
  cpuCores: number | null;
  memBytes: number | null;
  lastTermReason: string | null;
}

/**
 * Entity kardeşleri (sunucu ≤50) × /api/clusters/pods satırları — namespace+ad
 * ile (pod adı cluster-benzersiz değil; liste zaten tek cluster'ın). Listede
 * olmayan pod'da alanlar null: kesik liste (topk 500) «yok»u kanıtlamaz.
 * restartsUnknown → restarts null (0 değil; v0.9.371 sözleşmesi).
 */
export function joinSiblings(siblings: EntityRecord[], pods: ClusterPodRow[] | null | undefined): SiblingRow[] {
  const byKey = new Map<string, ClusterPodRow>();
  for (const r of pods ?? []) byKey.set(`${r.namespace}/${r.pod}`, r);
  return siblings.map(rec => {
    const r = byKey.get(`${rec.namespace ?? ''}/${rec.name}`);
    if (!r) return { rec, name: rec.name, known: false, phase: null, restarts: null, cpuCores: null, memBytes: null, lastTermReason: null };
    return {
      rec, name: rec.name, known: true,
      phase: r.phase ?? null,
      restarts: r.restartsUnknown ? null : (r.restarts ?? null),
      cpuCores: r.cpuCores, memBytes: r.memBytes,
      lastTermReason: r.lastTermReason ?? null,
    };
  });
}
