// CorePanel — @grafana/ui üzerine kurulan TEK panel sarmalayıcısı (FAZ 2).
//
// Spec: "TEK sarmalayıcı; sayfa başına kopya yok." Kopya sınıfının bedeli
// bu depoda ölçülü: dört uPlot bileşeni byte-benzer yaşam döngüsünü ayrı
// ayrı taşıdı ve v0.9.97 engine.ts o borcu tek yerde kapattı. CorePanel
// aynı hatayı @grafana/ui katmanında BAŞTAN engelliyor — UPlotChart
// importu yalnız burada yasal (corePanelMonopoly testi tarar).
//
// KATMANLAR (tek yönlü akış, FAZ 1 sözleşmesinin devamı):
//   API → DataFrame (dataFrame.ts köprüsü: birim/eşik/null)
//       → AlignedData (framesToAligned)
//       → UPlotChart (@grafana/ui, @internal işaretli — bkz. RİSK)
//   Legend/istatistik/görünürlük bizim saf çekirdeklerden:
//   legendStats + visibleStats + legendVisibility (v0.9.103/483 hattı).
//
// RİSK, AÇIKÇA: UPlotChart ve UPlotConfigBuilder dışa aktarılıyor ama
// docblock'ları "@internal -- not a public API". Majör sürüm göçünde
// kırılabilirler. Bilinçli kabul (operatör kararı, Grafana lisansı
// alınacak; project-grafana-license-decision) — tazmini bu dosyanın TEK
// tüketim noktası olması: kırılırsa değişecek dosya sayısı BİR.
//
// ZAMAN BAĞLAMI BURADA DEĞİL: drag-zoom'un URL-state sahibi
// usePageZoomRange (v0.9.429, "tek sahip"). CorePanel yalnız onZoom/
// onZoomReset callback'i alır ve saniye cinsinden iletir — kendi zoom
// yığını YOK, ikinci bir sahip doğurmayız.
//
// DÖRT DURUM discriminated union: yükleniyor/boş/hata/kısmi. Spec: boş
// durum NEDENİNİ ve sonraki adımı söyler; kısmi veri görsel işaretlenir.

import { useEffect, useMemo, useRef, useState } from 'react';
import type uPlot from 'uplot';
import {
  UPlotChart, UPlotConfigBuilder,
  AxisPlacement, ScaleOrientation, ScaleDirection, ScaleDistribution,
} from '@grafana/ui';
import type { DataFrame } from '@grafana/data';
import { framesToAligned, chartTheme } from '@/lib/chart/dataFrame';
import { seriesRoleColor, type SeriesRole } from '@/lib/chart/seriesRole';
import { visibleRangeStats } from '@/lib/chart/visibleStats';
import { resolveLegendCollapsed } from '@/lib/chart/legendStats';
import {
  toggleSeriesVisibility, isolateSeriesVisibility, resetSeriesVisibility,
} from '@/lib/chart/legendVisibility';
import { resolveVar } from '@/lib/chart/resolveVar';
import {
  drawThresholds, drawTimeRegions,
  type ChartThreshold, type ChartTimeRegion,
} from '@/lib/chart/overlays';
import { alignedToCsv } from '@/lib/chart/exportCsv';
import { sortedTooltipRows } from '@/lib/chart/tooltipModel';
import { placeTooltip } from '@/lib/chartTooltip';
import { fmtTooltipTime } from '@/lib/chartFmt';
import { getItem, setItem, legendCollapseKey } from '@/lib/storage';
import { useThemeTick } from '@/lib/useThemeTick';
import { fmtSmart } from '@/lib/chartFmt';
import { Spinner, Empty } from '@/components/Spinner';

export type PanelData =
  | { state: 'loading' }
  | { state: 'error'; message: string }
  // Boş durum SEBEP taşır: "veri yok" bir hüküm değil, bir açıklamadır.
  | { state: 'empty'; reason: string; hint?: string }
  // partial: kısmi veri açıklaması (ör. "son 2 dk henüz eksik") — spec
  // "kısmi veri görsel işaretlenir".
  | { state: 'ready'; frames: DataFrame[]; partial?: string };

export interface CorePanelProps {
  title: string;
  data: PanelData;
  height?: number;
  // Seri rolleri (indeksle hizalı, eksikse 'data'): hata serisi kırmızı,
  // başarı yeşil — ROL ÇAĞIRANDAN gelir, etiketten tahmin edilmez
  // (seriesRole.ts gerekçesi).
  roles?: SeriesRole[];
  // uPlot saniye cinsinden brush aralığı — usePageZoomRange.handleZoom'a.
  onZoom?: (fromSec: number, toSec: number) => void;
  // Çift tık: bir adım geri (usePageZoomRange.handleZoomReset).
  onZoomReset?: () => void;
  // Aynı sayfadaki paneller aynı anahtarı verir → uPlot native cursor
  // senkronu (spec: senkron crosshair).
  syncKey?: string;
  logScale?: boolean;
  // Legend tablosunun localStorage kimliği (resolveLegendCollapsed).
  storageKey: string;
  // Eşikler KONFİGÜRASYONDAN gelir (spec: hard-coded değil). Çizgi +
  // ihlal bandı + sağ-kenar etiket — overlays.drawThresholds (M3
  // çekirdeği, dört preset'le birebir aynı görsel).
  thresholds?: ChartThreshold[];
  // Annotation'lar (deploy/incident pencereleri) AYRI VERİ YOLU: chart
  // sorgusuna karışmaz, çağıran kendi fetch'inden verir (spec şartı).
  // {fromSec,toSec} — fromSec===toSec dikey çizgi gibi ince bant çizer.
  regions?: ChartTimeRegion[];
  // p50-p99 gölgeli bant: [üst seri indeksi, alt seri indeksi] (1-tabanlı
  // uPlot serisi; 0 = x). Çizgi olarak zaten var olan iki serinin ARASI
  // doldurulur — p95 çizgisi ayrı bir seri olarak gelir.
  bands?: { above: number; below: number; fill?: string }[];
  // Başlangıçta GİZLİ seriler (ada göre) — ChartCard defaultHidden
  // paritesi (v0.9.720): Response time P99'u kapalı açar, lejant açar.
  // Kullanıcı dokunuşu seri kümesi değişene dek kalıcıdır.
  defaultHidden?: string[];
  // Kısa boşlukları köprüle (ms eşiği) — Grafana "Connect null values:
  // Threshold" birebiri. VARSAYILAN YOK = sıkı doktrin (null → boşluk).
  // Operatör kararı 2026-08-06: köprüleme panel-başına açık tercih.
  connectNulls?: number;
  // v0.9.725 — x ekseni SORGU aralığına mıhlanır (saniye, epoch).
  // Grafana her zaman seçili [from,to]'yu çizer: kenarlar = aralık
  // kenarı; verilmezse uPlot veriden türetir (eski davranış) ve
  // eksende sağlı-sollu ölü boşluk kalır (operatör bulgusu).
  xRange?: { from: number; to: number } | null;
  // v0.9.728 (rt-ops v2) — başlık satırına ek kontrol yuvası (segment
  // anahtarı gibi); menünün SOLUNA girer. ChartCard.headerAside paritesi.
  headerExtra?: import('react').ReactNode;
  // Dürüstlük notu ("+N operasyon daha…", satır tavanı) — grafiğin
  // altında soluk tek satır. ChartCard.note paritesi.
  note?: string | null;
  // "Sorguyu göster" menü kalemi için: paneli besleyen sorgunun/isteğin
  // insan-okur özeti. Verilmezse kalem çizilmez.
  queryText?: string;
  // Log ölçek kullanıcıya AÇILSIN mı (menüde toggle). logScale prop'u
  // başlangıç değeri; toggle panel-yerel state'e biner.
  logScaleToggle?: boolean;
}

export function CorePanel({
  title, data, height = 200, roles, onZoom, onZoomReset, syncKey, logScale, storageKey,
  thresholds, regions, bands, queryText, logScaleToggle, connectNulls,
  defaultHidden, xRange, headerExtra, note,
}: CorePanelProps) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  // Tema değişince config yeniden kurulmalı (renkler draw anında çözülür;
  // useThemeTick data-theme mutasyonunda sayaç artırır — mevcut desen).
  const themeTick = useThemeTick();
  // v0.9.704 (self-review 🟠) — config'i KİMLİK değişimi yıkmasın.
  // Grafana UPlotChart config'i === ile karşılaştırır; farklıysa uPlot
  // DESTROY+RECREATE eder. Inline thresholds={[...]} her parent render'da
  // yeni kimlik üretir → her poll tick'inde canvas yıkılırdı (flicker,
  // sync kaybı, 16ms ihlali). Callback ref'e, overlay props içerik
  // imzasına biner; yalnız İÇERİK değişince rebuild.
  const onZoomRef = useRef(onZoom);
  onZoomRef.current = onZoom;
  const overlaySig = JSON.stringify([thresholds ?? null, regions ?? null, bands ?? null]);
  // FAZ 2D — panel menüsü durumları. Tam ekran CSS overlay: route/DOM
  // taşıma yok, ESC ile çıkılır. Log toggle logScale prop'unu TOHUM alır.
  const [menuOpen, setMenuOpen] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [showQuery, setShowQuery] = useState(false);
  const [logLocal, setLogLocal] = useState(!!logScale);
  const effLog = logScaleToggle ? logLocal : !!logScale;

  useEffect(() => {
    if (!fullscreen) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setFullscreen(false); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [fullscreen]);

  // v0.9.711 (self-review WCAG bulgusu, kendim doğruladım) — menü
  // klavye sözleşmesi: role=menu vaat edip ESC/dış-tık vermemek ekran
  // okuyucuya yalan söylemek. ESC kapatır, dışarı tık kapatır.
  const menuRef = useRef<HTMLSpanElement | null>(null);
  useEffect(() => {
    if (!menuOpen) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setMenuOpen(false); };
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setMenuOpen(false);
    };
    window.addEventListener('keydown', onKey);
    window.addEventListener('mousedown', onDown);
    return () => {
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('mousedown', onDown);
    };
  }, [menuOpen]);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const frames = data.state === 'ready' ? data.frames : [];
  const aligned = useMemo(() => framesToAligned(frames), [frames]);

  // Görünürlük: tıkla = izole, Ctrl/Cmd = çoklu seçim (spec). Sorgu
  // TETİKLEMEZ — yalnız çizim gizlenir.
  const [vis, setVis] = useState<boolean[]>([]);
  useEffect(() => {
    const v = resetSeriesVisibility(aligned.names.length);
    // v0.9.720 — defaultHidden tohumu (yalnız seri kümesi değişince).
    if (defaultHidden?.length) {
      aligned.names.forEach((n, i) => { if (defaultHidden.includes(n)) v[i] = false; });
    }
    setVis(v);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [aligned.names.join('|')]);

  // Görünen aralık (uPlot x scale) — legend istatistikleri bundan.
  const [xWin, setXWin] = useState<[number, number] | null>(null);

  // Görünürlük uPlot'a setSeries ile uygulanır — config rebuild DEĞİL.
  const plotRef = useRef<uPlot | null>(null);
  // v0.9.710 — tooltip. Kapanışlar config rebuild'ine bağlı kalmasın
  // diye canlı durum ref'lerde (TimeChart deseni: veri u.data'dan LIVE
  // okunur, yapısal olanlar rebuild'de tazelenir).
  const ttRef = useRef<HTMLDivElement | null>(null);
  const visRef = useRef<boolean[]>([]);
  visRef.current = vis;
  const framesRef = useRef(frames);
  framesRef.current = frames;
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    vis.forEach((show, i) => {
      if (u.series[i + 1] && u.series[i + 1].show !== show) {
        u.setSeries(i + 1, { show }, false);
      }
    });
  }, [vis]);

  const config = useMemo(() => {
    const theme = chartTheme();
    const b = new UPlotConfigBuilder();
    b.addScale({
      scaleKey: 'x', isTime: true,
      orientation: ScaleOrientation.Horizontal, direction: ScaleDirection.Right,
      // v0.9.725 — Grafana paritesi: x SORGU aralığına mıhlı (kenar =
      // aralık kenarı). uPlot x ms cinsinden (onZoom /1000 gerekçesi),
      // xRange sn gelir. Verilmezse eski davranış (veriden türet).
      range: xRange ? () => [xRange.from * 1000, xRange.to * 1000] as uPlot.Range.MinMax : undefined,
    });
    b.addScale({
      scaleKey: 'y',
      orientation: ScaleOrientation.Vertical, direction: ScaleDirection.Up,
      distribution: effLog ? ScaleDistribution.Log : ScaleDistribution.Linear,
      log: effLog ? 10 : undefined,
    });
    b.addAxis({ scaleKey: 'x', isTime: true, placement: AxisPlacement.Bottom, theme });
    b.addAxis({
      scaleKey: 'y', placement: AxisPlacement.Left, theme,
      formatValue: (v: unknown) => fmtSmart(typeof v === 'number' ? v : Number(v)),
    });
    aligned.names.forEach((name, i) => {
      b.addSeries({
        scaleKey: 'y', theme,
        lineColor: resolveVar(seriesRoleColor(name, roles?.[i] ?? 'data')),
        lineWidth: 1.5,
        // show BURADA SABİT true: görünürlük setSeries ile uygulanıyor
        // (aşağıdaki effect). Config'e gömmek her legend tıkını full
        // rebuild yapardı — uPlot'un ucuz toggle'ı varken.
        show: true,
        // Doktrin (operatör onayı 2026-08-06): varsayılan SIKI — null
        // boşluk çizilir, Grafana'nın "Connect null values: Never"
        // karşılığı. connectNulls (ms eşiği) panel-başına AÇIK tercihtir:
        // Grafana'daki "Threshold" modunun birebiri (spanNulls: number).
        spanNulls: connectNulls ?? false,
      });
    });
    if (syncKey) b.setCursor({ sync: { key: syncKey } });
    // p50-p99 bandı: uPlot native band — iki mevcut serinin arası dolar.
    for (const band of bands ?? []) {
      b.addBand({
        series: [band.above, band.below],
        fill: resolveVar(band.fill ?? 'var(--accent-soft)'),
      });
    }
    // Eşik çizgileri + annotation bölgeleri: M3 çizim çekirdeği. Renk
    // token'ları DRAW anında çözülür (tema-canlı) — build anında değil;
    // themeTick yalnız seri renklerini tazeler.
    if (thresholds?.length || regions?.length) {
      b.addHook('draw', (u) => {
        if (regions?.length) drawTimeRegions(u, regions);
        if (thresholds?.length) {
          drawThresholds(u, thresholds.map(th => ({
            value: th.value, label: th.label,
            color: resolveVar(th.color ?? 'var(--warn)'),
          })));
        }
      });
    }
    // Brush → zoom. uPlot select değeri px; setSelect hook'unda scale'e
    // çevrilir (posToVal) — cursorOpts.selectRangeSec ile aynı yaklaşım.
    b.addHook('setSelect', (u) => {
      if (!onZoomRef.current || u.select.width < 5) return;
      // v0.9.704 (self-review 🔴) — posToVal MİLİSANİYE döndürür: köprü
      // x'i ms üretir ve @grafana/ui ms:1 kurar. Sözleşme SANİYE
      // (usePageZoomRange.handleZoom ×1000 yapar); bölmeden geçirmek
      // URL'i yıl ~58.000'e götürüyordu. /1000 BURADA — çağıranda değil,
      // yoksa her çağıran ayrı hatırlamak zorunda kalır.
      const fromSec = u.posToVal(u.select.left, 'x') / 1000;
      const toSec = u.posToVal(u.select.left + u.select.width, 'x') / 1000;
      u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false);
      onZoomRef.current(fromSec, toSec);
    });
    // v0.9.710 — tooltip: "tüm seriler, değere göre sıralı" (spec
    // varsayılanı). Saf çekirdekler TimeChart'la ORTAK: sortedTooltipRows
    // (gizli seri düşer, gap 0 okumaz) + placeTooltip (flip/clamp) +
    // fmtTooltipTime (tarih koşulsuz, çözünürlük adımdan). Değer biçimi
    // DataFrame display processor'dan — birim çevirisi köprü sözleşmesi
    // gereği elle yazılmaz.
    b.addHook('setCursor', (u) => {
      const tt = ttRef.current;
      if (!tt) return;
      const idx = u.cursor.idx;
      if (idx == null || u.cursor.left == null || u.cursor.left < 0) {
        tt.style.display = 'none';
        return;
      }
      const xs = u.data[0] as number[];
      const tMs = xs[idx];
      if (tMs == null) { tt.style.display = 'none'; return; }
      const stepSec = xs.length > 1 ? Math.abs(xs[1] - xs[0]) / 1000 : null;
      const rows = sortedTooltipRows(aligned.names.map((label, i) => {
        const si = u.cursor.idxs?.[i + 1] ?? idx;
        const v = visRef.current[i] === false ? null
          : ((u.data[i + 1] as (number | null)[])?.[si] ?? null);
        const disp = framesRef.current[i]?.fields[1].display;
        return {
          label,
          color: resolveVar(seriesRoleColor(label, roles?.[i] ?? 'data')),
          value: v,
          // display processor varsa text'i o üretir; TooltipRow.text'i
          // model kurar ama biz biçimli metni unit alanına gömmüyoruz —
          // fmt override'ı aşağıda satır basarken uygulanıyor.
          unit: undefined,
          fmt: v != null && disp ? (() => { const d = disp(v); return `${d.text}${d.suffix ?? ''}`; })() : undefined,
        };
      }));
      if (rows.length === 0) { tt.style.display = 'none'; return; }
      tt.innerHTML = `<div class="ov-tt-t">${fmtTooltipTime(tMs / 1000, stepSec)}</div>` + rows.map(r =>
        `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${r.color}"></i>${r.label}</span><b>${r.text}</b></div>`,
      ).join('');
      tt.style.display = 'block';
      const host = wrapRef.current;
      if (host) {
        const pl = placeTooltip(
          u.cursor.left ?? 0, u.cursor.top ?? 0,
          tt.offsetWidth, tt.offsetHeight,
          u.over.clientWidth, u.over.clientHeight,
          u.over.offsetLeft, u.over.offsetTop,
          host.clientWidth, host.clientHeight,
        );
        tt.style.left = `${pl.x}px`;
        tt.style.top = `${pl.y}px`;
      }
    });
    // Görünen pencereyi legend istatistiklerine bildir.
    b.addHook('setScale', (u, key) => {
      if (key !== 'x') return;
      const s = u.scales.x;
      if (s.min != null && s.max != null) setXWin([s.min, s.max]);
    });
    return b;
    // themeTick: tema değişince renkler yeniden çözülsün diye bağımlılıkta.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [aligned.names.join(' '), roles?.join(), syncKey, effLog, themeTick, overlaySig, xRange?.from, xRange?.to]);

  // CSV: ekranda ne varsa o iner (görünen veri, ayrı export sorgusu yok).
  const downloadCsv = () => {
    if (data.state !== 'ready') return;
    const csv = alignedToCsv(aligned.names, aligned.data as (number | null)[][]);
    const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = `${title.replace(/[^\w-]+/g, '_')}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  };

  // Legend istatistikleri — GÖRÜNEN aralıktan (spec). Zoom bir sorgu
  // değil pencerelemedir; visibleRangeStats saf.
  const stats = useMemo(() => {
    const t = aligned.data[0] as number[];
    const [mn, mx] = xWin ?? [-Infinity, Infinity];
    return aligned.names.map((name, i) => ({
      name,
      stat: visibleRangeStats(t, aligned.data[i + 1] as (number | null)[], mn, mx),
    }));
  }, [aligned, xWin]);

  // v0.9.704 (self-review 🟠) — İKİ kusur: (a) initializer yalnız
  // mount'ta koşar ve panel loading (0 seri) ile mount olur → ">6 seri
  // kapalı" kuralı HİÇ tetiklenmezdi; (b) storageKey belgeliydi ama
  // hiç KULLANILMIYORDU — tercih kalıcılaşmıyordu. Şimdi: kayıt
  // legendCollapseKey ailesinden okunur/yazılır (v0.9.483), seri sayısı
  // İLK KEZ öğrenildiğinde (0→n) kural yeniden değerlendirilir; kullanıcı
  // dokunduysa (touchedRef) otomatik karar bir daha ezmez.
  const [legendOpen, setLegendOpen] = useState(true);
  const legendTouchedRef = useRef(false);
  const legendCountRef = useRef(0);
  useEffect(() => {
    const n = aligned.names.length;
    if (n === 0 || legendCountRef.current === n) return;
    legendCountRef.current = n;
    if (legendTouchedRef.current) return;
    const stored = getItem<boolean | null>(legendCollapseKey(storageKey), null);
    setLegendOpen(!resolveLegendCollapsed(stored, undefined, n, 6));
  }, [aligned.names.length, storageKey]);
  const toggleLegend = () => {
    legendTouchedRef.current = true;
    setLegendOpen(o => {
      // Kayıt COLLAPSED anlamında (resolveLegendCollapsed sözleşmesi):
      // yeni durum açık(=!o true) ise collapsed=false yazılır.
      setItem(legendCollapseKey(storageKey), o);
      return !o;
    });
  };

  return (
    // v0.9.735 (operatör: "arka plan PatternFly'da gri kalıyor") — "panel"
    // sınıfı globals.css'te TANIMLI DEĞİLDİ; kök çıplak kalıp sayfa
    // arka planını gösteriyordu. ChartCard'ın çizdiği kart kabuğu (.card:
    // bg1 + border + radius + gölge) buraya taşındı — redhat/light'ta
    // beyaz, dark'ta koyu; tema token'ları karar verir.
    <div className="card" style={{
      display: 'flex', flexDirection: 'column', gap: 6,
      // Tam ekran: CSS overlay. Route/DOM taşınmaz — uPlot instance'ı
      // yaşamaya devam eder, ResizeObserver genişliği kendisi yakalar.
      ...(fullscreen ? {
        position: 'fixed', inset: 12, zIndex: 100,
        background: 'var(--bg0)', padding: 12, overflow: 'auto',
      } : {}),
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <h3 style={{ margin: 0, fontSize: 12 }}>{title}</h3>
        {data.state === 'ready' && data.partial && (
          <span className="badge b-warn" title={data.partial}>kısmi</span>
        )}
        {headerExtra && <span style={{ marginLeft: 'auto' }}>{headerExtra}</span>}
        {/* FAZ 2D — panel menüsü: tam ekran / CSV / sorguyu göster / log. */}
        <span ref={menuRef} style={{ marginLeft: headerExtra ? 0 : 'auto', position: 'relative' }}>
          <button className="sec" aria-label="Panel menüsü" aria-expanded={menuOpen}
            style={{ fontSize: 11, padding: '0 6px' }}
            onClick={() => setMenuOpen(o => !o)}>⋯</button>
          {menuOpen && (
            <div role="menu" style={{
              position: 'absolute', right: 0, top: '100%', zIndex: 20,
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 6, padding: 4, display: 'flex',
              flexDirection: 'column', gap: 2, minWidth: 150,
            }}>
              <button className="sec" onClick={() => { setFullscreen(f => !f); setMenuOpen(false); }}>
                {fullscreen ? 'Tam ekrandan çık' : 'Tam ekran'}
              </button>
              <button className="sec" disabled={data.state !== 'ready'}
                onClick={() => { downloadCsv(); setMenuOpen(false); }}>
                CSV indir
              </button>
              {queryText && (
                <button className="sec" onClick={() => { setShowQuery(q => !q); setMenuOpen(false); }}>
                  Sorguyu göster
                </button>
              )}
              {logScaleToggle && (
                <button className="sec" onClick={() => setLogLocal(l => !l)}>
                  {effLog ? '✓ ' : ''}Log ölçek
                </button>
              )}
            </div>
          )}
        </span>
      </div>
      {showQuery && queryText && (
        <pre style={{
          margin: 0, padding: 8, fontSize: 11, background: 'var(--bg2)',
          border: '1px solid var(--border)', borderRadius: 6,
          overflowX: 'auto', whiteSpace: 'pre-wrap',
        }}>{queryText}</pre>
      )}

      <div ref={wrapRef} style={{ minHeight: height, position: 'relative' }}
        onDoubleClick={onZoomReset}
        onMouseLeave={() => { if (ttRef.current) ttRef.current.style.display = 'none'; }}>
        {/* v0.9.710 — tooltip overlay; .ov-tt sınıfları evdeki tooltip
            görseliyle birebir (OVC/TC/MLC aynı CSS'i kullanıyor). */}
        <div ref={ttRef} className="ov-tt" style={{ display: 'none', position: 'absolute', zIndex: 10, pointerEvents: 'none' }} />
        {data.state === 'loading' && <Spinner />}
        {data.state === 'error' && (
          <Empty icon="⚠" title="Grafik yüklenemedi">{data.message}</Empty>
        )}
        {data.state === 'empty' && (
          <Empty icon="◫" title={data.reason}>{data.hint ?? ''}</Empty>
        )}
        {data.state === 'ready' && width > 0 && aligned.data[0].length >= 2 && (
          <UPlotChart data={aligned.data} width={width} height={height} config={config}
            plotRef={(u) => { plotRef.current = u; }} />
        )}
        {data.state === 'ready' && aligned.data[0].length < 2 && (
          <Empty icon="◫" title="Bu aralıkta çizilecek nokta yok">
            Aralığı genişletmeyi deneyin.
          </Empty>
        )}
      </div>

      {note && (
        <div style={{ fontSize: 10, color: 'var(--text3)' }}>{note}</div>
      )}

      {data.state === 'ready' && aligned.names.length > 0 && (
        <div style={{ fontSize: 11 }}>
          <button className="sec" style={{ fontSize: 10, padding: '1px 6px' }}
            onClick={toggleLegend}>
            {legendOpen ? '▼' : '▶'} Series ({aligned.names.length})
          </button>
          {legendOpen && (
            <table style={{ width: '100%', marginTop: 4 }}>
              <thead><tr>
                <th style={{ textAlign: 'left' }}>Seri</th>
                <th className="num">Son</th><th className="num">Min</th>
                <th className="num">Maks</th><th className="num">Ort</th>
                <th className="num">Toplam</th>
              </tr></thead>
              <tbody>
                {stats.map((s, i) => (
                  <tr key={`${i}:${s.name}`}
                    tabIndex={0}
                    role="button"
                    aria-pressed={vis[i] !== false}
                    aria-label={`${s.name} — Enter: izole et, Boşluk: gizle/göster`}
                    style={{ opacity: vis[i] === false ? 0.35 : 1, cursor: 'pointer' }}
                    onClick={e => setVis(v =>
                      e.ctrlKey || e.metaKey
                        ? toggleSeriesVisibility(v, i)
                        : isolateSeriesVisibility(v, i))}
                    onKeyDown={e => {
                      // v0.9.711 — Ctrl+tık klavyeden ERİŞİLEMEZ (review
                      // bulgusu): Enter=izole, Space=tekil gizle/göster.
                      if (e.key === 'Enter') { e.preventDefault(); setVis(v => isolateSeriesVisibility(v, i)); }
                      if (e.key === ' ') { e.preventDefault(); setVis(v => toggleSeriesVisibility(v, i)); }
                    }}>
                    <td>
                      <span style={{
                        display: 'inline-block', width: 8, height: 8, borderRadius: 2,
                        background: resolveVar(seriesRoleColor(s.name, roles?.[i] ?? 'data')),
                        marginRight: 6,
                      }} />
                      {s.name}
                    </td>
                    <td className="num">{fmtSmart(s.stat.last)}</td>
                    <td className="num">{fmtSmart(s.stat.min)}</td>
                    <td className="num">{fmtSmart(s.stat.max)}</td>
                    <td className="num">{fmtSmart(s.stat.mean)}</td>
                    <td className="num">{fmtSmart(s.stat.sum)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}
