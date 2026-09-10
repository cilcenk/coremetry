// metricOnlyServices — v0.10.610 (operatör bug'ının üçüncü yarısı). Topic
// sayfasının başlığı ve Üreticiler/Tüketiciler sekmeleri SPAN tarafından
// sayar (messaging_caller_summary_5m). 609 kapsamı topic etiketli Kafka
// istemci metriğinden keşifle genişletti: span üretmeyen bir tüketici artık
// panellere giriyor ama başlık hâlâ "Tüketiciler 0" diyordu — iki yüzey
// birbirini yalanlıyordu. Bu yardımcı keşfedilenlerden span'de GÖRÜNMEYENLERİ
// verir; sayfa onları ayrı sayar ve ayrı listeler (RED sayısı uydurmaz:
// span yok, sayı yok).
//
// SAF: isim kümesi farkı, tam eşleşme (servis adı bir kimliktir, kısmi
// eşleşme iki farklı servisi aynı sayardı), sıralı, tekil.

export function metricOnlyServices(
  discovered: readonly string[] | undefined | null,
  spanRows: ReadonlyArray<{ service: string }>,
): string[] {
  if (!discovered || discovered.length === 0) return [];
  const seen = new Set<string>();
  for (const r of spanRows) {
    const s = (r.service ?? '').trim();
    if (s) seen.add(s);
  }
  const out = new Set<string>();
  for (const raw of discovered) {
    const s = (raw ?? '').trim();
    if (s && !seen.has(s)) out.add(s);
  }
  return [...out].sort();
}
