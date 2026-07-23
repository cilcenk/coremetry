import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { ChartCard } from '@/pages/service/charts/ChartCard';

// CosreChart (v0.9.183) — CoSRE sohbetinde GERÇEK-veri grafik. Copilot yanıtı
// ```chart {json}``` bloğu emit eder (guided service-health deterministik
// üretir); bu bileşen spanMetricBatch ile CANLI telemetriyi çekip mevcut uPlot
// ChartCard motoruyla çizer — LLM grafiği çizmez, sadece SPEC'i üretir.
export interface CosreChartSpec {
  title?: string;
  service: string;
  // operation (v0.9.184) — verilirse grafik tek span-name'e daralır
  // (DSL: name = "..."). Boşsa servis-geneli. Backend guided-operation
  // bundle bunu doldurur.
  operation?: string;
  agg: 'rate' | 'error_rate' | 'p50' | 'p95' | 'p99';
  unit?: string;
  rangeS?: number; // pencere (saniye); default 1800 (30 dk)
}

const AGG_META: Record<string, { field?: string; unit: string; color: string; label: string; mode: 'line' | 'area' }> = {
  rate:       { unit: ' req/s', color: 'var(--accent)', label: 'req/s',  mode: 'line' },
  error_rate: { unit: '%',      color: 'var(--err)',    label: 'errors', mode: 'area' },
  p50:        { field: 'duration_ms', unit: ' ms', color: 'var(--purple)', label: 'P50', mode: 'line' },
  p95:        { field: 'duration_ms', unit: ' ms', color: 'var(--orange)', label: 'P95', mode: 'line' },
  p99:        { field: 'duration_ms', unit: ' ms', color: 'var(--err)',    label: 'P99', mode: 'line' },
};

export function CosreChart({ spec }: { spec: CosreChartSpec }) {
  const rangeS = spec.rangeS && spec.rangeS > 0 ? spec.rangeS : 1800;
  const meta = AGG_META[spec.agg] ?? AGG_META.rate;
  const q = useQuery({
    queryKey: ['cosre-chart', spec.service, spec.operation ?? '', spec.agg, rangeS],
    queryFn: () => {
      // Canlı "son rangeS" penceresi (ns). Overview ile aynı spanMetricBatch yolu.
      const to = Date.now() * 1e6;
      const from = to - rangeS * 1e9;
      // operation verilirse DSL'e span-name conjunct'ı ekle (name kolonu,
      // http.route DEĞİL — dsl_test.go:61 ile doğrulandı).
      let dsl = `service.name = "${spec.service.replace(/"/g, '\\"')}"`;
      // operation'ı yalnız TEMİZ bir string ise ekle (v0.9.187): newline/
      // kontrol karakteri DSL'i (ParseDSL \n ile böler) bozar, string
      // olmayan bir değer .replace'te patlar — bozuksa servis-geneli çiz.
      const op =
        typeof spec.operation === 'string' &&
        spec.operation !== '' &&
        ![...spec.operation].some((c) => c.charCodeAt(0) < 32)
          ? spec.operation
          : '';
      if (op) {
        dsl += ` AND name = "${op.replace(/"/g, '\\"')}"`;
      }
      return api.spanMetricBatch({
        from, to, dsl,
        aggs: [{ name: 'v', agg: spec.agg, field: meta.field }],
      });
    },
    enabled: !!spec.service,
    staleTime: 30_000,
  });
  const status: 'loading' | 'error' | 'ready' = q.isLoading ? 'loading' : q.isError ? 'error' : 'ready';
  const series = q.data?.v ?? [];
  return (
    <div style={{ margin: '10px 0', maxWidth: 560 }}>
      <ChartCard
        title={spec.title ?? `${spec.operation || spec.service} · ${meta.label}`}
        unit={spec.unit ?? meta.unit}
        mode={meta.mode}
        status={status}
        lines={[{ series, color: meta.color, label: meta.label }]}
      />
    </div>
  );
}
