// msgSeries.ts — v0.10.575. SAF: `{timeS, …}` kovalarını CorePanel'in tek
// serisine çevirir.
//
// Neden ayrı dosya: aynı dönüşüm hem /messaging ÇEKMECESİNDE (DetailDrawer'ın
// e2e paneli) hem de /messaging/topic SAYFASINDA (aynı panelin ikizi) gerekiyor.
// İki nüsha bırakmak, birim sözleşmesini iki yere yaymak demekti — ve buradaki
// sözleşme sessizce yanlış olabilen türden: `time` unix NANOSANİYE
// (SpanMetricSeries sözleşmesi), kaynak alan ise SANİYE. Çarpanı unutan bir
// nüsha hata vermez, yalnız grafiği 1970'e koyar.
import type { SpanMetricSeries } from '@/lib/types';

export function kindSeries<T extends { timeS: number }>(
  points: T[], pick: (p: T) => number, label: string,
): SpanMetricSeries[] {
  return [{
    groupKey: [label],
    points: points.map(p => ({ time: p.timeS * 1e9, value: pick(p) })),
  }];
}
