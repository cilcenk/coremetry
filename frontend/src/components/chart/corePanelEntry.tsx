// corePanelEntry — lazy-yükleme giriş noktası (v0.9.708).
//
// Overview (ve gelecekteki sayfalar) CorePanel'i React.lazy ile buradan
// alır. spanSeriesToFrames de BURADA çağrılır: sayfa @grafana/data'ya
// statik bağlansaydı lazy'nin amacı boşa düşer, vendor yine şişerdi
// (ölçüldü: 35 KB → 1 MB). Sayfa yalnız ham SpanMetricSeries geçirir.

import { CorePanel, type CorePanelProps } from './CorePanel';
import { spanSeriesToFrames } from '@/lib/chart/dataFrame';
import type { SpanMetricSeries } from '@/lib/types';

export interface CorePanelWithFramesProps
  extends Omit<CorePanelProps, 'data'> {
  series: SpanMetricSeries[];
  unit?: string;
  seriesName?: string;
}

export function CorePanelWithFrames({ series, unit, seriesName, ...rest }: CorePanelWithFramesProps) {
  return (
    <CorePanel {...rest} data={{
      state: 'ready',
      frames: spanSeriesToFrames(series, { unit, name: seriesName }),
    }} />
  );
}

// ── v0.9.717 (dalga-2) — ÇOK SERİLİ giriş ────────────────────────────────
//
// Overview RED kartları birden çok adlandırılmış seri + ROL taşır
// (OK=success yeşil, Errors=error kırmızı — seriesRole vitrini). Tek-seri
// giriş (üstte) pilot panel için aynen duruyor; bu ek, dalga-2
// dönüşümlerinin ortak kapısı.
export interface CorePanelMultiItem {
  series: SpanMetricSeries[];
  name: string;
  role?: 'data' | 'error' | 'success' | 'muted';
  // v0.9.744 (Explore v2) — bu item'ın ilk frame'ine bağlı ◆ listesi.
  exemplars?: import('@/lib/chart/overlays').ChartExemplar[];
  // v0.9.793 — bu item KESİKLİ çizilsin. Ghost (önceki-dönem) kanalıyla AYNI
  // CorePanel prop'una (dashed[]) iner; fark yalnız kimin işaretlediği:
  // ghost'u CorePanelMulti kendi işaretler, bunu ÇAĞIRAN işaretler.
  // İlk tüketici Explore'un formül paneli — "bu seri ölçülmedi, HESAPLANDI"
  // ayrımı QueryPanel rozetinde (kesikli kenarlık) zaten kurulu bir dil.
  dashed?: boolean;
  // v0.9.799 — emphasis KALDIRILDI (v0.9.798'de eklenmişti). Tek
  // tüketicisi Overview'ın "Toplam" item'ıydı; operatör o çizgiyi büyük
  // grafiklerden geri aldırınca kanal tüketicisiz kaldı ve CorePanel
  // tarafıyla birlikte silindi — yarım kablo bırakmıyoruz.
}

export interface CorePanelMultiProps extends Omit<CorePanelProps, 'data' | 'roles' | 'dashed'> {
  items: CorePanelMultiItem[];
  unit?: string;
  // v0.9.764 — önceki-dönem hayaleti: zamanları ÇAĞIRAN kaydırmış
  // (bugünün eksenine bindirilmiş) seriler; kesikli + soluk çizilir,
  // adları "(önceki)" ekiyle lejantta.
  ghostItems?: CorePanelMultiItem[];
  // v0.9.748 (operatör: "yüklenirken 'aralığı genişlet' çıkıyor") —
  // sorgu sürerken boş items "veri yok" boş-durumuna düşmesin; loading
  // true iken Spinner'lı yükleme durumu çizilir.
  loading?: boolean;
  // v0.9.774 — HATA ve BOŞ kanalları. PanelData'nın 'error'/'empty'
  // varyantları v0.9.704'ten beri tipte ve render dalları CorePanel'de
  // hazırdı ama HİÇBİR çağıran onları üretemiyordu: çok-serili giriş
  // yalnız loading/ready kurabiliyordu. Sonuç, başarısız bir sorgunun
  // "Bu aralıkta çizilecek nokta yok · Aralığı genişletmeyi deneyin"
  // yazması — yanlış teşhis, yanlış eylem.
  //
  // İkisi de PRİMİTİF (string): CorePanel'e giden prop kimliği her
  // render'da değişip config'i yıkmasın (v0.9.704 destroy/recreate dersi).
  // Verilmezse davranış bayt-bayt bugünküdür.
  error?: string;
  emptyReason?: string;
  emptyHint?: string;
}

export function CorePanelMulti({
  items, unit, loading, ghostItems, error, emptyReason, emptyHint, ...rest
}: CorePanelMultiProps) {
  if (loading) return <CorePanel {...rest} data={{ state: 'loading' }} />;
  // Sıra ÖNEMLİ: hata boşluğu kapsar (başarısız sorgunun serisi de yok).
  if (error) return <CorePanel {...rest} data={{ state: 'error', message: error }} />;
  if (emptyReason) {
    return <CorePanel {...rest} data={{ state: 'empty', reason: emptyReason, hint: emptyHint }} />;
  }
  // TEK geçiş: frames + rol hizası birlikte (çifte dönüşüm = çifte
  // display-processor kurulumu olurdu).
  const frames: ReturnType<typeof spanSeriesToFrames> = [];
  const roles: NonNullable<CorePanelProps['roles']> = [];
  const exemplars: NonNullable<CorePanelProps['exemplars']> = [];
  // v0.9.793 — kesikli işaretler frame'lerle BİRLİKTE toplanır (ayrı bir
  // ikinci geçiş ghost/normal item sayılarını yeniden türetmek zorunda
  // kalırdı; iki sayaç = bir gün kayan hizalama).
  const dashed: boolean[] = [];
  let anyEx = false;
  let anyDash = false;
  for (const it of items) {
    const fs = spanSeriesToFrames(it.series, { unit, name: it.name });
    frames.push(...fs);
    for (let i = 0; i < fs.length; i++) {
      roles.push(it.role ?? 'data');
      // ◆'lar item'ın İLK frame'ine biner (Explore: item = tek seri).
      exemplars.push(i === 0 ? it.exemplars : undefined);
      if (i === 0 && it.exemplars?.length) anyEx = true;
      dashed.push(!!it.dashed);
      if (it.dashed) anyDash = true;
    }
  }
  for (const g of ghostItems ?? []) {
    const fs = spanSeriesToFrames(g.series, { unit, name: `${g.name} (önceki)` });
    frames.push(...fs);
    for (let i = 0; i < fs.length; i++) {
      roles.push('muted');
      exemplars.push(undefined);
      dashed.push(true);
      anyDash = true;
    }
  }
  return <CorePanel {...rest} roles={roles} exemplars={anyEx ? exemplars : undefined}
    dashed={anyDash ? dashed : undefined}
    data={{ state: 'ready', frames }} />;
}
