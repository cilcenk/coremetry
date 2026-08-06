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
