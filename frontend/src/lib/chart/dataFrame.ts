// dataFrame.ts — Coremetry API yanıtı → @grafana/data DataFrame köprüsü.
//
// FAZ 1 (grafana-ui chart katmanı). SÖZLEŞME:
//
//   • Dönüşüm YALNIZ burada yaşar. Bir sayfa/panel API yanıtından kendi
//     eliyle DataFrame kurmaz — kurarsa köprünün varlık sebebi biter ve
//     birim/eşik/null davranışı sayfa sayfa sapar (aynı hastalığın uPlot
//     hâli dört kopya chart bileşeniydi; v0.9.97 engine.ts o yüzden var).
//
//   • EKSİK VERİ NULL'DIR, SIFIR DEĞİL. gapPolicy.ts'in bütün varlık
//     sebebi "kaçmış scrape ile gerçek kesinti aynı görünmesin" — burada
//     null'u 0'a çevirmek o ayrımı köprünün altında imha ederdi. API'de
//     value alanı hiç gelmeyen nokta null taşınır.
//
//   • RATE / COUNTER-RESET / QUANTILE BURADA HESAPLANMAZ. Sunucu yapıyor:
//     resetSafeDelta (metricrate.go) Prometheus reset-korumalı delta,
//     quantile'lar histogram bucket'tan / tDigest merge'ten geliyor.
//     Frontend'e ham cumulative gelip burada türev alınmaz — böyle bir
//     ihtiyaç doğarsa yanıt SUNUCU tarafında genişler.
//
//   • Birim ölçekleme ELLE YAZILMAZ. FieldConfig.unit +
//     getDisplayProcessor @grafana/data'nın işi; ms→s, bayt→MiB gibi
//     çevrimleri bizim yazdığımız her satır, oradaki davranışın kopyası
//     ve gelecekteki uyumsuzluğudur.
//
// Zaman birimi: Coremetry API'leri unix NANOSANİYE döndürür
// (SpanMetricSeries.points[].time), DataFrame zaman alanı MİLİSANİYE
// bekler (Grafana konvansiyonu). Çevrim tek yerde, burada.

import {
  FieldType,
  ThresholdsMode,
  getDisplayProcessor,
  createTheme,
  type DataFrame,
  type Field,
  type FieldConfig,
  type GrafanaTheme2,
} from '@grafana/data';
import type { SpanMetricSeries } from '@/lib/types';

// ---------------------------------------------------------------------------
// Tema: display processor renk/eşik çözerken tema ister. Coremetry'nin
// görsel kimliği CSS değişkenlerinde yaşıyor (globals.css) — Grafana
// temasının RENKLERİNİ kullanmıyoruz, yalnız processor'ın istediği yapıyı
// veriyoruz. isDark, data-theme'den okunur; resolveVar zaten bu deseni
// kullanıyor.
// ---------------------------------------------------------------------------
export function chartTheme(): GrafanaTheme2 {
  const isDark =
    typeof document !== 'undefined' &&
    document.documentElement.getAttribute('data-theme') !== 'light';
  return createTheme({ colors: { mode: isDark ? 'dark' : 'light' } });
}

// SeriesFrameOpts — köprünün panel başına aldığı görüntü sözleşmesi.
// unit, @grafana/data'nın birim kataloğundan bir kimliktir ('ms', 'reqps',
// 'bytes', 'percent'...). decimals verilmezse processor kendi karar verir.
export interface SeriesFrameOpts {
  unit?: string;
  decimals?: number;
  // Eşikler FieldConfig'e iner; çizim katmanı (FAZ 2) bunları okur.
  // Konfigürasyondan gelir — hard-code eden çağıran yanlış yerde.
  thresholds?: { steps: { value: number; color: string }[] };
  // Seri adı: groupKey'den türetilemeyen durumlar için (tek seri, sabit ad).
  name?: string;
}

// nsToMs — unix ns → unix ms. Number hassasiyeti ms düzeyinde güvenli
// (2^53 ns ≈ 104 gün ama ns değerleri zaten Number'a düşmüş geliyor;
// ms'e bölmek hassasiyeti DÜZELTİR, bozmaz).
export const NS_PER_MS = 1e6;

// spanSeriesToFrame — /api/spans/metric ve akrabalarının döndürdüğü
// SpanMetricSeries[] → DataFrame[]. Her seri kendi frame'i: Grafana
// modeli çok-serili tek frame'e de izin verir ama seri-başına frame,
// legend/renk eşlemesini (deterministik renk, FAZ 2) basit tutar.
export function spanSeriesToFrames(
  series: ReadonlyArray<SpanMetricSeries>,
  opts: SeriesFrameOpts = {},
  theme: GrafanaTheme2 = chartTheme(),
): DataFrame[] {
  return series.map(s => {
    const times: number[] = new Array(s.points.length);
    const values: (number | null)[] = new Array(s.points.length);
    for (let i = 0; i < s.points.length; i++) {
      const p = s.points[i];
      times[i] = p.time / NS_PER_MS;
      // EKSİK VERİ NULL: value alanı yoksa/NaN ise null taşınır. API
      // sayı döndürmediyse bu bir "sıfır ölçüldü" değil "ölçüm yok"tur.
      values[i] = typeof p.value === 'number' && Number.isFinite(p.value) ? p.value : null;
    }

    // v0.9.704 (self-review 🟡) — opts.name yalnız TEK seride ad olur.
    // Çok seride hepsine aynı adı basmak duplicate React key + tek renk
    // demekti (seriesRoleColor etikete göre çözer). Çok seride name
    // ÖNEK olur, kimlik groupKey'den gelir.
    const base = s.groupKey.length ? s.groupKey.join(' · ') : 'value';
    const name = opts.name == null ? base
      : (series.length === 1 ? opts.name : `${opts.name} · ${base}`);

    const config: FieldConfig = {
      unit: opts.unit,
      decimals: opts.decimals,
      ...(opts.thresholds
        ? {
            thresholds: {
              mode: ThresholdsMode.Absolute,
              steps: [
                // Grafana eşik modeli taban adım ister (-Infinity).
                { value: -Infinity, color: 'transparent' },
                ...opts.thresholds.steps,
              ],
            },
          }
        : {}),
    };

    const timeField: Field<number> = {
      name: 'time',
      type: FieldType.time,
      values: times,
      config: {},
    };
    const valueField: Field<number | null> = {
      name,
      type: FieldType.number,
      values,
      config,
    };
    // Birim/ondalık/eşik çözümü @grafana/data'nın display processor'ında —
    // elle ms→s çevrimi YASAK (sözleşme, dosya başı).
    valueField.display = getDisplayProcessor({ field: valueField, theme });

    const frame: DataFrame = {
      name,
      fields: [timeField, valueField],
      length: times.length,
      // v0.9.1369 — KISALTILMAMIŞ etiket frame'in meta'sında yolculuk
      // eder. Ayrı bir prop MultiLineChart → CorePanelMulti → CorePanel
      // boyunca üç imza değiştirirdi; frame zaten paneli baştan sona
      // geçen tek taşıyıcı ve seri kimliğiyle indeks-hizalı.
      ...(s.fullKey?.length
        ? { meta: { custom: { fullName: s.fullKey.join(' · ') } } }
        : {}),
    };
    return frame;
  });
}

// framesToAligned — DataFrame[] → uPlot AlignedData + seri adları.
//
// FAZ 1'in mevcut render katmanıyla buluşma noktası: bugünkü çizim uPlot
// (engine.ts) ve FAZ 2 kararına kadar öyle kalacak. Bu yüzden köprü iki
// yönlü değil TEK yönlü akışın ortasında bir istasyon:
//   API → DataFrame (model, birim, eşik) → AlignedData (çizim).
//
// Zaman ekseni birleştirme alignSeries.ts'in zaten çözdüğü problem —
// BURADA YENİDEN YAZILMAZ; farklı zaman kafesli seriler önce oradan
// geçer. Bu fonksiyon frame'lerin AYNI zaman dizisini paylaştığı
// (sunucu toStartOfInterval hizası, spec şartı) mutlu yolu kapsar ve
// paylaşmıyorlarsa null-dolgulu birleşim yapar.
export function framesToAligned(frames: ReadonlyArray<DataFrame>): {
  data: [number[], ...(number | null)[][]];
  names: string[];
} {
  if (frames.length === 0) return { data: [[]], names: [] };

  const t0 = frames[0].fields[0].values as number[];
  // v0.9.704 (self-review 🟠) — TAM eşitlik. İlk yazımım uzunluk+uçlara
  // bakıyordu ("tam tarama boşa iş" diye) ve seyrek bucket'lı seride
  // ÇÖKÜYORDU: A=[0,60,120,300], B=[0,60,240,300] aynı-kafes sayılıp
  // B'nin t=240 değeri x=120'ye çizilirdi — çizgi, legend istatistiği ve
  // CSV birden yanlış. Sunucu GERÇEKTEN seyrek üretiyor (WITH FILL yok;
  // alignSeries.ts v0.9.87 yorumu aynı sınıfı belgeliyor). Eleman
  // taraması sayı karşılaştırması: 800×6 ≈ 5k işlem, "pahalı" değil —
  // o varsayım erken optimizasyondu ve major bir kusur üretti.
  // Referans-paylaşım hızlı yolu duruyor (aynı diziyse tarama yok).
  const sameGrid = frames.every(f => {
    const t = f.fields[0].values as number[];
    if (t === t0) return true;
    if (t.length !== t0.length) return false;
    for (let i = 0; i < t.length; i++) if (t[i] !== t0[i]) return false;
    return true;
  });

  if (sameGrid) {
    return {
      data: [t0, ...frames.map(f => f.fields[1].values as (number | null)[])],
      names: frames.map(f => f.name ?? 'value'),
    };
  }

  // Kafesler farklı: null-dolgulu birleşim. Null DOLGUDUR, veri değil —
  // gapPolicy bunları kısa boşluksa köprüler, uzunsa kırar; 0 yazmak
  // burada da yasak.
  const allTimes = new Set<number>();
  for (const f of frames) for (const t of f.fields[0].values as number[]) allTimes.add(t);
  const merged = Array.from(allTimes).sort((a, b) => a - b);
  const index = new Map(merged.map((t, i) => [t, i]));

  const cols: (number | null)[][] = frames.map(f => {
    const col: (number | null)[] = new Array(merged.length).fill(null);
    const t = f.fields[0].values as number[];
    const v = f.fields[1].values as (number | null)[];
    for (let i = 0; i < t.length; i++) col[index.get(t[i])!] = v[i];
    return col;
  });

  return { data: [merged, ...cols], names: frames.map(f => f.name ?? 'value') };
}

// maxPointsForWidth — spec şartı: "bucket boyutu chart'ın PİKSEL
// genişliğinden türetilir; piksel sayısından fazla nokta çekilmez."
// Sorgu katmanı step'i buradan alır; hizalama sunucuda
// (toStartOfInterval), burada yalnız NOKTA BÜTÇESİ hesaplanır.
//
// devicePixelRatio bilinçli olarak YOK: retina'da 2× nokta çekmek görsel
// fark yaratmaz (çizgi zaten 1-2 px), sorgu maliyetini ikiye katlar.
export function maxPointsForWidth(cssWidthPx: number): number {
  // Alt sınır: çok dar panelde bile anlamlı eğri (min 50 nokta).
  return Math.max(50, Math.floor(cssWidthPx));
}

// stepSecondsFor — [from,to] ns aralığı + piksel genişliği → sunucuya
// gönderilecek step (saniye). Sunucu bunu toStartOfInterval'a verir.
// Step her zaman ≥1 sn ve nokta sayısı ≤ maxPointsForWidth olacak şekilde
// yukarı yuvarlanır.
export function stepSecondsFor(fromNs: number, toNs: number, cssWidthPx: number): number {
  const spanSec = Math.max(1, (toNs - fromNs) / 1e9);
  const budget = maxPointsForWidth(cssWidthPx);
  return Math.max(1, Math.ceil(spanSec / budget));
}
