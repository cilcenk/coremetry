// jmxHumanize — v0.9.383, redesign D7 (mockup af7419e5). Ham JMX metrik
// adını insan-dili panel başlığına çevirir; ham ad alt yazı olarak
// monospace kalır (bilgi kaybı yok). Kural-tabanlı (sözlük değil):
// jmx_exporter adları serbest biçimlidir, sözlük hiç bitmezdi.
//   jvm_memory_bytes_used            → "Memory bytes used — JVM"
//   jvm_gc_collection_seconds        → "GC collection seconds — JVM"
//   jboss_undertow_request_count_total → "Undertow request count — JBoss"
// Saf; jmxHumanize.test.ts ile pinli.

const FAMILY: Array<[prefix: string, label: string]> = [
  ['jvm_', 'JVM'],
  ['jboss_', 'JBoss'],
];

// Kısaltmalar büyük kalır; diğer parçalar küçük-harf akar (ilk kelime
// cümle-başı büyür). "_total" Prometheus sayaç eki — başlıkta gürültü.
const ACRONYMS = new Set(['gc', 'jvm', 'ws', 'jdbc', 'xa', 'ejb', 'jms', 'cpu', 'io']);

export function jmxHumanize(metric: string): { title: string; raw: string } {
  let rest = metric;
  let family = '';
  for (const [prefix, label] of FAMILY) {
    if (rest.startsWith(prefix)) {
      rest = rest.slice(prefix.length);
      family = label;
      break;
    }
  }
  rest = rest.replace(/_total$/, '');
  const words = rest.split('_').filter(Boolean).map((w, i) => {
    if (ACRONYMS.has(w)) return w.toUpperCase();
    if (i === 0) return w.charAt(0).toUpperCase() + w.slice(1);
    return w;
  });
  // Tanınmayan/boş kalıntı → ham ad başlık olur, aile eki de eklenmez
  // ("jvm_ — JVM" gibi yarı-insan başlık üretme).
  if (words.length === 0) return { title: metric, raw: metric };
  const body = words.join(' ');
  return { title: family ? `${body} — ${family}` : body, raw: metric };
}
