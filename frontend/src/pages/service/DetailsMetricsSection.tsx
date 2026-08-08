import { lazy, Suspense, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQueries, useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { encodeFilters } from '@/lib/urlState';
import { panelMaxDataPoints, stepForWidth } from '@/lib/chartStep';
import { LazyMount } from '@/components/LazyMount';
import { Spinner } from '@/components/Spinner';
import { metricCatalogueHref } from '@/pages/explore/urlCodec';
import { buildDetailsMetricPanels, type DetailsPanelSpec } from './detailsMetricPanels';

// DetailsMetricsSection — v0.9.784. Service /details sekmesinin OTLP
// metrik satırı.
//
// Operatör isteği (eski): "Details sayfasında da metrikler olsun".
// Details'teki her panel span türevliydi; metric_points'ten beslenen TEK
// grafik yoktu — collector'ın taşıdığı runtime/HTTP/DB metrikleri yalnız
// Explore'da ve Pods sekmesinde görünüyordu.
//
// Panel seti KATALOG-GÜDÜMLÜ (detailsMetricPanels.ts): metrik adı hardcode
// EDİLMEZ, servisin kendi kataloğundan türetilir. Kurallar ve "p95 değil
// avg" kararının gerekçesi o dosyanın başlığında.
//
// İKİ AŞAMALI GİZLEME (RuntimeCharts deseni):
//   1) aile katalogda YOK   → panel hiç kurulmaz; hiç panel yoksa bölüm
//      (ve ToC girdisi) tamamen gizli — boş kart duvarı YASAK.
//   2) panel kurulu, veri boş → CorePanel'in 'empty' kanalı + hint'te
//      metrik adı. Hata AYRI kanal: "sorgu patladı" ile "bu pencerede
//      veri yok" farklı eylem gerektirir (v0.9.774).
//
// Poll YOK: aralık değişimi zaten refetch tetikliyor, yeni bir interval
// açmak document.hidden disiplinine girecek bir maliyet olurdu.
//
// CorePanel LAZY: @grafana/* statik import edilseydi Service sayfasının
// vendor chunk'ı 35 KB → ~1 MB olurdu (Overview.tsx:26-29 ölçümü).
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// Katalog penceresi sunucuda 7 gün (metricNameLookback, repo.go). "Panel
// kurulmadı" bu yüzden "metrik yok" değil "7 gündür sessiz" de olabilir —
// boş-durum hint'i bunu söylüyor ki operatör yanlış yere bakmasın.
const CATALOG_NOTE = 'Katalog penceresi 7 gün; metrik son 7 günde görüldü, seçili aralıkta veri yok.';

/**
 * useDetailsMetricPanels — katalog sorgusu + kurulacak panel listesi.
 *
 * İKİ tüketici: bu bölüm (panelleri çizer) ve Service.tsx (ToC girdisi +
 * ?op= kapsam beyanı için "bölüm var mı" bilgisi). AYNI queryKey +
 * staleTime ⇒ React Query dedupe eder, ikinci ağ isteği YOK.
 */
export function useDetailsMetricPanels(service: string, enabled = true) {
  const q = useQuery({
    queryKey: ['details-metric-catalog', service],
    queryFn: () => api.metricNamesSearch(service, '', 500),
    enabled: enabled && !!service,
    // Sunucu tarafı katalog cache'i ile hizalı; sekme gidip gelmesi
    // yeniden istek üretmesin.
    staleTime: 60_000,
  });
  const panels = useMemo(
    () => buildDetailsMetricPanels(q.data?.names ?? []),
    [q.data],
  );
  return { panels, isLoading: q.isPending && enabled && !!service };
}

export function DetailsMetricsSection({ service, rangeNs, onZoom, onZoomReset }: {
  service: string;
  /** Parent'ın çözülmüş penceresi (unix ns) — RQ anahtarı hizalı. */
  rangeNs: { from: number; to: number };
  onZoom?: (fromSec: number, toSec: number) => void;
  onZoomReset?: () => void;
}) {
  const navigate = useNavigate();
  const { panels } = useDetailsMetricPanels(service);
  const { from, to } = rangeNs;

  // Adım: iki-kolonlu grid bütçesi (px ≈ mdp×2), rung'a snap'li →
  // sunucu cache anahtarının kardinalitesi sınırlı (v0.8.270).
  const step = useMemo(
    () => stepForWidth(Math.max(1, (to - from) / 1e9), panelMaxDataPoints(2) * 2),
    [from, to],
  );
  const filters = useMemo(
    () => encodeFilters([{ k: 'service.name', op: '=', v: [service] }]),
    [service],
  );
  const xRange = useMemo(() => ({ from: from / 1e9, to: to / 1e9 }), [from, to]);

  // Panel başına DEĞİL metrik başına sorgu (her metrik ayrı seri).
  const flat = useMemo(
    () => panels.flatMap(p => p.metrics.map(metric => ({ panel: p, metric }))),
    [panels],
  );
  const results = useQueries({
    queries: flat.map(({ panel, metric }) => ({
      queryKey: ['details-metric', service, metric.name, panel.agg,
        panel.groupBy ?? '', from, to, step],
      queryFn: ({ signal }: { signal: AbortSignal }) => api.metricQueryFull({
        name: metric.name,
        agg: panel.agg,
        // groupBy YALNIZ kural tablosunda yazan ailede — geri kalan her
        // panel tek çizgi (kırılım kardinalitesi bilinmiyor).
        groupBy: panel.groupBy,
        filters,
        from, to, step,
      }, signal),
      staleTime: 60_000,
    })),
  });

  // Sorgu sonuçlarını panellere geri dağıt (flat sırası = panels sırası).
  const cards = useMemo(() => {
    let i = 0;
    return panels.map(panel => {
      const slice = panel.metrics.map(metric => ({ metric, r: results[i++] }));
      const loading = slice.some(x => x?.r?.isPending);
      const errored = slice.find(x => x?.r?.isError);
      const items = slice.map(({ metric, r }) => ({
        name: metric.label,
        series: r?.data?.series ?? [],
      }));
      const empty = !loading && !errored && items.every(it => it.series.length === 0);
      const rowsCapped = slice.some(x => x?.r?.data?.rowsCapped);
      return { panel, items, loading, errored, empty, rowsCapped };
    });
  }, [panels, results]);

  // Hiç panel kurulamadı → bölüm YOK (başlık dahil). Katalog yüklenirken
  // de panels boştur: flash yerine sessiz gecikme (RuntimeCharts emsali).
  if (panels.length === 0) return null;

  return (
    <>
      <div className="dtl-sech" id="dtl-metrics">Metrikler
        <span className="badge b-gray" style={{ textTransform: 'none', letterSpacing: 0 }}>
          tüm servis
        </span>
      </div>
      <div className="ov-grid ov-cols-2" style={{ marginBottom: 16 }}>
        {cards.map(({ panel, items, loading, errored, empty, rowsCapped }) => (
          <LazyMount key={panel.key} minHeight={240}>
            <Suspense fallback={<div style={{ height: 240, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
              <CorePanelMultiLazy
                title={panelTitle(panel)}
                storageKey={`dtl-metric-${panel.key}`}
                height={200}
                unit={panel.unit}
                xRange={xRange}
                items={items}
                loading={loading}
                error={errored
                  ? (errored.r?.error instanceof Error ? errored.r.error.message : 'Metrik sorgusu başarısız')
                  : undefined}
                emptyReason={empty ? 'Bu pencerede veri yok' : undefined}
                emptyHint={empty
                  ? `${panel.metrics.map(x => x.name).join(' · ')} — ${CATALOG_NOTE}`
                  : undefined}
                note={rowsCapped ? '⚠ satır tavanı doldu — seriler eksik olabilir' : null}
                queryText={queryText(panel, service, step)}
                // Doorway: metrik KAYNAKLI Explore sorgusu. MetricPanel
                // descriptor'ı KULLANILMAZ — seedFromMetricDescriptor
                // katalog metriğini span sorgusuna çevirir ve operatör
                // yanlış Explore'a düşer.
                onExpandClick={() => navigate(
                  metricCatalogueHref(panel.metrics[0].name, { service, agg: panel.agg }))}
                onZoom={onZoom}
                onZoomReset={onZoomReset}
                // Sayfadaki TÜM CorePanel'lerle aynı crosshair grubu
                // (Overview + Details/Performance). Bilinen ayrışma: v1
                // MultiLineChart yolu syncKey'e '-ms' ekliyor, o yüzden
                // eski motorda ayrı grupta kalır — dokunulmadı.
                syncKey={`service:${service}`}
              />
            </Suspense>
          </LazyMount>
        ))}
      </div>
    </>
  );
}

// Başlık metriğin ADINI taşır: "hangi metriği çiziyor" sorusu panelin
// üstünde cevaplanmalı (v0.9.774'ün "ne çizdiğini söyle" dersi).
function panelTitle(p: DetailsPanelSpec): string {
  const names = p.metrics.map(x => x.name).join(' · ');
  return `${p.title} — ${names}`;
}

function queryText(p: DetailsPanelSpec, service: string, step: number): string {
  return p.metrics
    .map(x => `${p.agg}(${x.name})${p.groupBy ? ` by (${p.groupBy})` : ''}`
      + `, service.name="${service}", step=${step}s`)
    .join('\n');
}
