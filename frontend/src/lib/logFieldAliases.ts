// Log alanlarının ŞEKİL adayları — Kibana derin-linki için (v0.9.661).
//
// Operatör-bildirimi: "Bir Service'e ait logları Discover'ın Kibana
// dediğimde bulamıyor. Ancak Logs sayfasında listeleniyor."
//
// KÖK NEDEN — kural üç yere bölünmüştü, ikisi anlaşmıştı, üçüncüsü
// geride kalmıştı:
//
//   1. GÖSTERİM  (elasticsearch.go mapHit) — servis adını okurken
//      kubernetes.container_name'i düz service_name'den ÖNCE deniyor.
//   2. FİLTRE    (elasticsearch.go svcFields) — aynı k8s alanlarını
//      eşliyor, böylece gösterilen ada tıklayınca liste boş dönmüyor.
//   3. KIBANA LİNKİ (burası) — tek bir `service.name:"…"` yazıyordu.
//
// Operatörün prod OpenShift indeksinde `service.name` alanı HİÇ YOK:
// gerçek servis kimliği kubernetes.container_name'de, düz service_name
// ise uygulamanın kendi OPERASYON adı (LOAN_CORPORATE_..._V2). Coremetry
// logları buluyordu (2), Kibana linki "No results" diyordu (3).
//
// Aynı ayrışma trace ve seviye için de vardı: o indekste alan `trace_id`
// (düz), link `trace.id:` soruyordu.
//
// DÜZ `service_name` LİSTEDE YOK — bilinçli. v0.9.480'de ölçüldü:
// cluster-logging kayıtlarında o alan çoğu zaman servis DEĞİL, operasyon
// adı. Linke eklemek yanlış kayıtları eşleştirirdi. Backend'in svcFields
// listesi de tam bu yüzden onu dışarıda bırakıyor; buradaki liste onun
// AYNADIR, "yardımcı olmaya çalışan" bir üst kümesi değil.
//
// KAPI: logFieldAliases.test.ts Go kaynağını OKUYUP bu listelerin
// backend'le eşleştiğini doğruluyor. Diller arası ayna bu kod tabanında
// zaten var (logEnvSuffixes ↔ podWorkload.ts ENV_SUFFIXES) ve ayrışması
// tam olarak bu hata sınıfını üretiyor.

// SERVICE_FIELDS — internal/logstore/elasticsearch.go `svcFields`.
// "service.name" başta çünkü s.fields.Service'in varsayılanı o.
export const SERVICE_FIELDS = [
  'service.name',
  'kubernetes.container.name',
  'kubernetes.container_name',
  'kubernetes.labels.app',
  'kubernetes.labels_app',
] as const;

// TRACE_FIELDS / SPAN_FIELDS / LEVEL_FIELDS — elasticsearch.go
// `expandShorthand` alias tablosu.
export const TRACE_FIELDS = ['trace.id', 'trace_id', 'traceId', 'TraceId'] as const;
export const SPAN_FIELDS = ['span.id', 'span_id', 'spanId', 'SpanId'] as const;
export const LEVEL_FIELDS = ['log.level', 'level', 'severity', 'severity_text', 'SeverityText'] as const;

// LOG_ENV_SUFFIXES — internal/logstore/env_suffix.go `logEnvSuffixes`
// (ve pages/clusters/podWorkload.ts ENV_SUFFIXES).
export const LOG_ENV_SUFFIXES = ['-prod', '-int', '-uat', '-prep'] as const;

// stripLogEnvSuffix — env_suffix.go'nun aynısı. Servis adı ortam ekiyle
// bitiyorsa eksiz hâli de aday DEĞERdir: bazı pipeline'lar konteyneri
// eksiz adlandırıyor.
export function stripLogEnvSuffix(service: string): string {
  for (const suf of LOG_ENV_SUFFIXES) {
    if (service.length > suf.length && service.endsWith(suf)) {
      return service.slice(0, -suf.length);
    }
  }
  return service;
}

// kqlEscape — KQL değer kaçışı. Ters bölü ÖNCE gelmeli, yoksa kendi
// eklediğimiz kaçışları bir daha kaçırırız.
export function kqlEscape(v: string): string {
  return v.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

// kqlAnyField — "şu alanlardan HERHANGİ biri şu değerlerden HERHANGİ
// birine eşit" grubu.
//
// Tek alan + tek değer olduğunda parantez YOK: en sık durum
// (`trace.id:"abc"`) okunabilir kalsın ve mevcut linkler değişmesin.
//
// İndekste olmayan alan KQL'de hata değil, sadece eşleşmez — bu yüzden
// aday listesi uzun olabilir; maliyeti yok, faydası indeks şekli
// bilinmeden çalışması.
export function kqlAnyField(fields: readonly string[], values: readonly string[]): string {
  const clauses: string[] = [];
  for (const f of fields) {
    for (const v of values) {
      clauses.push(`${f}:"${kqlEscape(v)}"`);
    }
  }
  if (clauses.length === 0) return '';
  if (clauses.length === 1) return clauses[0];
  return `(${clauses.join(' or ')})`;
}

// serviceValues — bir servis adı için aday DEĞERler: tam ad her zaman,
// eksiz ad yalnız gerçekten bir ek soyulduysa (aksi halde aynı değeri
// iki kez sorardık — svcValues, elasticsearch.go).
export function serviceValues(service: string): string[] {
  const stripped = stripLogEnvSuffix(service);
  return stripped === service ? [service] : [service, stripped];
}
