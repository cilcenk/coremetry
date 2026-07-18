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
