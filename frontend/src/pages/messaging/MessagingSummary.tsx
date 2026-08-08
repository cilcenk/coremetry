import { lazy, Suspense, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { LazyMount } from '@/components/LazyMount';
import { Spinner } from '@/components/Spinner';
import type { MessagingSeries, MessagingSeriesPoint, SpanMetricSeries } from '@/lib/types';

// MessagingSummary — /messaging sayfasının üst şeridi (v0.9.814).
//
// Sayfa bugüne dek SADECE tabloydu: her satır pencerenin TOPLAMINI
// gösteriyordu, yani "ne zaman bozuldu" sorusu ancak satır açılıp
// drawer'a bakılarak — destination destination — cevaplanabiliyordu.
// Filo genelinde "üretim tüketimi geçti mi", "gecikme ne zaman tırmandı",
// "hata dalgası nerede" soruları hiç cevaplanamıyordu.
//
// TEK ÇAĞRI (/api/messaging/series) hem şeridi hem üç grafiği besler ve
// KPI'lar sunucuda serinin KENDİ satırlarından türetilir — şerit ile
// grafiklerin farklı sayı göstermesi imkânsız.
//
// CorePanel LAZY: @grafana/* statik import edilse /messaging'in vendor
// chunk'ı ~35 KB'den ~1 MB'ye çıkardı (Overview.tsx:26-29 ölçümü).
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// toSeries — MessagingSeriesPoint[] → CorePanel'in beklediği tek seri.
// time unix NANOSANİYE (SpanMetricSeries sözleşmesi; dataFrame köprüsü
// /1e6 ile ms'ye indiriyor).
function toSeries(
  points: MessagingSeriesPoint[],
  pick: (p: MessagingSeriesPoint) => number,
  label: string,
): SpanMetricSeries[] {
  return [{
    groupKey: [label],
    points: points.map(p => ({ time: p.timeS * 1e9, value: pick(p) })),
  }];
}

// KPI karosu — ev dili (.card.ov-kpi + .ov-kpi-accent + .ov-lab/.ov-val).
// Service Overview'daki KpiTile'ın dışa açılmamış olması yüzünden burada
// aynı SINIFLARLA yeniden kuruluyor; elle uydurulmuş bir stil DEĞİL, aynı
// token seti. Delta yok: bu şerit pencerenin kendisini anlatıyor,
// compare=prior tablonun rozetlerinde yaşıyor.
function MsgKpi({ lab, val, unit, accent, note }: {
  lab: string;
  val: string;
  unit?: string;
  accent: string;
  note?: string;
}) {
  return (
    <div className="card ov-kpi" title={note}>
      <div className="ov-kpi-accent" style={{ background: accent }} />
      <div className="ov-lab">{lab}</div>
      <div className="ov-val">{val}{unit && <span className="ov-unit">{unit}</span>}</div>
    </div>
  );
}

// fmtRate — sub-10 oranlar bir ondalık tutar ki damlayan bir topic "0"
// diye okunmasın (tablodaki fmtPerMin ile aynı kural).
function fmtRate(v: number): string {
  if (!Number.isFinite(v)) return '—';
  return v < 10 ? v.toFixed(1) : Math.round(v).toLocaleString();
}

export function MessagingSummary({ fromNs, toNs, system, search }: {
  fromNs: number;
  toNs: number;
  /** Tablodaki System seçicisi (?msys=) — şerit tabloyla aynı kümeyi anlatır. */
  system: string;
  /** Tablodaki arama kutusu (?q=). */
  search: string;
}) {
  const q = useQuery({
    queryKey: ['messaging-series', fromNs, toNs, system, search],
    queryFn: () => api.messagingSeries(fromNs, toNs, system || undefined, search || undefined),
    // Sunucu TTL'i 30 sn — altına inmek sıcak slotu ıskalayıp CH'ye
    // gereksiz tur attırırdı (ES-maliyet disiplininin CH ikizi).
    staleTime: 30_000,
  });

  const data = q.data as MessagingSeries | undefined;
  const points = useMemo(() => data?.points ?? [], [data]);
  const xRange = useMemo(() => ({ from: fromNs / 1e9, to: toNs / 1e9 }), [fromNs, toNs]);

  // Kapsam notu — grafikler neyi çiziyor? Filtre varsa SÖYLENMELİ,
  // yoksa daraltılmış bir grafik filo geneliymiş gibi okunur.
  //
  // DÜRÜSTLÜK ŞERHİ: tablodaki arama ÇAĞIRAN ADLARINDA da eşleşiyor;
  // sunucu tarafı arama ise destination / system / cluster kimliğinde.
  // Yalnızca bir çağıran adına yazılan arama tabloyu daraltır ama
  // grafikleri daraltmaz — not bunu açıkça yazıyor.
  const scope = useMemo(() => {
    const bits: string[] = [];
    if (system) bits.push(system);
    if (search) bits.push(`"${search}" araması`);
    return bits.length ? bits.join(' · ') : 'tüm messaging trafiği';
  }, [system, search]);
  const scopeNote = search
    ? `Kapsam: ${scope}. Arama destination / system / cluster adında eşleşir — tablodaki arama ayrıca ÇAĞIRAN adlarında da eşleştiği için satır sayısı grafiklerden dar olabilir.`
    : `Kapsam: ${scope}.`;

  const loading = q.isPending;
  const error = q.isError ? 'Messaging seri sorgusu başarısız' : undefined;
  const empty = !loading && !error && points.length === 0;
  const emptyReason = empty ? 'Bu pencerede messaging trafiği yok' : undefined;

  // Üç panel de AYNI sync grubunda: imleç birlikte gezer.
  // '-ms' soneki MOTOR AD ALANI (v0.9.789): CorePanel x'i milisaniye
  // tutar, v1 uPlot gövdesi saniye — karışık grup crosshair'i 1000×
  // yanlış yere koyar. Bugün /messaging'de v1 grafik yok ama sonek
  // ileride birinin yanlış gruba düşmesini imkânsız kılıyor.
  const syncKey = 'messaging-ms';

  const panelCommon = {
    height: 190,
    xRange,
    loading,
    error,
    emptyReason,
    emptyHint: empty ? scopeNote : undefined,
    syncKey,
  };

  return (
    <>
      <div className="ov-grid ov-kpis ov-mb">
        <MsgKpi lab="Üretim" accent="var(--accent)"
          val={fmtRate(data?.producePerMin ?? 0)} unit=" /dk"
          note={`producer kind'lı span'ler, pencere boyunca dakikaya normalize. ${scopeNote}`} />
        <MsgKpi lab="Tüketim" accent="var(--ok)"
          val={fmtRate(data?.consumePerMin ?? 0)} unit=" /dk"
          note={`consumer kind'lı span'ler, pencere boyunca dakikaya normalize. ${scopeNote}`} />
        <MsgKpi lab="Hata oranı" accent="var(--err)"
          val={`${(data?.errorRate ?? 0).toFixed(2)}%`}
          note={`Pencere TOPLAMLARINDAN: ${(data?.errorCount ?? 0).toLocaleString()} hatalı / ${(data?.spanCount ?? 0).toLocaleString()} messaging span. Kova oranlarının ortalaması DEĞİL. ${scopeNote}`} />
        {/* Consumer lag karosu BİLEREK YOK. Lag broker tarafı bir
            ölçüdür (kafka consumer group lag) ve bu kurulumda hiçbir
            broker metriği ingest EDİLMİYOR — copilot bağlamı da bunu
            böyle söylüyor (copilot_deps.go). Span'lerden lag TÜRETİLEMEZ:
            elimizdeki en yakın şey tüketici işleme süresi, ki o farklı
            bir büyüklük. Uydurulmuş bir "lag" karosu bu sayfanın
            v0.9.813'te temizlediği yalan sınıfının aynısı olurdu.
            Broker metrikleri bir gün ingest edilirse karo katalog
            kapısıyla (messaging.*consumer.lag) eklenir. */}
      </div>

      <div className="ov-grid ov-charts-3 ov-mb">
        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              title="Üretim vs Tüketim"
              storageKey="msg-produce-consume"
              unit="cpm"
              note={scopeNote}
              items={[
                { name: 'Üretim', role: 'data' as const, series: toSeries(points, p => p.produceCount / 5, 'Üretim') },
                { name: 'Tüketim', role: 'success' as const, series: toSeries(points, p => p.consumeCount / 5, 'Tüketim') },
              ]} />
          </Suspense>
        </LazyMount>

        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              // BAŞLIK NE ÇİZDİĞİNİ SÖYLER (v0.9.774 dersi). Bu, messaging
              // SPAN süresidir — üretici publish + tüketici process — ve
              // tablodaki P50/P95 kolonlarıyla AYNI TDigest state'inden
              // gelir, yani grafik ile tablo birbirini doğrular.
              // Uçtan uca (produce→consume) gecikme AYRI bir büyüklük ve
              // destination drawer'ında yaşıyor: o zincir span_links
              // üzerinden yalnız destination başına sınırlandığında
              // dürüst kalıyor (gerekçe: messaging_series.go başlığı).
              title="Mesaj span gecikmesi · p50 / p95"
              storageKey="msg-latency"
              unit="ms"
              note={`${scopeNote} Uçtan uca produce→consume gecikmesi için bir destination satırını aç.`}
              items={[
                { name: 'p50', role: 'data' as const, series: toSeries(points, p => p.p50Ms, 'p50') },
                { name: 'p95', role: 'data' as const, series: toSeries(points, p => p.p95Ms, 'p95') },
              ]} />
          </Suspense>
        </LazyMount>

        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              title="Hata oranı"
              storageKey="msg-error-rate"
              unit="percent"
              note={scopeNote}
              items={[
                { name: 'Hata oranı', role: 'error' as const, series: toSeries(points, p => p.errorRate, 'Hata oranı') },
              ]} />
          </Suspense>
        </LazyMount>
      </div>
    </>
  );
}
