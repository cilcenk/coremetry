// podWorkloadName — pod adından iş yükü adı sezgiseli (v0.9.56).
// Backend internal/thanos/promql.go stripPodSuffixes'in FE aynası:
// Deployment pod'u <ad>-<rs-hash 8-10 hex>-<5 rasgele>, StatefulSet
// <ad>-<N>, DaemonSet <ad>-<5 rasgele>. Son segment k8s rand
// alfabesindense (rakam + sessiz harfler) ya da tamamen sayıysa
// soyulur; kalan son segment rs-hash'e benziyorsa o da soyulur.
// Tutmayan ada DOKUNULMAZ. Prefix DEĞİL eşitlik için kullanılır:
// "bsa-login" servisi "bsa-login-prep-…" pod'unu YAKALAMAZ (kardeş
// servis öneki tuzağı) — soyulmuş ad birebir karşılaştırılır.
const RAND5 = /^[0-9bcdfghjklmnpqrstvwxz]{5}$/;
const RS_HASH = /^[0-9a-f]{8,10}$/;
const ALL_DIGITS = /^[0-9]+$/;

export function podWorkloadName(pod: string): string {
  const segs = pod.split('-');
  if (segs.length < 2) return pod;
  const last = segs[segs.length - 1];
  if (RAND5.test(last) || ALL_DIGITS.test(last)) {
    segs.pop();
    if (segs.length >= 2 && RS_HASH.test(segs[segs.length - 1])) segs.pop();
    return segs.join('-');
  }
  return pod;
}

// Enstrümantasyon-enjeksiyon varyant ekleri (v0.9.56, operatör ekran
// görüntüsü kanıtı — OpenShift konsolundaki gerçek filo adlandırması):
// aynı servisin "bsa-callcenter-core-prep" VE
// "bsa-callcenter-core-prep-oneagent" deployment'ları koşuyor
// (Dynatrace OneAgent enjeksiyonu ayrı deployment üretir). Varyant eki
// soyulup servise eşlenir; "-batch"/"-uat" gibi kardeş İŞ YÜKLERİ
// listede DEĞİL — onlar ayrı servistir, prefix eşleşmesi bilinçli yok.
const WORKLOAD_VARIANT_SUFFIXES = ['-oneagent'];

// workloadMatchesService — soyulmuş iş-yükü adının servise eşitliği,
// bilinen enstrümantasyon varyantları dahil.
export function workloadMatchesService(workload: string, service: string): boolean {
  if (workload === service) return true;
  for (const suf of WORKLOAD_VARIANT_SUFFIXES) {
    if (workload === service + suf) return true;
  }
  return false;
}

// PodMatchInput — podMatchesService'in ihtiyaç duyduğu ClusterPodRow
// alt kümesi (test edilebilirlik için dar tip; ClusterPodRow bunu karşılar).
export interface PodMatchInput {
  pod: string;
  namespace: string;
  service?: string;
}

// podMatchesService — Servis → Infrastructure sekmesinin pod-eşleşme
// zincirinin SAF kararı (v0.9.130 — operatör raporu: "infrastructure
// tabında bazı cluster'ları buluyor bazılarını bulamıyor").
//
// Kök neden: zincir eskiden ServiceInfraTab içinde kilitli bir if/else'ti —
// `depRow` (o cluster'ın KSM ns-rollup'unda deployment satırı) bulununca
// pod'lar YALNIZCA `podSet.has(pod)` ile eşleşir, `<deploy>-` prefix
// yedeğine hiç DÜŞMEZDİ. Bir cluster'da KSM owner ailesi kısmi/yoksa —
// ya da applyDeployKSM cpu/mem serisi olmayan bir deployment'ı
// PodNames:[] ile eklediyse — o cluster'ın pod'ları podSet'te bulunmadığı
// için HİÇ eşleşmiyor; depRow bulunmayan cluster ise prefix yedeğiyle
// buluyordu → "bazı cluster buluyor, bazısı bulamıyor".
//
// Düzeltme: deploy varken podSet ADDİTİF (kilit değil) — üyelik VEYA
// prefix. Union, prefix-yalnızdan da (özel-adlı pod'u KSM üyeliği yakalar)
// podSet-yalnızdan da (eksik KSM'i prefix yakalar) geniş; eşleşmeyi asla
// daraltmaz, yalnız genişletir.
export function podMatchesService(
  p: PodMatchInput,
  opts: { service: string; deploy: string; ns: string; podNames: Set<string> | null },
): boolean {
  const { service, deploy, ns, podNames } = opts;
  // Namespace süzgeci — metadata ns türetildiyse aynı adlı başka
  // namespace'in pod'unu dışlar; ns boşsa (yedek mod) uygulanmaz.
  if (ns && p.namespace !== ns) return false;
  if (deploy) {
    return (podNames?.has(p.pod) ?? false) || p.pod.startsWith(deploy + '-');
  }
  // deploy yoksa: enrichment servis alanı YA DA soyulmuş iş-yükü adı ==
  // servis (prefix DEĞİL eşitlik — kardeş-öneki tuzağı yok).
  return p.service === service || workloadMatchesService(podWorkloadName(p.pod), service);
}
