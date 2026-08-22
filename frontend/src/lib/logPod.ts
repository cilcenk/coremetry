// Log satırından POD kimliği — v0.9.1249 (Kibana-parite artığı).
//
// Bağlam modalının "⌖ Yalnız bu pod" kapsamı pivot satırının podunu
// bilmek zorunda: kalabalık bir serviste ±pencere BAŞKA podların
// satırlarıyla dolar, incelenen olay tek podun hikâyesiyken komşuluk
// çok-pod karışımı olur. Kibana bunu tek indeks üzerinde pod alan
// filtresiyle yapar; bizde kapsamı istemci çıkarır, sunucu süzer.
//
// POD_FIELDS = internal/logstore/elasticsearch.go `esPodFields` AYNASI.
// Sıra da aynı: operatörün gerçek prod şekli (OpenShift
// cluster-logging'in düz `kubernetes.pod_name`i) BAŞTA. Bu bir
// nezaket değil sözleşme — GÖSTERDİĞİMİZ pod adı FİLTRENİN
// bulamadığı bir değerse düğme yalan söyler (v0.8.265 sınıfı:
// seçimi başka bir kümeye karşı doğrulamak). Kapı: logPod.test.ts
// Go kaynağını okuyup listeleri karşılaştırıyor.
export const POD_FIELDS = [
  'kubernetes.pod_name',
  'k8s.pod.name',
  'kubernetes.pod.name',
  'resource_attributes.k8s.pod.name',
  'pod_name',
] as const;

// podOfLog — pivot satırının pod adı, yoksa boş dize.
//
// Önce resourceAttributes (CH backend OTLP resource haritasını oraya
// yazar; ES mapHit `k8s.`/`kubernetes.` önekli düzleşmiş alanları oraya
// taşır), sonra attributes (ES'in `resource_attributes.…` ve düz
// `pod_name` gibi önek eşleşmeyen alanları orada kalır).
//
// Boş dize DEĞER SAYILMAZ: bazı pipeline'lar kanonik OTel attr'ını boş
// yazıp gerçek değeri snake_case ikizine koyar — LogTable'ın pod
// zincirindeki v0.5.224 dersi. Zincir boş değerde durmaz, yürür.
//
// Sonuç boşsa çağıran DÜĞMEYİ ÇİZMEZ: kapsayamayacağımız bir kapsamı
// vaat etmek, boş sonuç döndürmekten beterdir.
export function podOfLog(l: {
  attributes?: Record<string, string> | null;
  resourceAttributes?: Record<string, string> | null;
} | null | undefined): string {
  if (!l) return '';
  const ra = l.resourceAttributes ?? {};
  const at = l.attributes ?? {};
  for (const src of [ra, at]) {
    for (const k of POD_FIELDS) {
      const v = src[k];
      if (typeof v === 'string' && v.length > 0) return v;
    }
  }
  return '';
}
