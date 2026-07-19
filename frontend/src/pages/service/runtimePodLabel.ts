// runtimePodLabel (v0.9.91) — Runtime panelinin pod-bazlı çizgi etiketi.
//
// groupBy ÜÇLÜ (öncelik sırası okunabilirlik için — churn-tespitinden
// FARKLI, o benzersizliği yeğler, deploys.go instanceIdExpr):
//   [0] k8s.pod.name         — collector k8sattributes zenginleştirmesi
//                              (her ortamda yok; varsa altın standart)
//   [1] host.name            — container hostname = k8s pod adı
//                              (javaagent 2.x default; podservice.go:11,
//                              store.go:2392 — kod tabanının bildiği gerçek)
//   [2] service.instance.id  — javaagent'in ürettiği UUID (SON çare;
//                              operatör "4abcd5" olarak bunu görüyordu)
//
// Okunabilir pod adı (0 veya 1) varsa servis-adı prefix'i kırpılır
// (bsa-...-login-prep-6f9c7b-x2v → 6f9c7b-x2v); yoksa instance id ilk 8;
// üçü de boşsa spec fallback'i (tek çizgi aggregate).
export function podLineLabel(groupKey: string[], service: string, fallback: string): string {
  const podName = (groupKey[0] ?? '').trim() || (groupKey[1] ?? '').trim();
  if (podName) {
    return podName.startsWith(service + '-') ? podName.slice(service.length + 1) : podName;
  }
  const inst = (groupKey[2] ?? '').trim();
  if (inst) return inst.slice(0, 8);
  return fallback;
}
