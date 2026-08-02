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
//
// "-bff" — v0.9.535, operatör örneği: servis mobile-overview-prod,
// deployment mobile-overview-bff. Pod adında servis adında OLMAYAN bir
// -bff kuyruğu var; env eki soyulunca (mobile-overview) varyant
// eşitliği onu yakalar. Kardeş disiplini korunur: mobile-overview-web
// gibi başka bir kuyruk eşleşmez.
const WORKLOAD_VARIANT_SUFFIXES = ['-oneagent', '-bff'];

// workloadMatchesService — soyulmuş iş-yükü adının servise eşitliği,
// bilinen enstrümantasyon varyantları dahil.
export function workloadMatchesService(workload: string, service: string): boolean {
  if (workload === service) return true;
  for (const suf of WORKLOAD_VARIANT_SUFFIXES) {
    if (workload === service + suf) return true;
  }
  return false;
}

// stripEnvSuffix — v0.9.535 (operatör direktifi: "mobile*bff-prod
// sonunda prod olmadan bul"). Servis adının SONUNDAKİ bilinen env eki
// soyulur: filoda k8s deployment adı env ekini taşımayabiliyor (servis
// mobile-loans-bff-prod, deployment mobile-loans-bff). Yalnız BİLİNEN
// ekler (whitelist) ve yalnız kuyrukta — "-production" gibi serbest
// varyantlar ya da ad ortasındaki "prod" DOKUNULMAZ.
//
// Çapraz-env notu (operatörle konuşuldu, kabul edilen risk):
// mobile-loans-bff-prod ve mobile-loans-bff-int soyulunca aynı ada
// iner; iki env'in pod'ları aynı Thanos setinde ve katalog ns boşsa
// karışabilir. Çare katalogda namespace girmek (ns süzgeci kesin
// ayırır). Prod tek-env olduğu için beklenen durumda sorun yok.
const ENV_SUFFIXES = ['-prod', '-int', '-uat', '-prep'];

export function stripEnvSuffix(service: string): string {
  for (const suf of ENV_SUFFIXES) {
    if (service.length > suf.length && service.endsWith(suf)) {
      return service.slice(0, -suf.length);
    }
  }
  return service;
}

// dominantWorkload — v0.9.535. Eşleşen pod adlarından EN SIK iş-yükü
// adı; PromQL pod=~"<deploy>-.*" seçicisinin yedeği buradan beslenir.
// Neden servis adı değil: BFF'te servis adı env ekli, pod'lar eksiz —
// gözlemlenen pod'un kendi iş-yükü adı her zaman DOĞRU önektir ve
// kardeş-önek tuzağı taşımaz (bsa-login-prep pod'unun iş yükü
// bsa-login-prep'tir, bsa-login değil). Eşitlikte alfabetik ilk
// (deterministik — map sırasına bırakılmaz).
export function dominantWorkload(pods: string[]): string {
  const counts = new Map<string, number>();
  for (const p of pods) {
    const w = podWorkloadName(p);
    if (w && w !== p) counts.set(w, (counts.get(w) ?? 0) + 1); // soyulmamış ad (hostname vb.) önek kanıtı değil
  }
  let best = '', n = 0;
  for (const [w, c] of counts) {
    if (c > n || (c === n && (best === '' || w < best))) { best = w; n = c; }
  }
  return best;
}

// servicePodRegex — v0.9.536. Servis sekmelerinin HEDEFLİ envanter
// seçicisi: sunucu bu regex'i pod=~ olarak PromQL'e gömer, topk(500)
// servisin kendi pod'ları içinde işler (cluster-geneli kesim kalkar —
// operator-reported: 0.001 core'luk BFF pod'ları top-500'e giremiyordu).
//
// Adaylar: katalog deployment'ı (varsa) + servis adı + env eki soyulmuş
// hâli. Önek kalıbı BİLİNÇLİ gevşek — "(mobile-overview)-.*" hem
// mobile-overview-bff'i hem olası kardeşleri getirir; kesin ayrımı
// istemcideki podMatchesService eşitlik disiplini yapar (sunucu
// DARALTIR, istemci AYIKLAR).
export function servicePodRegex(service: string, deploy: string): string {
  const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const cands = [...new Set([deploy, service, stripEnvSuffix(service)].filter(Boolean))];
  return `(${cands.map(esc).join('|')})-.*`;
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
  if (p.service === service || workloadMatchesService(podWorkloadName(p.pod), service)) {
    return true;
  }
  // v0.9.535 — env eki soyulmuş İKİNCİ aday (operatör direktifi):
  // servis mobile-loans-bff-prod, iş yükü mobile-loans-bff. Yine
  // EŞİTLİK — kardeş disiplini korunur: bsa-login-prod soyulunca
  // bsa-login olur ama bsa-login-prep pod'unun iş yükü bsa-login-prep
  // olduğu için eşleşmez.
  const stripped = stripEnvSuffix(service);
  return stripped !== service && workloadMatchesService(podWorkloadName(p.pod), stripped);
}
