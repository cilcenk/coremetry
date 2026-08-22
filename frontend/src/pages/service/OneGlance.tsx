import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { panelMaxDataPoints } from '@/lib/chartStep';
import { TimeChart, type TimeChartSeries } from '@/components/charts/TimeChart';
import { Chip } from '@/components/ui/Chip';
import { Spinner } from '@/components/Spinner';
import type { EndpointRow, OperationSummary, TimeRange } from '@/lib/types';

// OneGlance — v0.9.1262: servis Overview'unun "daha az bilgi, daha
// entegre" önerisi, ?newpage=1 bayrağı arkasında GERÇEK veriyle
// (v0.9.743 bayraklı-motor geçişinin emsali: operatör tarayıcıda
// karşılaştırır, onaylarsa varsayılan olur; varsayılan sayfa bu
// commit'te DEĞİŞMEZ).
//
// Veri sözleşmesi: SIFIR yeni uç. RED, klasik Overview'un
// 'service-overview-red' sorgusunun TA KENDİSİ (aynı anahtar → bayrakla
// gidip-gelmek cache'ten; kıyas ücretsiz). KPI/şerit/ray mevcut
// props'lardan (info, operations, endpoints) türetilir.
//
// Karar cümlesi DETERMİNİSTİK (LLM yok): pencere içi Δ'lardan kurulur;
// "✨ Neden?" mevcut coremetry:ai-ask köprüsüyle CoSRE'ye sorar.

const envDSL = (env: string) =>
  env ? ` and deployment.environment.name = "${env.replace(/"/g, '\\"')}"` : '';

type Pt = { t: number; v: number | null };
const lastVal = (s?: Pt[] | null) => {
  if (!s) return undefined;
  for (let i = s.length - 1; i >= 0; i--) { const v = s[i]?.v; if (v != null) return v; }
  return undefined;
};
const firstVal = (s?: Pt[] | null) => s?.find(p => p.v != null)?.v ?? undefined;

// deltaPct — pencere başı → sonu yüzde değişim; taban ~0 ise null
// (0'dan artışı %∞ diye bağırmak yalan sınıfı).
export function deltaPct(first?: number, last?: number): number | null {
  if (first == null || last == null || !(first > 1e-9)) return null;
  return ((last - first) / first) * 100;
}

// verdictOf — deterministik tek-cümle karar. SAF (vitest'te).
export function verdictOf(p99d: number | null, errNow: number | undefined, worstPath: string): {
  tone: 'ok' | 'warn' | 'err'; text: string;
} {
  const errHigh = (errNow ?? 0) >= 5;
  const errWarn = (errNow ?? 0) >= 1;
  const p99Up = (p99d ?? 0) >= 25;
  if (errHigh || (p99Up && errWarn)) {
    return { tone: 'err', text: `Bozulma var — hata %${(errNow ?? 0).toFixed(2)}${p99Up ? `, p99 pencere içinde %${Math.round(p99d!)} yükseldi` : ''}${worstPath ? ` · en kötü uç ${worstPath}` : ''}.` };
  }
  if (p99Up || errWarn) {
    return { tone: 'warn', text: `${p99Up ? `p99 pencere içinde %${Math.round(p99d!)} yükseldi` : `hata %${(errNow ?? 0).toFixed(2)}`}${worstPath ? ` · en kötü uç ${worstPath}` : ''}.` };
  }
  return { tone: 'ok', text: 'Bu pencerede sağlıklı — hata ve p99 tabanında.' };
}

export function ServiceOneGlance({ service, range, windowNs, operations, endpoints = [], onZoom, onZoomReset, env = '' }: {
  service: string;
  range: TimeRange;
  windowNs?: { from: number; to: number };
  operations: OperationSummary[];
  endpoints?: EndpointRow[];
  onZoom?: (fromS: number, toS: number) => void;
  onZoomReset?: () => void;
  env?: string;
}) {
  const [sp, setSp] = useSearchParams();
  const computed = useMemo(() => timeRangeToNs(range), [range]);
  const { from, to } = windowNs ?? computed;
  const redMdp = panelMaxDataPoints(3);

  // Klasik Overview'un sorgusuyla AYNI anahtar — cache ortak.
  const seriesQ = useQuery({
    queryKey: ['service-overview-red', service, from, to, redMdp, env],
    queryFn: () => api.spanMetricBatch({
      from, to, maxDataPoints: redMdp,
      rateWindow: 180,
      dsl: `service.name = "${service.replace(/"/g, '\\"')}"` + envDSL(env),
      aggs: [
        { name: 'rate', agg: 'rate' },
        { name: 'error_rate', agg: 'error_rate' },
        { name: 'p99', agg: 'p99', field: 'duration_ms' },
        { name: 'p95', agg: 'p95', field: 'duration_ms' },
        { name: 'p50', agg: 'p50', field: 'duration_ms' },
      ],
    }),
    select: (d: { stepSeconds: number; series: Record<string, import('@/lib/types').SpanMetricSeries[] | null> }) => d.series,
    enabled: !!service,
    staleTime: 30_000,
  });

  const s = seriesQ.data;
  const rate = s?.rate?.[0]?.points as Pt[] | undefined;
  const errR = s?.error_rate?.[0]?.points as Pt[] | undefined;
  const p99 = s?.p99?.[0]?.points as Pt[] | undefined;

  const rateNow = lastVal(rate);
  const errNow = lastVal(errR);
  const p99Now = lastVal(p99);
  const p99d = deltaPct(firstVal(p99), p99Now);
  const errd = deltaPct(firstVal(errR), errNow);

  // En kötü uç: impact (calls × p99 × hata) — Endpoints sayfasının formülü.
  const worst = useMemo(() => {
    let w: EndpointRow | undefined;
    let best = -1;
    for (const e of endpoints) {
      const score = e.calls * e.p99Ms * (1 + e.errorRate / 100);
      if (score > best) { best = score; w = e; }
    }
    return w;
  }, [endpoints]);

  const verdict = verdictOf(p99d, errNow, worst ? worst.path : '');

  // Birleşik grafik: hacim (istek/s) çubuk + hata/s kırmızı overlay +
  // p99 sağ eksende — LogsHistogram v0.9.218 felsefesi.
  const unified = useMemo(() => {
    if (!rate) return null;
    const times = rate.map(p => Math.round(p.t));
    const vol = rate.map(p => p.v);
    const errs = rate.map((p, i) => {
      const e = errR?.[i]?.v;
      return p.v != null && e != null ? p.v * (e / 100) : null;
    });
    const p99v = (p99 ?? []).map(p => p.v);
    const series: TimeChartSeries[] = [
      { key: 'vol', label: 'istek/s', data: vol, type: 'bar', axis: 'left',
        color: 'color-mix(in srgb, var(--text3) 45%, transparent)' },
      { key: 'err', label: 'hata/s', data: errs, type: 'bar', axis: 'left', color: 'var(--err)' },
      { key: 'p99', label: 'p99 ms', data: p99v, type: 'line', axis: 'right',
        color: 'var(--purple)', width: 1.8 },
    ];
    return { times, series };
  }, [rate, errR, p99]);

  const ask = (q: string) => window.dispatchEvent(new CustomEvent('coremetry:ai-ask', { detail: { question: q } }));
  const tabHref = (tab: string) => {
    const p = new URLSearchParams(sp);
    p.set('tab', tab); p.delete('newpage');
    return `/service?${p.toString()}`;
  };

  const toneBg: Record<string, string> = {
    ok: 'color-mix(in srgb, var(--ok) 6%, var(--bg1))',
    warn: 'color-mix(in srgb, var(--warn) 7%, var(--bg1))',
    err: 'color-mix(in srgb, var(--err) 7%, var(--bg1))',
  };
  const toneDot: Record<string, string> = { ok: 'var(--ok)', warn: 'var(--warn)', err: 'var(--err)' };

  const kpi = (label: string, value: string, sub: string, subColor: string, href?: string) => (
    <Link to={href ?? '#'} style={{
      background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 7,
      padding: '10px 12px 8px', textDecoration: 'none', color: 'var(--text)', display: 'block',
    }}>
      <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '.06em', color: 'var(--text3)', fontWeight: 600 }}>{label}</div>
      <div className="mono" style={{ fontSize: 24, fontWeight: 700, lineHeight: 1.15, marginTop: 2 }}>{value}</div>
      <div style={{ fontSize: 10.5, marginTop: 1, color: subColor }}>{sub}</div>
    </Link>
  );

  return (
    <div>
      {/* Bayrak şeffaflığı + geri dönüş — yüzen şerit değil, düz satır. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '2px 0 10px', fontSize: 11, color: 'var(--text3)' }}>
        <Chip size="sm" tone="accent">yeni tasarım · ?newpage=1</Chip>
        <button className="sec" style={{ fontSize: 10.5, padding: '2px 8px' }}
          onClick={() => setSp(prev => { const p = new URLSearchParams(prev); p.delete('newpage'); return p; }, { replace: true })}>
          klasik Overview'a dön
        </button>
      </div>

      {/* Karar cümlesi — deterministik; ✨ CoSRE'ye köprü. */}
      <div style={{
        display: 'flex', gap: 10, padding: '10px 12px', borderRadius: 6, marginBottom: 12,
        background: toneBg[verdict.tone],
        border: `1px solid color-mix(in srgb, ${toneDot[verdict.tone]} 35%, var(--border))`,
      }}>
        <span style={{ width: 8, height: 8, borderRadius: '50%', background: toneDot[verdict.tone], marginTop: 5, flex: 'none' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: 13.5 }}>{verdict.text}</div>
          <div style={{ fontSize: 10.5, color: 'var(--text3)', marginTop: 2 }}>
            pencere-içi Δ'lardan türetildi (LLM değil) — derin analiz için ✨
          </div>
        </div>
        <button className="sec" style={{ fontSize: 11, padding: '3px 10px', color: 'var(--accent2)', alignSelf: 'flex-start' }}
          onClick={() => ask(`${service} servisinde neler oluyor? p99 ve hata trendini açıkla.`)}>
          ✨ Neden?
        </button>
      </div>

      {/* 4 pivot-KPI */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(190px, 1fr))', gap: 10, marginBottom: 12 }}>
        {kpi('İstek', rateNow != null ? `${fmtNum(rateNow * 60)}/dk` : '—', 'giriş span hızı', 'var(--text3)', tabHref('operations'))}
        {kpi('Hata', errNow != null ? `%${errNow.toFixed(2)}` : '—',
          errd != null ? `${errd >= 0 ? '▲' : '▼'} %${Math.abs(errd).toFixed(0)} pencere içinde` : '—',
          (errNow ?? 0) >= 1 ? 'var(--err)' : 'var(--text3)', tabHref('logs'))}
        {kpi('P99', p99Now != null ? `${fmtNum(p99Now)} ms` : '—',
          p99d != null ? `${p99d >= 0 ? '▲' : '▼'} %${Math.abs(p99d).toFixed(0)} pencere içinde` : '—',
          (p99d ?? 0) >= 25 ? 'var(--err)' : 'var(--text3)', tabHref('operations'))}
        {kpi('En kötü uç', worst ? worst.path.split('/').slice(-1)[0] || worst.path : '—',
          worst ? `p99 ${fmtNum(worst.p99Ms)} ms · %${worst.errorRate.toFixed(1)} hata` : 'endpoint verisi yok',
          'var(--text3)', `/endpoints?service=${encodeURIComponent(service)}`)}
      </div>

      {/* Birleşik RED */}
      <div style={{ background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 8, padding: '12px 14px 8px', marginBottom: 12 }}>
        <div style={{ display: 'flex', gap: 12, fontSize: 10.5, color: 'var(--text2)', marginBottom: 6 }}>
          <span style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '.06em', color: 'var(--text3)', fontWeight: 600 }}>RED — birleşik</span>
          <span style={{ marginLeft: 'auto', fontFamily: 'ui-monospace,monospace', fontSize: 10, color: 'var(--text3)' }}>
            {onZoom ? 'sürükle = zaman seç · çift tık = geri' : ''}
          </span>
        </div>
        {seriesQ.isLoading && <Spinner />}
        {seriesQ.isError && <div style={{ fontSize: 12, color: 'var(--err)' }}>RED serileri yüklenemedi.</div>}
        {unified && unified.times.length > 0 && (
          <TimeChart times={unified.times} series={unified.series} height={170}
            rightUnit=" ms"
            onBrush={onZoom ? (a, b) => onZoom(a, b) : undefined}
            onZoomReset={onZoomReset} hideLegend={false} />
        )}
      </div>

      {/* Şu an önemli — yalnız veri varsa; en fazla 2 kart (v1). */}
      {(worst || operations.length > 0) && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))', gap: 10, marginBottom: 12 }}>
          {worst && (
            <div style={{ background: 'var(--bg1)', border: '1px solid var(--border)', borderLeft: '3px solid var(--err)', borderRadius: 8, padding: '11px 13px' }}>
              <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '.06em', color: 'var(--text2)', fontWeight: 600, marginBottom: 5 }}>En kötü endpoint</div>
              <div className="mono" style={{ fontSize: 12.5 }}>{worst.method ? `${worst.method} ` : ''}{worst.path}</div>
              <div className="mono" style={{ fontSize: 11, color: 'var(--text3)', marginTop: 3 }}>
                p99 {fmtNum(worst.p99Ms)} ms · %{worst.errorRate.toFixed(1)} hata · {fmtNum(worst.calls)} çağrı
              </div>
              <div style={{ fontSize: 11.5, marginTop: 4 }}>
                <Link to={`/endpoints?service=${encodeURIComponent(service)}`} style={{ color: 'var(--accent2)', textDecoration: 'none' }}>Endpoints →</Link>
              </div>
            </div>
          )}
          {operations.length > 0 && (
            <div style={{ background: 'var(--bg1)', border: '1px solid var(--border)', borderLeft: '3px solid var(--warn)', borderRadius: 8, padding: '11px 13px' }}>
              <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '.06em', color: 'var(--text2)', fontWeight: 600, marginBottom: 5 }}>En yavaş operasyon</div>
              <div className="mono" style={{ fontSize: 12.5 }}>{[...operations].sort((a, b) => b.p99DurationMs - a.p99DurationMs)[0].name}</div>
              <div style={{ fontSize: 11.5, marginTop: 4 }}>
                <Link to={tabHref('operations')} style={{ color: 'var(--accent2)', textDecoration: 'none' }}>Operations →</Link>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Pivot rayı */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(190px, 1fr))', gap: 10 }}>
        {[
          { l: 'Endpoints', n: `${endpoints.length} uç`, href: `/endpoints?service=${encodeURIComponent(service)}` },
          { l: 'Loglar', n: '', href: tabHref('logs') },
          { l: 'Topoloji', n: '', href: tabHref('topology') },
          { l: "Pod'lar", n: '', href: tabHref('pods') },
        ].map(r => (
          <Link key={r.l} to={r.href} style={{
            display: 'flex', alignItems: 'center', gap: 10, padding: '11px 14px',
            textDecoration: 'none', color: 'var(--text)', background: 'var(--bg1)',
            border: '1px solid var(--border)', borderRadius: 7, fontSize: 12.5,
          }}>
            {r.l}<span className="mono" style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>{r.n}</span>
          </Link>
        ))}
      </div>
    </div>
  );
}
