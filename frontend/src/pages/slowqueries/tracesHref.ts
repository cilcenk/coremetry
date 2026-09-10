import { encodeFilters } from '@/lib/urlState';
import { tracesPivotHref } from '@/lib/pivotHref';
import type { TimeRange } from '@/lib/types';

// tracesHref — v0.10.652 (operatör isteği): "Search traces with this query"
// genişletilmiş satırdan çıkıp her satırın EN SAĞINDA "Traces →" kolonu
// oldu; href kurucusu saf ve testli.
//
// v0.5.200 — rootOnly kapalı: kök span'lar genelde gelen HTTP isteğidir ve
// db.statement taşımaz; DB span'ı ÇOCUK span'dır. rootOnly=true (varsayılan)
// ile LIKE sıfır satır eşliyordu (v0.5.195 düzgün FilterExpr kodladı ama
// bunu atlamıştı). tracesPivotHref view:'list' rootOnly'yi kapatır.

/** Örnek ifadenin LIKE için kullanılan ön eki (60 karakter). */
export const SLOW_QUERY_SNIPPET_LEN = 60;

export function slowQueryTracesHref(
  r: { service: string; sampleStatement: string },
  range: TimeRange,
): string {
  const snippet = r.sampleStatement.slice(0, SLOW_QUERY_SNIPPET_LEN);
  const f = encodeFilters([{ k: 'db.statement', op: 'LIKE', v: [snippet] }]);
  return tracesPivotHref({ window: range, service: r.service, filters: f, view: 'list' });
}
