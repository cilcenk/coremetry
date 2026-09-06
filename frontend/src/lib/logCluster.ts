// Log satırından CLUSTER kimliği — v0.10.501 (log arama denetimi B4:
// filter-for/out yalnız pod hücresindeydi; servis / cluster / attribute
// hücreleri pivotsuzdu).
//
// logPod.ts'in ikizi: değerle birlikte ONU TAŞIYAN ANAHTAR döner, ⊕/⊖
// pill'i o anahtarla yazılır (`k8s.cluster.name:"x"`). Anahtar listesi
// LogTable'ın gösterim zinciriyle AYNI (gösterilen değer = süzgecin
// bulacağı değer, v0.8.265 sınıfı) ve backend'lerin cluster takma
// adlarına bağlı: CH DSL derleyicisi (internal/chstore/log_query_compile.go
// `case "cluster", …`) bu anahtarları cluster zincirine çevirir; ES'te
// çıplak anahtar doküman yoludur (query_string), `openshift.labels.cluster`
// yalnız ES'te yaşar (ClusterLogForwarder üst düzey yazar). Kapı:
// logCluster.test.ts Go kaynağını okuyup listeyi doğrular.
export const CLUSTER_FIELDS = [
  'openshift.labels.cluster',
  'openshift.cluster.name',
  'k8s.cluster.name',
  'kubernetes.cluster.name',
  'kubernetes.cluster_name',
  'cluster',
] as const;

type AttrBag = {
  attributes?: Record<string, string> | null;
  resourceAttributes?: Record<string, string> | null;
} | null | undefined;

// clusterEntryOfLog — ilk boş olmayan (anahtar, değer); boş dize değer
// sayılmaz (zincir yürür — v0.5.224 dersi). Önce resourceAttributes.
export function clusterEntryOfLog(l: AttrBag): { key: string; value: string } | null {
  if (!l) return null;
  const ra = l.resourceAttributes ?? {};
  const at = l.attributes ?? {};
  for (const src of [ra, at]) {
    for (const k of CLUSTER_FIELDS) {
      const v = src[k];
      if (typeof v === 'string' && v.length > 0) return { key: k, value: v };
    }
  }
  return null;
}

export function clusterOfLog(l: AttrBag): string {
  return clusterEntryOfLog(l)?.value ?? '';
}
