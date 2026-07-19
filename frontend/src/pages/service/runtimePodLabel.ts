// runtimePodLabel (v0.9.89) — Runtime panelinin pod-bazlı çizgi etiketi.
//
// groupBy = resource.k8s.pod.name + resource.service.instance.id İKİLİ:
// pod adı collector'ın k8sattributes processor'ına bağlı (her ortamda
// yok — lokalde boş döndüğü canlıda görüldü); service.instance.id ise
// javaagent 2.x'in HER ZAMAN bastığı instance UUID'si. Pod adı varsa o
// (servis-adı prefix'i kırpılır: bsa-adkservices-login-prep-6f9c…-x2v
// → 6f9c…-x2v), yoksa instance id'nin ilk 8 karakteri, o da yoksa
// spec'in fallback etiketi (aggregate görünüm — tek çizgi).
export function podLineLabel(groupKey: string[], service: string, fallback: string): string {
  const pod = (groupKey[0] ?? '').trim();
  if (pod) {
    return pod.startsWith(service + '-') ? pod.slice(service.length + 1) : pod;
  }
  const inst = (groupKey[1] ?? '').trim();
  if (inst) return inst.slice(0, 8);
  return fallback;
}
