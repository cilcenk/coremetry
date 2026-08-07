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
}

export interface CorePanelMultiProps extends Omit<CorePanelProps, 'data' | 'roles'> {
  items: CorePanelMultiItem[];
  unit?: string;
}

export function CorePanelMulti({ items, unit, ...rest }: CorePanelMultiProps) {
  // TEK geçiş: frames + rol hizası birlikte (çifte dönüşüm = çifte
  // display-processor kurulumu olurdu).
  const frames: ReturnType<typeof spanSeriesToFrames> = [];
  const roles: NonNullable<CorePanelProps['roles']> = [];
  const exemplars: NonNullable<CorePanelProps['exemplars']> = [];
  let anyEx = false;
  for (const it of items) {
    const fs = spanSeriesToFrames(it.series, { unit, name: it.name });
    frames.push(...fs);
    for (let i = 0; i < fs.length; i++) {
      roles.push(it.role ?? 'data');
      // ◆'lar item'ın İLK frame'ine biner (Explore: item = tek seri).
      exemplars.push(i === 0 ? it.exemplars : undefined);
      if (i === 0 && it.exemplars?.length) anyEx = true;
    }
  }
  return <CorePanel {...rest} roles={roles} exemplars={anyEx ? exemplars : undefined}
    data={{ state: 'ready', frames }} />;
}
