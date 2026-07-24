import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import { isXZoomed, yRefitScale } from './zoomState';

// engine.ts (v0.9.97, chart-consolidation Adım 1) — dört uPlot bileşeninin
// (OverviewChart / TimeChart / MultiLineChart / TimeSeriesPanel) BYTE-BENZER
// yaşam-döngü iskeletini tek yere çıkarır. Bileşenler "preset" olur: renk
// çözümü + uPlot.Options + AlignedData + hook'ları verir; new uPlot /
// destroy / ResizeObserver / setData fast-path / drag-zoom-koruma İSKELETİ
// buraya iner.
//
// SIFIR davranış değişikliği hedefi — her preset kendi eski opts/data/
// hook'larını birebir üretir. İskelet, dört bileşende zaten AYNI olan
// (v0.8.531 rebuild-vs-setData split + v0.9.78/79 isXZoomed zoom-koruma)
// koddur. Kanıt: chartBuildSig.test.ts imza kontratı (rebuild-vs-setData
// seam'i) + zoomState.test.ts (fast-path zoom kararı) + Adım 1 için
// 3-skeptik adversarial review (yaşam-döngü diff / fast-path-ref / audit
// 12-risk → hepsi EQUIVALENT). Hook wiring React effect'lerinde olduğundan
// node-only test ortamında renderHook edilemez; saf çekirdekler tam kaplı.

export interface ChartEngineSpec {
  // Rebuild tetikleyicisi: yalnız YAPI değişince (seri şekli/mod/eksen/
  // overlay/callback-PRESENCE) değişen saf imza. Nokta DEĞERLERİ buraya
  // girmez — onlar setData fast-path'ine biner.
  signature: string;
  // uPlot yükseklik (ResizeObserver setSize'da kullanılır).
  height: number;
  // ≥2 x-noktası + seri var mı — yoksa build atlanır (boş grafik çizmez).
  renderable: boolean;
  // Preset'in memoize ettiği hizalı verisi; setData fast-path'ini sürer.
  // Referans değişimi = yeni poll (fast-path); kolon sayısı değişimi =
  // rebuild'in verisidir (fast-path atlar).
  data: uPlot.AlignedData;
  // Rebuild'de çağrılır: renkleri o an çöz (tema-canlı), tam Options üret.
  buildOptions(width: number): uPlot.Options;
  // new uPlot SONRASI (rebuild) — ör. DOM ▼ deploy bayrağını kur+konumla.
  afterBuild?(u: uPlot): void;
  // setData fast-path SONRASI — ör. bayrağı kaydırılan pencereye taşı.
  afterData?(u: uPlot): void;
  // ResizeObserver setSize SONRASI — ör. bayrağı yeni genişliğe taşı.
  onResize?(u: uPlot): void;
  // Zoomlu fast-path'te y'yi elle refit (setData(false) y'yi resetlemez).
  // Verilmezse VARSAYILAN: tüm seriler (1..n) tek y eksenine yRefitScale.
  // TimeChart dual-eksende (y/y2) bunu override eder.
  refitScales?(u: uPlot, data: uPlot.AlignedData): void;
  // Çift-tık (Grafana-parite M1) — "zoom'dan bir adım geri" niyeti. Motor
  // URL BİLMEZ: host'a mount'ta bağlanan tek dblclick listener'ı yalnız bu
  // callback'i çağırır (specRef'ten CANLI okunur); sayfa katmanı geri-yığını
  // pop edip range'i geri yazar (TSP verilmediğinde eski yerel tam-aralık
  // reset'ine düşer). Verilmezse no-op — uPlot'un yerleşik dblclick
  // autoscale davranışı zaten aynen sürer (mevcut default bozulmaz).
  onZoomReset?(): void;
}

// useChartEngine — iskeleti sahiplenir; preset'in uPlot örneğine ref döner.
// Hook'lar/buildOptions specRef'ten CANLI okunur (fresh closure), tetikleyici
// yalnız signature/themeTick (rebuild) ve data (fast-path) — mevcut
// onZoomRef/zoomRef ref-disiplininin aynısı.
export function useChartEngine(
  hostRef: React.RefObject<HTMLDivElement | null>,
  spec: ChartEngineSpec,
  themeTick: number,
): React.MutableRefObject<uPlot | null> {
  const plotRef = useRef<uPlot | null>(null);
  const specRef = useRef(spec);
  specRef.current = spec;

  // Build / re-create effect — yalnız YAPI imzası ya da tema flip'inde.
  useEffect(() => {
    const el = hostRef.current;
    const s = specRef.current;
    if (!el || !s.renderable) return;

    plotRef.current?.destroy();
    const opts = s.buildOptions(el.clientWidth || 320);
    // Risk #8 (audit §5) MERKEZİ SAVUNMA: uPlot fire() `if (evName in hooks)`
    // yapar — değeri undefined olan bir hook key'i `in` kontrolünden geçer
    // ve hooks[evName].forEach'te patlar. Preset'ler koşullu hook'u
    // `key: cond ? [fn] : undefined` diye yazabilir (OVC setSelect no-onZoom);
    // undefined-değerli key'leri new uPlot ÖNCESİ ele, hiçbir preset
    // tökezlemesin (Incident impact grafiğinde drag-zoom latent çökmesini de
    // kapatır — pre-existing, v0.8.534'ten beri).
    if (opts.hooks) {
      const h = opts.hooks as Record<string, unknown>;
      for (const k of Object.keys(h)) if (h[k] === undefined) delete h[k];
    }
    const u = new uPlot(opts, s.data, el);
    plotRef.current = u;
    s.afterBuild?.(u);

    const ro = new ResizeObserver(() => {
      // StrictMode çift-mount / 0-genişlik unmount race: yıkılmış uPlot'a
      // setSize "forEach undefined" atardı — instance null'sa çık.
      const live = plotRef.current;
      if (!live || !el.clientWidth) return;
      live.setSize({ width: el.clientWidth, height: specRef.current.height });
      specRef.current.onResize?.(live);
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [spec.signature, themeTick]);

  // Çift-tık listener'ı — MOUNT'ta bir kez host div'e (host rebuild'ler
  // boyunca sabit React elemanı; uPlot over'ı ise her rebuild'de yenilenir,
  // o yüzden host'a bağlamak re-register derdini sıfırlar). Canlı spec okunur;
  // onZoomReset vermeyen preset'te no-op.
  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const onDbl = (e: MouseEvent) => {
      // v0.9.199 review-fix: yalnız ÇİZİM ALANI (.u-over) çift-tıkı zoom
      // geri-alır — MLC'nin host-İÇİ legend'i (click-to-isolate bölgesi)
      // ve eksen olukları eskisi gibi inert kalır; legend'e çift-tık
      // sayfa range'ine asla dokunmaz. .u-over kontrolü canlı uPlot
      // DOM'una karşı, bayat instance derdi yok.
      if (!(e.target instanceof Element) || !e.target.closest('.u-over')) return;
      specRef.current.onZoomReset?.();
    };
    el.addEventListener('dblclick', onDbl);
    return () => el.removeEventListener('dblclick', onDbl);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Data fast-path (v0.8.531 + v0.9.78/79) — poll yalnız nokta DEĞERLERİNİ
  // değiştirir; buildSig aynı, build effect çalışmaz. Zoomluysa (x
  // daralmış) setData(false) ile x/zoom KORUNUR + y elle refit; değilse
  // eski davranış (setData reset). Kolon sayısı değişimi = rebuild'in işi.
  useEffect(() => {
    const u = plotRef.current;
    const s = specRef.current;
    if (!u) return;
    if (u.data.length !== s.data.length) return;
    const xs = u.data[0] as number[];
    if (isXZoomed(xs, u.scales.x.min, u.scales.x.max)) {
      u.batch(() => {
        u.setData(s.data, false);
        if (s.refitScales) {
          s.refitScales(u, s.data);
        } else {
          const idxs = s.data.map((_, i) => i).slice(1);
          u.setScale('y', yRefitScale(s.data as (number | null)[][], idxs));
        }
      });
    } else {
      u.setData(s.data);
    }
    s.afterData?.(u);
  }, [spec.data]);

  return plotRef;
}
