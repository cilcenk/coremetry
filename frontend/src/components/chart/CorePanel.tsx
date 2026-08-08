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
  GraphGradientMode, DrawStyle, PointVisibility,
} from '@grafana/ui';
import type { DataFrame, DecimalCount } from '@grafana/data';
import { framesToAligned, chartTheme } from '@/lib/chart/dataFrame';
import {
  AXIS_FONT_SIZE, axisTickPlan, decimalsForIncr, widestLabelPx, axisGutterPx,
  seriesExtent, paddedExtent,
} from '@/lib/chart/axisSize';
import { seriesRoleColor, type SeriesRole } from '@/lib/chart/seriesRole';
import { visibleRangeStats } from '@/lib/chart/visibleStats';
import { resolveLegendCollapsed, isAdditiveUnit } from '@/lib/chart/legendStats';
import {
  toggleSeriesVisibility, isolateSeriesVisibility, resetSeriesVisibility,
} from '@/lib/chart/legendVisibility';
import { resolveVar } from '@/lib/chart/resolveVar';
import {
  drawThresholds, drawTimeRegions, drawExemplars, exemplarAt,
  type ChartThreshold, type ChartTimeRegion, type ChartExemplar,
} from '@/lib/chart/overlays';
import {
  stackData, stackBands, seriesDrawOrder, drawPosOf, reorderSeries,
} from '@/lib/chart/stacking';
import { bucketWindowNs } from '@/lib/chart/bucketWindow';
import { alignedToCsv } from '@/lib/chart/exportCsv';
import { sortedTooltipRows, capTooltipRows } from '@/lib/chart/tooltipModel';
import { decidePinGesture, applyPinStyle, clearPinStyle } from '@/lib/chart/tooltipPin';
import { resolveFocusIdx, focusSeriesStyle } from '@/lib/chart/seriesFocus';
import { placeTooltip } from '@/lib/chartTooltip';
import { fmtTooltipTime } from '@/lib/chartFmt';
import { getItem, setItem, legendCollapseKey } from '@/lib/storage';
import { useThemeTick } from '@/lib/useThemeTick';
import { fmtSmart } from '@/lib/chartFmt';
import { Spinner, Empty } from '@/components/Spinner';

// bucketWindowAt (v0.9.789) — sayfa koordinatındaki bir tıkı, tıklanan
// bucket'ın ns penceresine çevirir. Çizim alanı DIŞINDA (eksen şeridi,
// başlık boşluğu, lejant kenarı) kalan tık null döner: bucket-tık
// "grafiğe tıkladım" jestidir, karta değil.
//
// Modül kapsamında ve saf: tek iş, uPlot'un canlı ölçeğinden x değerini
// okuyup saf bucketWindow çekirdeğine devretmek. Eksen MİLİSANİYE
// (dataFrame.ts köprü sözleşmesi) → unitsPerSec = 1000.
function bucketWindowAt(u: uPlot, clientX: number, clientY: number) {
  const r = u.over.getBoundingClientRect();
  const left = clientX - r.left;
  const top = clientY - r.top;
  if (left < 0 || left > u.over.clientWidth) return null;
  if (top < 0 || top > u.over.clientHeight) return null;
  const xMs = u.posToVal(left, 'x');
  if (!isFinite(xMs)) return null;
  const xs = u.data[0] as number[] | undefined;
  if (!xs || xs.length === 0) return null;
  return bucketWindowNs(xs, xMs, 1000);
}

// v0.9.792 — pin AFORDANSI. Tooltip'in ALT satırına soluk tek satır; yalnız
// pin YOKKEN yazılır (pinliyken setCursor erken döner, yerine applyPinStyle'ın
// "📌 sabit — tık / Esc çözer" başlık satırı geçer). Token'lar note satırıyla
// aynı (10px / var(--text3)) — panelde üçüncü bir tipografi dili doğmaz.
// v0.9.799 — imleç noktası ölçüleri. Kutu 10px, kenarlık 2px → görünen
// dolu çekirdek 6px (uplot.css: box-sizing border-box + background-clip
// padding-box), kenarlık yarı saydam bir hale olur. Grafana TimeSeries'in
// imleç noktası ölçüsü de bu civarda (seri nokta çapı × 2).
const CURSOR_POINT_PX = 10;
const CURSOR_POINT_BORDER_PX = 2;

const PIN_TIP_HTML =
  '<div class="ov-tt-tip" style="margin-top:4px;padding-top:4px;' +
  'border-top:1px solid var(--border);color:var(--text3);font-size:10px">' +
  'Shift+tık: sabitle</div>';

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
  // v0.9.737 tek-tık büyütme → v0.9.742 (operatör tercihi): tık artık
  // ÇAĞIRANIN verdiği eyleme gider (ör. Metrics sayfasına navigasyon);
  // tam ekran menüde duruyor. Drag-zoom ve çift-tık (zoom reset) ile
  // çatışmaz: >5px sürüklenen basış tık sayılmaz, tek tık 250ms
  // çift-tık beklemesinden sonra işler.
  onExpandClick?: () => void;
  // v0.9.744 (Explore v2) — seri-hizalı exemplar ◆ listeleri (frames ile
  // aynı indeks). Tık önceliği: ◆ isabeti trace açar (onExemplarClick),
  // panel tık-eylemi (onExpandClick) ancak isabet yoksa çalışır.
  exemplars?: (ChartExemplar[] | undefined)[];
  onExemplarClick?: (traceId: string) => void;
  // v0.9.789 — "spike → exemplar": çizim alanına DÜZ TIK, tıklanan
  // bucket'ın zaman penceresini NANOSANİYE olarak çağırana verir
  // (çağıran tipik olarak o pencerenin temsilci trace'ini açar).
  // v0.7.22'den beri MultiLineChart'ın kancasıydı; v2 motoruna taşındı
  // ve Service sayfasının error-rate/latency panelleri buradan çiziliyor.
  //
  // Pencere hesabı SAF: lib/chart/bucketWindow — MLC'nin eski gövdesi de
  // AYNI fonksiyonu çağırır, yani ?chartsV2=0 kaçışı aynı tıkta aynı
  // pencereyi açar (iki kopya = iki farklı exemplar demekti).
  //
  // JEST ÖNCELİĞİ (yukarıdan aşağı): exemplar ◆ isabeti > bucket-tık >
  // onExpandClick. Sürükleme (>5px) ve çift-tık (zoom-geri) elenir —
  // onExpandClick'in 250ms bekleme koruması bucket-tık için de geçerli.
  onBucketClick?: (fromNs: number, toNs: number) => void;
  // v0.9.745 (Explore v2) — KONTROLLÜ görünürlük modu: Explore'da lejant
  // GroupTable'dadır; hiddenNames verildiğinde iç lejant durumu değil BU
  // küme görünürlüğün kaynağıdır (ada göre). hideLegend iç tabloyu
  // gizler. onCursorTime crosshair zamanını (unix sn; null=ayrıldı)
  // yayınlar — cursorBus buradan beslenir.
  hiddenNames?: Set<string>;
  hideLegend?: boolean;
  onCursorTime?: (timeSec: number | null) => void;
  // v0.9.793 — KONTROLLÜ odak (TimeSeriesPanel.focusedLabel sözleşmesinin
  // birebiri): verilen etiketin serisi tam opaklıkta + yarım piksel kalın
  // çizilir, ötekiler soluklaşır. Explore'da lejant GroupTable'dadır ve
  // satır hover'ı buradan iner (QueryPanel:27'de "v2'de henüz yok" diye
  // belgelenen boşluk). Panel KENDİ lejantını çiziyorsa satır hover'ı da
  // aynı kanaldan geçer — prop verilmediğinde iç hover kazanır.
  focusedLabel?: string | null;
  // v0.9.764 (mockup dilim 3) — frame-hizalı KESİKLİ çizim işareti:
  // önceki-dönem hayaleti (Grafana timeShift görünümü). true olan
  // frame 5-4 kesikli, dolgusuz çizilir; rol rengi aynen (muted
  // öneriliyor ama çağıranın kararı).
  dashed?: boolean[];
  // v0.9.799 — emphasis[] KANALI KALDIRILDI. v0.9.798'de tek tüketicisi
  // Overview'ın "Toplam" çizgisiydi; operatör o çizgiyi büyük
  // grafiklerden geri aldırınca kanal tüketicisiz kaldı. Ölü bir prop
  // bırakmak, bir sonraki okuyucuya "bu kablo çalışıyor" diye yalan
  // söylerdi (CLAUDE.md: geriye-uyum şimi YOK).
  // v0.9.785 — çizim markı. 'line' varsayılan (bugünkü davranış, bayt
  // bayt); 'bars' Grafana'nın Bars draw-style'ı. PRİMİTİF string olarak
  // taşınır — nesne/dizi bir prop config kimliğini her render'da yıkar
  // (v0.9.704 destroy/recreate dersi).
  //
  // v0.9.788 — union DÖRDE çıktı, Explore'un mark kümesiyle birebir:
  //   • 'area'    = line dalı + kalın dolgu (başka fark yok).
  //   • 'stacked' = yığılmış ALAN. Kümülatif matris yalnız ÇİZİME gider;
  //     tooltip/lejant/CSV ham değeri okur (aşağıdaki ham-veri kanalı).
  // Union genişledi ve QueryPanel kapısı da genişledi — ikisi birlikte,
  // "gelemeyecek mark'ı tipte vaat etme" kuralı gereği.
  //
  // v0.9.808 — union BEŞE çıktı: 'stacked-bars' = yığılmış ÇUBUK. Aynı
  // kümülatif matris, farklı mark (DrawStyle.Bars) ve TERS çizim sırası
  // (stacking.seriesDrawOrder — gerekçe orada). Dashboard'ın
  // 'stacked-bar' markı buraya düşer; bant YOKTUR, çubuklar opaktır ve
  // tooltip yine HAM katman değerini okur.
  viz?: 'line' | 'bars' | 'area' | 'stacked' | 'stacked-bars';
}

export function CorePanel({
  title, data, height = 200, roles, onZoom, onZoomReset, syncKey, logScale, storageKey,
  thresholds, regions, bands, queryText, logScaleToggle, connectNulls,
  defaultHidden, xRange, headerExtra, note, onExpandClick, exemplars, onExemplarClick,
  onBucketClick, hiddenNames, hideLegend, onCursorTime, dashed, viz = 'line',
  focusedLabel,
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
  // FAZ 2D — panel menüsü durumları. Tam ekran CSS overlay: route/DOM
  // taşıma yok, ESC ile çıkılır. Log toggle logScale prop'unu TOHUM alır.
  const [menuOpen, setMenuOpen] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  // v0.9.737 — tek-tık/tam-ekran ayrımı için basış konumu + bekleme.
  const clickRef = useRef<{ x: number; y: number } | null>(null);
  const clickTimerRef = useRef<number | null>(null);
  // v0.9.744 — exemplar'lar draw-hook'ta REF'ten okunur: poll'da yeni
  // dizi gelse de config yeniden kurulmaz (704 kimlik dersi).
  const exemplarsRef = useRef(exemplars);
  exemplarsRef.current = exemplars;
  const exemplarClickRef = useRef(onExemplarClick);
  exemplarClickRef.current = onExemplarClick;
  // v0.9.789 — bucket-tık callback'i de REF'te (onZoomRef deseni).
  // Çağıranlar her render'da taze bir ok fonksiyonu veriyor; kimliği bir
  // bağımlılık dizisine sokmak uPlot'u her poll tick'inde destroy/recreate
  // ederdi (v0.9.704 dersi). Bu yüzden hiçbir dep dizisinde YOK —
  // corePanelContracts 'onBucketClick,' pini bunu çiviler.
  const bucketClickRef = useRef(onBucketClick);
  bucketClickRef.current = onBucketClick;
  const onCursorTimeRef = useRef(onCursorTime);
  onCursorTimeRef.current = onCursorTime;
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
  const [visLocal, setVis] = useState<boolean[]>([]);
  useEffect(() => {
    const v = resetSeriesVisibility(aligned.names.length);
    // v0.9.720 — defaultHidden tohumu (yalnız seri kümesi değişince).
    if (defaultHidden?.length) {
      aligned.names.forEach((n, i) => { if (defaultHidden.includes(n)) v[i] = false; });
    }
    setVis(v);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [aligned.names.join('|')]);
  // v0.9.745 — kontrollü mod: hiddenNames verildiyse görünürlüğün kaynağı
  // ODUR (iç lejant durumu değil); ada göre türetilir.
  const vis = useMemo(
    () => hiddenNames
      ? aligned.names.map(n => !hiddenNames.has(n))
      : visLocal,
    [hiddenNames, visLocal, aligned.names.join('|')]);

  // ── v0.9.788 — HAM VERİ KANALI (yığılmış alan) ────────────────────────
  //
  // Kümülatif matris `aligned`'a YAZILMAZ. aligned ham kalır ve lejant
  // istatistikleri, CSV ve tooltip ONDAN okur; uPlot'a giden `drawData`
  // ayrı bir türetmedir. Tek matris tutsaydık stacked'te tooltip
  // KÜMÜLATİF değeri "bu serinin değeri" diye gösterirdi — grafiğin en
  // çok güvenilen parçası yalan söylerdi (TSP:609-625 aynı dersi
  // bundleRef ile öğrenmişti).
  // v0.9.808 — yığın AİLESİ iki marklı: alan ('stacked') ve çubuk
  // ('stacked-bars'). Kümülatif matris, gizleme yeniden hesabı, pxAlign,
  // düz dolgu ve exemplar bastırması İKİSİNDE de aynı — o yüzden `stacked`
  // aileyi temsil eder. Ayrışan iki şey var ve ikisi de aşağıda ADIYLA
  // ayrılmış: bantlar (çubukta YOK) ve çizim sırası (çubukta TERS).
  const stackedBars = viz === 'stacked-bars';
  const stacked = viz === 'stacked' || stackedBars;
  const hiddenIdx = useMemo(() => {
    const s = new Set<number>();
    vis.forEach((show, i) => { if (show === false) s.add(i); });
    return s;
  }, [vis]);
  // Gizleme = YENİDEN HESAP: bir katman kapanınca üsttekiler gerçekten
  // aşağı iner. Bu bir VERİ memo'su, config değil — ' vis,' config
  // bağımlılığına girmez (corePanelContracts 🟠 pini korunur).
  const stackedData = useMemo(
    () => (stacked ? stackData(aligned.data, hiddenIdx) : aligned.data),
    [stacked, aligned, hiddenIdx]);
  // ÇİZİM SIRASI (v0.9.808). uPlot sonraki seriyi ÜSTE boyar ve bars path
  // builder her çubuğu TABANDAN çizer: kümülatifte en üst katman en uzun
  // olduğundan mantıksal sırada çizmek alttakileri tamamen örter. Ters
  // sırada (en uzun önce) doğru yığın görünür. Kimlik sırası dışındaki her
  // markta bu tablo [0..n-1]'dir, yani ek bir kod yolu doğmaz.
  const drawOrder = useMemo(
    () => seriesDrawOrder(aligned.names.length, stackedBars),
    [aligned.names.length, stackedBars]);
  // Ters tablo: MANTIKSAL indeks → çizim pozisyonu. uPlot'un 1-tabanlı
  // series[] dizisine dokunan üç yol bundan geçer (görünürlük, tooltip'in
  // cursor.idxs okuması, odak stilleri); lejant/istatistik/CSV mantıksal
  // sırada kalır.
  const drawPos = useMemo(() => drawPosOf(drawOrder), [drawOrder]);
  const drawData = useMemo(
    () => reorderSeries(stackedData, drawOrder), [stackedData, drawOrder]);
  // Bantlar TÜRETİLMİŞ veridir: liste her render'da yeni kimlik alır, o
  // yüzden config imzasına İÇERİKÇE katılır (v0.9.704 — kimliğe bağlansa
  // her poll tick'i uPlot'u destroy/recreate ederdi).
  //
  // v0.9.808 — yığılmış ÇUBUKTA bant YOK: bant iki çizgi ARASINI doldurur,
  // çubuk zaten tabandan dolu çizilir. Bant eklemek üst üste binen iki
  // dolgu üretirdi (ve uPlot'un "bir seri en fazla bir bandın üst kenarı"
  // kuralına gereksizce yüklenirdi).
  const stackedBands = useMemo(
    () => (stacked && !stackedBars ? stackBands(aligned.names.length, hiddenIdx) : null),
    [stacked, stackedBars, aligned.names.length, hiddenIdx]);
  // v0.9.788 — stacked iken PROP bands (p50-p99) imzaya bile girmez:
  // aşağıda yok sayılıyor, imzada tutmak sahte rebuild üretirdi.
  const overlaySig = JSON.stringify([
    thresholds ?? null, regions ?? null, stacked ? null : (bands ?? null), stackedBands,
  ]);

  // Görünen aralık (uPlot x scale) — legend istatistikleri bundan.
  const [xWin, setXWin] = useState<[number, number] | null>(null);

  // v0.9.793 — "uPlot canlı mı" TEK ifade. Hem JSX kapısı hem de plotRef'e
  // bağımlı efektlerin tetiği: örnek MOUNT ANINDA doğar (ilk render'da
  // width 0'dır, ResizeObserver ölçene kadar UPlotChart hiç çizilmez), o
  // yüzden yalnız kendi girdisine bakan bir efekt plotRef'i sonsuza dek
  // null görebilir. focusedLabel bunu canlı yakaladı: mount'ta verilen odak
  // hiç uygulanmıyordu (hover'la gelen odak çalıştığı için gözden kaçardı).
  const plotMounted = data.state === 'ready' && width > 0 && aligned.data[0].length >= 2;

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
  // v0.9.788 — HAM matris ref'i. Tooltip bugüne dek u.data'dan okuyordu;
  // stacked'te u.data KÜMÜLATİF olduğu için oradan okumak yalan olurdu.
  // Her modda buradan okunur: line/bars/area'da drawData === aligned.data
  // olduğundan davranış bayt bayt aynıdır, stacked'te ise doğru olur.
  const rawRef = useRef(aligned.data);
  rawRef.current = aligned.data;

  // ── v0.9.792 — tooltip sabitleme (Grafana-parite #2, v2 motorunda) ────────
  // pinRef: pinli veri index'i (null = pin yok). Dört preset'in (OVC/TC/MLC/
  // TSP) taşıdığı sözleşmenin CorePanel karşılığı; karar mantığı saf ve
  // paylaşımlı (tooltipPin.decidePinGesture), burada yalnız DOM tarafı var.
  // Ad da paylaşımlı: dört preset'in hepsinde `pinRef`.
  //
  // Ref, state DEĞİL — ve hiçbir bağımlılık dizisine GİRMEZ (v0.9.704 kimlik
  // dersi): pin bir çizim durumu değil, tooltip DOM'unun donma anahtarıdır.
  // State olsaydı her pin/unpin panelin tümünü yeniden render ederdi; config
  // dizisine sızsaydı uPlot'u destroy/recreate ederdi. corePanelContracts'ın
  // 'pinRef' + 'pinnedIdx' dep yasağı pinleri bunu çiviler.
  const pinRef = useRef<number | null>(null);
  const unpinTooltip = () => {
    pinRef.current = null;
    if (ttRef.current) clearPinStyle(ttRef.current, 'display');
  };
  // Esc → pin çöz. Tam ekranın ESC'inden AYRI dinleyici: pin fullscreen'siz
  // de yaşar. unpinTooltip yalnız ref'lere dokunur → dizi boş kalabilir.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && pinRef.current != null) unpinTooltip(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    // v0.9.808 — döngü ÇİZİM pozisyonunda: uPlot'un series[i+1]'i çizim
    // sırasındaki seridir, görünürlük bayrağı ise mantıksal seriye ait.
    // Kimlik sırasında (yığılmış çubuk dışındaki her mark) drawOrder[i]===i
    // olduğundan davranış bayt bayt eskisidir.
    drawOrder.forEach((li, i) => {
      const show = vis[li];
      if (u.series[i + 1] && u.series[i + 1].show !== show) {
        u.setSeries(i + 1, { show }, false);
      }
    });
    u.redraw(false, true); // v0.9.744 — gizlenen serinin ◆'ları da kalksın
  }, [drawOrder, vis]);

  // ── v0.9.799 — y ekseni oluk genişliği (operatör: "00 req/s") ────────────
  //
  // Kök neden ve neden Grafana'nın otomatiğine güvenemediğimiz
  // axisSize.ts'in dosya başında: measureText fontu SABİT 'Inter' yazıyor,
  // biz o fontu kullanmıyoruz, ölçüm çizilenden dar çıkıyor ve etiketin
  // baş rakamı kırpılıyor.
  //
  // Burada tick kümesi ÇİZİMDEN ÖNCE tahmin edilir (uPlot'un 1/2/5
  // merdiveni), etiketler panelin KENDİ display processor'ıyla üretilir
  // (birim + ondalık tek kaynaktan) ve GERÇEK eksen fontuyla ölçülür.
  //
  // Uçlar TÜM seriden okunur — gizli seriler ve zoom penceresi dışarıda
  // kalan noktalar dahil. uPlot ölçeği onları dışlar, yani oluk gerektiği
  // kadar VEYA biraz daha geniş çıkar. Yön bilinçli: fazla oluk birkaç
  // piksel yer yer, eksik oluk etiketi kırpar (düzelttiğimiz kusur).
  const yGutterNeeded = useMemo(() => {
    if (data.state !== 'ready' || frames.length === 0) return 0;
    const ext = seriesExtent(drawData.slice(1) as (number | null)[][]);
    if (!ext) return 0;
    // Log ölçekte dolgu yok (decade'ler zaten sınırları kapsıyor).
    const [lo, hi] = effLog ? ext : paddedExtent(ext);
    // Çizim yüksekliği ≈ panel yüksekliği − x ekseni şeridi.
    const plan = axisTickPlan(lo, hi, Math.max(40, height - 34), effLog);
    if (plan.ticks.length === 0) return 0;
    const dec = decimalsForIncr(plan.incr);
    const disp = frames[0]?.fields[1].display;
    const labels = plan.ticks.map((v) => {
      if (!disp) return fmtSmart(v);
      const d = disp(v, dec > 0 ? dec : undefined);
      return `${d.text}${d.suffix ?? ''}`;
    });
    return axisGutterPx(
      widestLabelPx(labels, `${AXIS_FONT_SIZE}px ${chartTheme().typography.fontFamily}`),
      width);
    // themeTick: font ailesi temayla değişebilir → ölçüm tazelenir.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data.state, frames, drawData, effLog, height, width, themeTick]);

  // MANDAL — oluk seri kümesi boyunca YALNIZ BÜYÜR. Genişlik config'in bir
  // parçası ve config kimliği değişince UPlotChart uPlot'u
  // destroy/recreate ediyor (v0.9.704). Son değeri 98↔102 arasında
  // salınan bir panelde oluğu her poll'da yeniden hesaplayıp küçültseydik
  // grafik 10 saniyede bir yeniden doğardı. Küçülme YALNIZ seri kümesi
  // değişince olur — o an config zaten yeniden kuruluyor, yani bedava.
  const seriesSig = aligned.names.join('|');
  const [yGutter, setYGutter] = useState<{ sig: string; px: number }>({ sig: '', px: 0 });
  useEffect(() => {
    setYGutter(prev => {
      if (prev.sig !== seriesSig) return { sig: seriesSig, px: yGutterNeeded };
      return yGutterNeeded > prev.px ? { sig: seriesSig, px: yGutterNeeded } : prev;
    });
  }, [yGutterNeeded, seriesSig]);

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
      // v0.9.774 — eksen BİRİMLİ. Tooltip (aşağıda) v0.9.710'dan beri
      // display processor'dan geçiyordu, eksen ise ham fmtSmart'taydı:
      // aynı panelde tooltip "1.04 s" derken eksen "1042" yazıyordu.
      // Birim ÇEVİRİSİ elle yazılmaz (dataFrame.ts sözleşmesi) — ilk
      // frame'in display'i çağrılır. Frame'ler ref'ten CANLI okunur:
      // config bağımlılığına girmezler, yani birim değişimi uPlot'u
      // destroy/recreate ETMEZ (v0.9.704 kimlik dersi).
      //
      // v0.9.799 (operatör-bildirimi: "failure-rate panelinde her tick
      // '0 req/s'") — İKİNCİ PARAMETRE artık geçiriliyor. @grafana/ui'nin
      // eksen sarmalayıcısı tick ARTIŞINDAN ondalık türetip
      // formatValue(v, decimals) diye çağırıyor; biz onu YUTUYORDUK ve
      // display processor kendi otomatiğine düşüyordu — küçük aralıkta
      // ya "0.0500" gibi gereksiz uzun ya da (kırpılınca) "0" okunuyordu.
      // Adı `adjacentDecimals`: verildiğinde processor sondaki sıfırları
      // da kırpar, yani 0-0.2 aralığı "0.05 / 0.1 / 0.15" olur, 0-300
      // aralığı tam sayı kalır. Grafana TimeSeries ekseninin birebiri.
      formatValue: (v: unknown, decimals?: DecimalCount) => {
        const n = typeof v === 'number' ? v : Number(v);
        const disp = framesRef.current[0]?.fields[1].display;
        if (!disp || n == null || !isFinite(n)) return fmtSmart(n);
        const d = disp(n, decimals);
        return `${d.text}${d.suffix ?? ''}`;
      },
      // v0.9.799 — OLUK GENİŞLİĞİ BİZDEN (operatör-bildirimi: "eksende
      // 00 req/s yazıyor"). Grafana'nın otomatiği en uzun etiketi SABİT
      // 'Inter' fontuyla ölçüyor; Coremetry'de o font yok, ölçüm çizilen
      // fonttan dar çıkıyor ve etiketin baş rakamı kırpılıyor
      // (axisSize.ts dosya başı). 0 iken Grafana otomatiği devrede kalır
      // — ilk boyanmada (ölçüm henüz yokken) davranış bugünküdür.
      size: yGutter.px || undefined,
    });
    // v0.9.785 — bars markı. Grafana'nın kendi UPlotSeriesBuilder'ı
    // drawStyle=Bars görünce path builder'ı `bars({size:[barWidthFactor,
    // barMaxWidth]})` ile kurar. İKİ alan da ŞART:
    //   • barWidthFactor 0.6 → bucket'ın %60'ı çubuk, %40'ı nefes payı.
    //   • barMaxWidth 40px → v0.9.245 dersi ("barlar çok büyük"): eski
    //     motor [0.85, Infinity] kullanıyordu ve seyrek pencerede tek
    //     çubuk yarım paneli kaplıyordu. Tavan olmadan bars okunmuyor.
    // showPoints AÇIKÇA geçilir: builder Auto'yu görmezse points.show'u
    // hiç set etmez ve uPlot çubukların TEPESİNE nokta basar
    // (UPlotSeriesBuilder.mjs — Auto + Bars = points kapalı).
    //
    // v0.9.788 — area/stacked dalları. area SADECE dolgu kalınlığıdır
    // (Grafana'nın "Fill opacity" kaydırağı); stacked'in çizgi/mark
    // ayarı da line'ın aynısıdır — farkı VERİ (kümülatif matris) ve
    // BANTLAR yapar, mark değil.
    //
    // v0.9.808 — 'stacked-bars' İKİ dala birden girer: mark tarafında
    // `bars` (DrawStyle.Bars + genişlik tavanı), veri tarafında `stacked`
    // (kümülatif matris + pxAlign + düz dolgu). Ayrıştığı tek yer dolgu
    // OPAKLIĞI: kümülatif çubuklar tabandan çizildiği için üst üste
    // biner ve yarı saydam bir çubuk altındaki daha uzun çubuğu gösterip
    // rengi kirletir — 100 bir tercih değil, ters çizim sırasının şartı.
    //
    // DÖNGÜ ÇİZİM SIRASINDA, `i` MANTIKSAL indeks: rol/kesikli/renk hep
    // mantıksal seriye ait, uPlot'a eklenme SIRASI ise drawOrder'dan gelir.
    const bars = viz === 'bars' || stackedBars;
    const area = viz === 'area';
    drawOrder.forEach((i) => {
      const name = aligned.names[i];
      b.addSeries({
        scaleKey: 'y', theme,
        lineColor: resolveVar(seriesRoleColor(name, roles?.[i] ?? 'data')),
        // Çubuk kenarı ince (1) — 1.5px stroke dar çubuğu şişman gösterir.
        lineWidth: bars ? 1 : 1.5,
        // v0.9.93 (uPlot Aşama 3) dersinin bu motordaki karşılığı: yığın
        // dolguları arasında saç-teli beyaz dikişler kalır çünkü komşu
        // katmanların kenarları ayrı ayrı piksele yuvarlanır. pxAlign
        // kapatılınca dolgular sürekli hizalanır. `false` uPlot'ta 0'a
        // eşittir (`s.pxAlign = +ifNull(s.pxAlign, 1)`); non-stacked'te
        // dokunulmaz, crisp gridline için varsayılan 1 kalır.
        pxAlign: stacked ? false : undefined,
        // v0.9.756 (operatör: "çizgiler basit geldi") — Grafana'nın imza
        // görünümü: çizgi renginden türeyen hafif opaklık-degradeli alan
        // dolgusu (fillOpacity 12, Opacity gradyanı). Tema-canlı: renk
        // rebuild'de çözülür (themeTick dep'i zaten var).
        // Bars'ta dolgu ASIL gövdedir (12 solgun kalırdı) → 35.
        // v0.9.788 — area 60 (dolgu markın kendisi); stacked 28, eski
        // motorun '47' alpha'sının (0x47/0xff ≈ %28) birebiri.
        // v0.9.808 — stackedBars dalı EN ÖNDE: `bars` onu da kapsıyor ama
        // 35 saydamlık kümülatif çubuklarda yanlış renk üretir (yukarıya bkz).
        fillOpacity: stackedBars ? 100 : bars ? 35 : area ? 60 : stacked ? 28 : (dashed?.[i] ? 0 : 12),
        // Stacked'te dolgu DÜZ olmalı: bantlar üst serinin dolgusunu
        // yeniden kullanıyor (aşağıya bkz.) ve degrade bir bandın içinde
        // katman sınırını bulanıklaştırır. None + fillOpacity =
        // alpha(lineColor, %28) — renk yine token kanalından gelir.
        gradientMode: stacked ? GraphGradientMode.None : GraphGradientMode.Opacity,
        // Line dalı bayt-bayt eskisi: bar alanları yalnız bars'ta var.
        ...(bars ? {
          drawStyle: DrawStyle.Bars,
          showPoints: PointVisibility.Auto,
          barWidthFactor: 0.6,
          barMaxWidth: 40,
        } : {}),
        lineStyle: dashed?.[i] ? { fill: 'dash' as const, dash: [5, 4] } : undefined,
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
    // ── v0.9.799 — İMLEÇ NOKTALARI (operatör: "çizgiler üzerinde imleçle
    // ilerlediğimde nokta hareket etse güzel olur") ─────────────────────
    //
    // uPlot'un imleç noktaları ZATEN açıktı ama GÖRÜNMÜYORDU, ve sebebi
    // kaynakta okunabilir bir zincir: @grafana/ui'nin seri kurucusu
    // `points.size`ı KOŞULLU yazıyor (`!pointSize || pointSize < lineWidth
    // ? undefined : pointSize`) ve biz pointSize vermiyoruz → nesnede
    // `size: undefined` ALANI var. uPlot varsayılanları Object.assign ile
    // birleştiriyor, yani tanımlı-ama-undefined alan varsayılan çapı
    // (ptDia) EZİYOR. Grafana'nın imleç varsayılanı da o çapı okuyor
    // (`series[i].points.size * 2`) → NaN → `width: NaNpx` → sıfır
    // boyutlu, görünmez bir div. Grafana'nın kendi TimeSeries'inde bu
    // olmuyor çünkü orada pointSize her zaman bir sayı.
    //
    // Düzeltme SAYIYI BURADA vermek: 10px kutu + 2px kenarlık = 6px dolu
    // çekirdek + hale halkası (uplot.css'te box-sizing:border-box ve
    // background-clip:padding-box). Renk kanalına DOKUNMUYORUZ — dolgu
    // seri rengi, kenarlık aynı renk %50 alfa; ikisi de Grafana'nın
    // pointColorFn'inden geliyor ve seri rengi zaten tema token'ından
    // çözülüyor (seriesRoleColor → resolveVar).
    //
    // `show` GEÇİLMEZ ve geçilmemeli: uPlot `points.show`u fnOrSelf'ten
    // geçirip dönen değerin HTMLElement olmasını bekliyor. `show: true`
    // yazmak `() => true` üretir, `true instanceof HTMLElement` false'tur
    // ve noktalar HİÇ oluşturulmaz — yani "açmak" için true yazmak tam
    // tersini yapar.
    //
    // STATİK ayar: bir bağımlılık gerektirmez (v0.9.704 kimlik disiplini).
    b.setCursor({
      points: { size: CURSOR_POINT_PX, width: CURSOR_POINT_BORDER_PX },
      ...(syncKey ? { sync: { key: syncKey } } : {}),
    });
    // v0.9.788 — İKİ bant kaynağı vardır ve AYNI ANDA kullanılamazlar:
    // uPlot'ta bir seri en fazla BİR bandın üst kenarı olabilir
    // (`bands.find(b => b.series[0] == si)`), iki liste çakışırsa katman
    // dolgusu sessizce p50-p99 bandına kayar. Bu yüzden stacked iken
    // PROP bands YOK SAYILIR — yığma bir mark kararıdır; çağıran ikisini
    // birden istiyorsa mark yanlış seçilmiştir.
    if (stacked) {
      // Yığın bantları: ardışık görünür katmanların arası. fill BİLEREK
      // verilmez — uPlot bant dolgusu boşsa üst serinin kendi dolgusuna
      // düşer (`b.fill(self, bi) || fillStyle`), yani katman rengi tek
      // kanaldan (seriesRoleColor → fillOpacity) gelir ve burada ikinci
      // bir palet doğmaz.
      for (const sb of stackedBands ?? []) b.addBand({ series: sb.series });
    } else {
      // p50-p99 bandı: uPlot native band — iki mevcut serinin arası dolar.
      for (const band of bands ?? []) {
        b.addBand({
          series: [band.above, band.below],
          fill: resolveVar(band.fill ?? 'var(--accent-soft)'),
        });
      }
    }
    // Eşik çizgileri + annotation bölgeleri: M3 çizim çekirdeği. Renk
    // token'ları DRAW anında çözülür (tema-canlı) — build anında değil;
    // themeTick yalnız seri renklerini tazeler.
    {
      b.addHook('draw', (u) => {
        if (regions?.length) drawTimeRegions(u, regions);
        // v0.9.744 — exemplar ◆'ları en son (çizgilerin üstünde);
        // ref'ten canlı okunur, halo panel arka planından.
        //
        // v0.9.788 — stacked'te BASTIRILIR. ◆'ın y'si HAM değerden
        // valToPos ile bulunuyor (overlays.ts:219/254) ama çizilen çizgi
        // KÜMÜLATİF: elmas katmandan kopar, rastgele bir yükseklikte
        // asılı kalır ve tık isabeti (exemplarAt, aynı ham hesap)
        // görünenle uyuşmaz. Yığın kümülatif olduğu sürece "bu noktadaki
        // trace" diye gösterilecek dürüst bir konum yok — çizmemek,
        // yanlış yere çizmekten iyidir.
        if (!stacked && exemplarsRef.current?.some(e => e?.length)) {
          drawExemplars(u, exemplarsRef.current, visRef.current, resolveVar,
            resolveVar('var(--bg1)') || '#0d1117');
        }
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
      // v0.9.792 — pencere değişiyorsa pin bayatlar (pinli index başka bir
      // zamana denk gelir): zoom pin'i çözer. "ikinci tık / Esc / pan çözer".
      unpinTooltip();
      onZoomRef.current(fromSec, toSec);
    });
    // v0.9.710 — tooltip: "tüm seriler, değere göre sıralı" (spec
    // varsayılanı). Saf çekirdekler TimeChart'la ORTAK: sortedTooltipRows
    // (gizli seri düşer, gap 0 okumaz) + placeTooltip (flip/clamp) +
    // fmtTooltipTime (tarih koşulsuz, çözünürlük adımdan). Değer biçimi
    // DataFrame display processor'dan — birim çevirisi köprü sözleşmesi
    // gereği elle yazılmaz.
    b.addHook('setCursor', (u) => {
      // v0.9.745 — crosshair zaman kanalı (cursorBus): tooltip'ten
      // bağımsız, ref üzerinden canlı.
      const idx0 = u.cursor.idx;
      if (onCursorTimeRef.current) {
        if (idx0 == null || u.cursor.left == null || u.cursor.left < 0) {
          onCursorTimeRef.current(null);
        } else {
          const t = (u.data[0] as number[])[idx0];
          if (t != null) onCursorTimeRef.current(t / 1000);
        }
      }
      const tt = ttRef.current;
      if (!tt) return;
      // v0.9.792 — PİNLİYKEN tooltip donuk: içerik de konum da dokunulmaz.
      // Guard cursorTime kanalının SONRASINDA (TSP:600 sırası): crosshair ve
      // senkron paneller yaşamaya devam eder, yalnız kutu donar.
      if (pinRef.current != null) return;
      const idx = u.cursor.idx;
      if (idx == null || u.cursor.left == null || u.cursor.left < 0) {
        tt.style.display = 'none';
        return;
      }
      const xs = u.data[0] as number[];
      const tMs = xs[idx];
      if (tMs == null) { tt.style.display = 'none'; return; }
      const stepSec = xs.length > 1 ? Math.abs(xs[1] - xs[0]) / 1000 : null;
      const rows = capTooltipRows(sortedTooltipRows(aligned.names.map((label, i) => {
        // v0.9.808 — satırlar MANTIKSAL sırada kurulur (lejantla aynı),
        // ama uPlot'un imleç indeksi ÇİZİM pozisyonundan okunur: yığılmış
        // çubukta ikisi ters. Ham değer yine rawRef[i+1]'den, yani mantıksal
        // seriden — tooltip hangi çizim sırasında olursa olsun doğru katmanı
        // gösterir.
        const si = u.cursor.idxs?.[drawPos[i] + 1] ?? idx;
        // v0.9.788 — değer u.data'dan DEĞİL ham matris ref'inden. Stacked
        // panelde u.data kümülatiftir; oradan okunan "değer" katmanın
        // kendi değeri değil altındakilerin toplamıdır.
        const v = visRef.current[i] === false ? null
          : ((rawRef.current[i + 1] as (number | null)[] | undefined)?.[si] ?? null);
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
      })));
      if (rows.length === 0) { tt.style.display = 'none'; return; }
      tt.innerHTML = `<div class="ov-tt-t">${fmtTooltipTime(tMs / 1000, stepSec)}</div>` + rows.map(r =>
        `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${r.color}"></i>${r.label}</span><b>${r.text}</b></div>`,
      ).join('') + PIN_TIP_HTML;
      tt.style.display = 'block';
      const host = wrapRef.current;
      if (host) {
        // v0.9.755 (operatör: "tooltip imlecin üzerinde kalıyor") —
        // kıstırma sınırı KART değil EKRAN: 200px'lik panelde 8 satırlık
        // kutu iki eksende de yer bulamayıp imlece ortalanıyordu.
        // Kutu kartın dışına taşabilir (absolute + z-index), yalnız
        // viewport'a kıstırılır → "imlecin sonrası/öncesi" hemen her
        // zaman yer bulur, işaretçi kutunun ÜSTÜNE gelmez.
        const hr = host.getBoundingClientRect();
        const pl = placeTooltip(
          u.cursor.left ?? 0, u.cursor.top ?? 0,
          tt.offsetWidth, tt.offsetHeight,
          u.over.clientWidth, u.over.clientHeight,
          u.over.offsetLeft, u.over.offsetTop,
          Math.max(host.clientWidth, window.innerWidth - hr.left - 8),
          Math.max(host.clientHeight, window.innerHeight - hr.top - 8),
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
    //
    // v0.9.785 — ÜÇ eksik bağımlılık kapatıldı. Bu useMemo config'i kurar
    // ve config === ile karşılaştırılır (v0.9.704): dizide OLMAYAN bir
    // girdi değiştiğinde uPlot ESKİ config'le çizmeye devam eder, yani
    // değişiklik sessizce YUTULUR. Üçü de gövdede OKUNUYORDU:
    //   • viz      — bars↔line geçişi seri ADLARINI değiştirmez, o yüzden
    //                names.join() imzası sabit kalır ve panel çizgi
    //                kalırdı (bu sürümün asıl hatası; yeni prop olduğu
    //                için ilk günden latent değil, doğrudan bozuk olurdu).
    //   • dashed   — join(',') ile İÇERİK imzası (dizi kimliği değil:
    //                inline [] her render'da yeni kimlik = sürekli yıkım).
    //   • connectNulls — spanNulls eşiği; panel-başına açık tercih
    //                değişince boşluk doktrini gerçekten değişmeliydi.
    //   • yGutter.px — eksen oluk genişliği (v0.9.799). Bir ÇİZİM girdisi
    //                değil ÖLÇÜ; mandal yüzünden yalnız büyür, yani
    //                rebuild seyrek ve gerçekten gerekli olduğunda olur.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [aligned.names.join(' '), roles?.join(), syncKey, effLog, themeTick, overlaySig, xRange?.from, xRange?.to, viz, dashed?.join(','), connectNulls, yGutter.px]);

  // ── v0.9.793 — focusedLabel (lejant hover vurgusu) ───────────────────────
  //
  // İki sürücü, TEK kanal: kontrollü prop (Explore → GroupTable satırı) ya da
  // panelin KENDİ lejant satırı hover'ı. Prop kazanır; verilmezse iç hover.
  const [hoverName, setHoverName] = useState<string | null>(null);
  const focusName = focusedLabel ?? hoverName;
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    // Taban genişlik config'in KENDİSİNDEN okunur (builder props'u) — burada
    // ikinci bir "1.5 / bars ise 1" kopyası tutmuyoruz; mark değişince taban
    // kendiliğinden doğru gelir.
    // v0.9.808 — config.getSeries() ÇİZİM sırasındadır, resolveFocusIdx ise
    // MANTIKSAL indeks döndürür (adlar mantıksal sırada). Yığılmış çubukta
    // ikisi ters: çeviri yapılmazsa lejantın en üst satırına gelen imleç
    // en alttaki seriyi vurgulardı.
    const focusIdx = resolveFocusIdx(aligned.names, focusName);
    const styles = focusSeriesStyle(
      config.getSeries().map(s => s.props.lineWidth),
      focusIdx < 0 ? -1 : (drawPos[focusIdx] ?? -1));
    let changed = false;
    styles.forEach((st, i) => {
      const s = u.series[i + 1];
      if (!s) return;
      if (s.alpha !== st.alpha) { s.alpha = st.alpha; changed = true; }
      if (s.width !== st.width) { s.width = st.width; changed = true; }
    });
    // rebuildPaths=true: kalınlık path'e giriyor, cache'li Path2D eskir.
    // uPlot'u YIKMIYORUZ — bu sadece bir yeniden çizim (v0.9.704 dersi).
    if (changed) u.redraw(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusName, config, drawPos, aligned.names.join('|'), plotMounted]);

  // v0.9.792 — config kimliği değişince UPlotChart destroy/recreate eder ve
  // pinli index bayat bir kutuya işaret eder (eksen/seri kümesi değişmiş
  // olabilir). Rebuild pin'i çözer — TSP'nin afterBuild sözleşmesi.
  useEffect(() => {
    if (pinRef.current != null) unpinTooltip();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config]);

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

  // v0.9.774 — lejant hücreleri de BİRİMLİ (eksen + tooltip ile aynı
  // kaynak). field.display'i köprü (dataFrame.ts) frame'i kurarken
  // yarattı; burada yalnız ÇAĞRILIYOR — hücre başına yeni nesne/işlemci
  // üretilmiyor (sıcak yol), ve birim mantığı köprünün tekelinde kalıyor.
  const fmtCell = (i: number, v: number | null): string => {
    if (v == null || !isFinite(v)) return fmtSmart(v);
    const disp = frames[i]?.fields[1].display;
    if (!disp) return fmtSmart(v);
    const d = disp(v);
    return `${d.text}${d.suffix ?? ''}`;
  };
  // TOPLAM yalnız toplanabilir birimde anlamlı — v1 StatsLegend paritesi
  // (legendStats.isAdditiveUnit): pod'lar arası p95 latency'yi toplamak
  // anlamsız, yüzdeleri toplamak yanlış. Birimsiz panel bugünkü davranışta
  // kalır (isAdditiveUnit('') === true).
  const sumAdditive = isAdditiveUnit(frames[0]?.fields[1].config.unit);

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
    // v0.9.743 (operatör): Series lejantı VARSAYILAN KAPALI gelir —
    // kullanıcı isterse açar; localStorage'daki kullanıcı tercihi
    // (stored) her zaman kazanır.
    setLegendOpen(!resolveLegendCollapsed(stored, true, n, 6));
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

      <div ref={wrapRef} style={{ minHeight: height, position: 'relative', cursor: (onExpandClick || onBucketClick) && !fullscreen ? 'pointer' : undefined }}
        // v0.9.792 — dinleyiciler KOŞULSUZ bağlı: pin jesti her panelde
        // çalışır (bir tık callback'i olmayan panelde de). Zincirin geri
        // kalanı aşağıda kendi kapılarını koruyor.
        onPointerDown={(e) => { clickRef.current = { x: e.clientX, y: e.clientY }; }}
        onClick={(e) => {
          const d = clickRef.current;
          const u = plotRef.current;
          // v0.9.792 — PIN ÖNCE. Shift+tık (operatör jesti) VE Alt+tık (MLC
          // v1 kas hafızası) pinler; PİNLİYKEN her tık unpin eder ve alttaki
          // zincire DÜŞMEZ (MLC:387 sözleşmesinin birebiri). Karar saf
          // çekirdekte: sürükleme kuyruğu / çift-tık click'i / boş imleç
          // 'swallow' döner — Shift'li bir sürükleme exemplar açamaz.
          const g = decidePinGesture({
            pinnedIdx: pinRef.current,
            cursorIdx: u?.cursor.idx,
            shiftKey: e.shiftKey, altKey: e.altKey,
            dragPx: d ? Math.abs(e.clientX - d.x) : 0,
            detail: e.detail,
          });
          if (g.action === 'unpin') { unpinTooltip(); return; }
          if (g.action === 'pin') {
            const tt = ttRef.current;
            if (tt && tt.style.display !== 'none') { pinRef.current = g.idx; applyPinStyle(tt); }
            return;
          }
          if (g.action === 'swallow') return;
          // ── mevcut jest zinciri (◆ > bucket > panel eylemi) ─────────────
          if (!onExpandClick && !onExemplarClick && !onBucketClick) return;
          if (fullscreen) return;
          // Drag-zoom basışı tık değildir (5px eşiği).
          if (d && Math.hypot(e.clientX - d.x, e.clientY - d.y) > 5) return;
          // v0.9.744 — ◆ isabeti ÖNCELİKLİ: trace açar, panel eylemi
          // (navigasyon) devreye girmez. Bekleme yok — anında.
          // stacked'te ◆ çizilmiyor (draw hook'u bastırıyor) → isabet
          // testi de kapalı: görünmeyen bir işarete tıklatmak olmaz.
          if (u && !stacked && exemplarClickRef.current && exemplarsRef.current?.some(x => x?.length)) {
            const r = u.over.getBoundingClientRect();
            const hit = exemplarAt(u, exemplarsRef.current, visRef.current,
              e.clientX - r.left, e.clientY - r.top);
            if (hit) {
              if (clickTimerRef.current) window.clearTimeout(clickTimerRef.current);
              exemplarClickRef.current(hit.traceId);
              return;
            }
          }
          // v0.9.789 — bucket-tık: ◆ isabeti YOKSA sıra burada (MLC'nin
          // v0.7.22 sırası: ◆ varsa o kazanır). Çift-tık zoom-geri
          // jestidir ve ilk tıkı bir exemplar çekmecesi açmamalı — bu
          // yüzden onExpandClick'le AYNI 250ms bekleme kullanılır:
          // ikinci tık/dblclick zamanlayıcıyı iptal eder.
          if (bucketClickRef.current) {
            if (clickTimerRef.current) window.clearTimeout(clickTimerRef.current);
            const w = u ? bucketWindowAt(u, e.clientX, e.clientY) : null;
            if (!w) return;
            const cb = bucketClickRef.current;
            clickTimerRef.current = window.setTimeout(() => cb(w.fromNs, w.toNs), 250);
            return;
          }
          if (!onExpandClick) return;
          if (clickTimerRef.current) window.clearTimeout(clickTimerRef.current);
          clickTimerRef.current = window.setTimeout(() => onExpandClick(), 250);
        }}
        onDoubleClick={(e) => {
          if (clickTimerRef.current) { window.clearTimeout(clickTimerRef.current); clickTimerRef.current = null; }
          // v0.9.792 — çift-tık (zoom-geri) pin'i DETERMİNİSTİK çözer: aksi
          // halde geri adımdan sonra bayat bir pinli kutu asılı kalırdı
          // (dört preset'in dblclick dinleyicisiyle aynı gerekçe).
          unpinTooltip();
          onZoomReset?.();
          void e;
        }}
        // v0.9.792 — pinliyken imleç ayrılması kutuyu KAPATMAZ: pin'in tek
        // vaadi bu ("imleç ayrılınca da sabit kalır").
        onMouseLeave={() => { if (pinRef.current == null && ttRef.current) ttRef.current.style.display = 'none'; }}>
        {/* v0.9.710 — tooltip overlay; .ov-tt sınıfları evdeki tooltip
            görseliyle birebir (OVC/TC/MLC aynı CSS'i kullanıyor). */}
        <div ref={ttRef} className="ov-tt" style={{ display: 'none', position: 'absolute', zIndex: 10, pointerEvents: 'none' }} />
        {/* v0.9.789 — bucket-tık afordansı. MLC'nin "click → exemplar"
            rozetinin v2 karşılığı; dil ve token'lar panelin note satırıyla
            aynı (10px / var(--text3)), imleç zaten pointer. pointerEvents
            none: rozet kendi üstündeki tıkı yutmaz. */}
        {onBucketClick && data.state === 'ready' && (
          <div style={{
            position: 'absolute', top: 2, right: 6, zIndex: 6,
            fontSize: 10, color: 'var(--text3)', pointerEvents: 'none', opacity: 0.75,
          }}>
            tık → örnek trace
          </div>
        )}
        {data.state === 'loading' && <Spinner />}
        {data.state === 'error' && (
          <Empty icon="⚠" title="Grafik yüklenemedi">{data.message}</Empty>
        )}
        {data.state === 'empty' && (
          <Empty icon="◫" title={data.reason}>{data.hint ?? ''}</Empty>
        )}
        {plotMounted && (
          // v0.9.788 — ÇİZİME giden matris (stacked'te kümülatif); ham
          // matris aligned'da durur ve tooltip/lejant/CSV oradan okur.
          <UPlotChart data={drawData} width={width} height={height} config={config}
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

      {data.state === 'ready' && aligned.names.length > 0 && !hideLegend && (
        <div style={{ fontSize: 11 }}>
          <button className="sec" style={{ fontSize: 10, padding: '1px 6px' }}
            onClick={toggleLegend}>
            {legendOpen ? '▼' : '▶'} Series ({aligned.names.length})
          </button>
          {legendOpen && (
            <div style={{ overflowX: 'auto' }}>
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
                    style={{
                      opacity: vis[i] === false ? 0.35 : 1, cursor: 'pointer',
                      background: focusName === s.name ? 'var(--bg2)' : undefined,
                    }}
                    // v0.9.793 — hover/fokus SERİYİ VURGULAR (TSP sözleşmesi).
                    // Klavye fokusu da aynı kanaldan geçer: vurgu yalnız fareye
                    // ait olsaydı Tab'la gezen operatör hangi satırın hangi
                    // çizgi olduğunu göremezdi (v0.9.711 erişim dersi).
                    onMouseEnter={() => setHoverName(s.name)}
                    onMouseLeave={() => setHoverName(n => (n === s.name ? null : n))}
                    onFocus={() => setHoverName(s.name)}
                    onBlur={() => setHoverName(n => (n === s.name ? null : n))}
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
                    <td className="num">{fmtCell(i, s.stat.last)}</td>
                    <td className="num">{fmtCell(i, s.stat.min)}</td>
                    <td className="num">{fmtCell(i, s.stat.max)}</td>
                    <td className="num">{fmtCell(i, s.stat.mean)}</td>
                    <td className="num">{sumAdditive ? fmtCell(i, s.stat.sum) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
