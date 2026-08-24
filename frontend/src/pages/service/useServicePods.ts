import { useMemo } from 'react';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import { clampThanosWindow } from '@/lib/thanosWindow';
import { podMatchesService, dominantWorkload, servicePodRegex } from '@/pages/clusters/podWorkload';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// dominantNamespace — eşleşen pod'ların en sık namespace'i (v0.9.56):
// metadata ns türetilememişse grafik/JMX sorgularının namespace parametresi
// buradan gelir (yedek modda pod'lar zaten ada göre eşleşti).
function dominantNamespace(rows: ClusterPodRow[]): string {
  const counts = new Map<string, number>();
  for (const r of rows) {
    if (r.namespace) counts.set(r.namespace, (counts.get(r.namespace) ?? 0) + 1);
  }
  let best = '', n = 0;
  for (const [ns, c] of counts) {
    if (c > n || (c === n && ns < best)) { best = ns; n = c; }
  }
  return best;
}

// useServicePods (v0.9.158) — servisin Thanos pod envanteri, hem Infrastructure
// hem yeni Pods sekmesince paylaşılan VERİ KATMANI. Önceden ServiceInfraTab'ın
// içindeydi; Pods sekmesi de aynı eşleşmeye (rows/effNs/effDeploy) ihtiyaç
// duyduğundan hook'a çıkarıldı (fetch'ler cache-paylaşımlı, tekrar istek yok).
//
// Cluster keşfi TÜM etkin Thanos kaynaklarını tarar (v0.9.138); span-türetimi
// cluster SEÇİMİNDE kullanılmaz — hangi cluster'da pod var precise
// pod-eşleşmesi (podMatchesService, v0.9.130) belirler. Grafik parametreleri
// yedek modda da dolu: deploy yoksa servis adı, ns yoksa baskın namespace.
export function useServicePods(service: string, range: TimeRange) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const metaQ = useServicesMetadata();
  const ns = metaQ.data?.[service]?.namespace ?? '';
  const deploy = metaQ.data?.[service]?.deployment ?? '';

  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 300_000,
  });
  const matched = useMemo(() => sourcesQ.data?.clusters ?? [], [sourcesQ.data]);

  const depQs = useQueries({
    queries: matched.map(c => ({
      queryKey: ['cluster-deployments', c, ns],
      queryFn: () => api.clusterDeployments(c, ns),
      staleTime: 60_000, retry: 1, enabled: ns !== '',
    })),
  });
  // v0.9.536 — HEDEFLİ envanter (operator-reported: BFF pod'ları
  // cluster-geneli topk(500)'e giremiyordu → istemci eşleştirmesi hiç
  // gelmeyen pod'u eşleştiremez, "No pods matched"). Sunucu regex'le
  // daraltır; anahtar podRe'yi taşır — /clusters sayfasının düz
  // envanter cache'iyle ÇAKIŞMAZ (farklı anahtar, farklı sonuç).
  const podRe = servicePodRegex(service, deploy);
  const podQs = useQueries({
    queries: matched.map(c => ({
      queryKey: ['cluster-pods', c, podRe],
      queryFn: () => api.clusterPods(c, podRe),
      staleTime: 60_000,
      // v0.9.539 (operatör: "20+ cluster, kullanıcılar 20-30 sn
      // bekliyor") — RETRY YOK. Aritmetik: handler'ın cluster başına
      // 10s deadline'ı × retry:1 = tek yanıt vermeyen cluster 20
      // saniye. Yeniden deneme burada BEDAVA DEĞİL ve işe de yaramaz:
      // 10 saniyede yanıt vermemiş bir Thanos'un hemen ardından
      // yanıt verme olasılığı düşük, ve 60s'lik poll zaten doğal
      // yeniden denemedir. Hata o cluster için görünür kalır
      // (podErrors → "X cluster'ı yanıt vermedi"), sessizce yutulmaz.
      retry: false,
    })),
  });

  // Pod eşleşme (podMatchesService, testli): ns süzgeci + deploy varken podSet
  // ÜYELİĞİ ⋃ "<deploy>-" prefix VEYA yedek modda isim-eşitliği. Bilinçli
  // memo'suz: useQueries kimliği her render değişir, tarama ≤ birkaç bin satır.
  const rows: ClusterPodRow[] = [];
  matched.forEach((c, i) => {
    const depRow = deploy
      ? (depQs[i]?.data?.deployments ?? []).find(d => d.deployment === deploy)
      : undefined;
    const podSet = depRow ? new Set(depRow.podNames) : null;
    for (const p of podQs[i]?.data?.pods ?? []) {
      if (podMatchesService(p, { service, deploy, ns, podNames: podSet })) rows.push(p);
    }
  });
  const clustersWithPods = [...new Set(rows.map(r => r.cluster))];
  // v0.9.535 — deploy yedeği artık EŞLEŞEN pod'ların iş-yükü adı,
  // servis adı son çare. BFF'te servis adı env ekli (mobile-loans-
  // bff-prod), pod'lar eksiz — PromQL pod=~"<servis>-.*" hiç tutmuyordu
  // ve CPU/Mem boş kalıyordu. Gözlemlenen pod'un iş-yükü adı her zaman
  // doğru önektir; satır yoksa eski davranış (servis adı) aynen kalır.
  const effDeploy = deploy || dominantWorkload(rows.map(r => r.pod)) || service;
  const effNs = ns || dominantNamespace(rows);

  // Sunucu pencere tavanı — Clusters Overview'la aynı dürüstlük
  // (v0.9.21). Kural tek gövdede (lib/thanosWindow), sunucuyla
  // ayrışması Go testiyle kapalı (v0.9.1370).
  const { cFrom, cTo, clamped } = useMemo(
    () => clampThanosWindow(from, to), [from, to]);

  // Gate bit'leri (her iki sekme aynı boş/yükleniyor durumlarını gösterir).
  const sourcesPending = sourcesQ.isPending;
  const noClusters = (sourcesQ.data?.clusters ?? []).length === 0;
  // v0.9.538 (operatör: "infra sayfası tüm cluster'ları döndüğü için
  // yavaş yükleniyor") — KADEMELİ render. Eskiden podsPending =
  // some(isPending) idi: TEK bir yavaş cluster tüm sayfayı spinner'da
  // tutuyordu. 14 cluster'ın 13'ü 200ms'de dönse de 14.'sü handler'ın
  // 10s deadline'ına dayanırsa operatör 10 saniye boş ekran görüyordu.
  //
  // Cluster listesi DARALTILAMAZ: servis, Coremetry'ye trace
  // göndermediği (yalnız Thanos'ta metrik/JMX olan) cluster'larda da
  // koşabilir — spans'tan türetmek v0.9.138'in düzelttiği "bazı
  // cluster'lar görünmüyor" bug'ını geri getirirdi (v0.9.142 notu).
  // O yüzden hepsini sormaya devam ediyoruz, ama GELENİ hemen çiziyoruz.
  const podsSettled = podQs.filter(q => !q.isPending).length;
  const podsTotal = podQs.length;
  const podsPending = podsSettled < podsTotal;
  // podsBlocking — spinner SADECE hiçbir eşleşme yokken ve hâlâ
  // beklenen cluster varken. Erken "No pods matched" YASAK: ilk dönen
  // cluster'lar boş çıkıp pod'lar 12. cluster'da olabilir; o hâlde
  // "yok" demek yalan olurdu.
  const podsBlocking = rows.length === 0 && podsPending;
  // v0.9.363 — hatalar dışarı çıkıyor. Eskiden Thanos 502'si, 10s handler
  // deadline'ı ya da süresi dolmuş token, "No pods matched … nothing
  // matched" ile AYNI ekranı üretiyordu: operatöre ürün yanlış yapılandırılmış
  // ya da workload yok deniyordu — tam da bakması gereken anda.
  const sourcesError = sourcesQ.isError ? String(sourcesQ.error) : null;
  const podErrors = podQs
    .map((q, i) => (q.isError ? (matched[i] ?? `cluster ${i + 1}`) : null))
    .filter((x): x is string => !!x);
  // v0.9.369 — sunucu topk(500) tavanına dayanan cluster'lar: istemci
  // süzmesi o cluster'da "yok" sonucunu KANITLAYAMAZ (sakin pod'lar
  // topk dışında). Boş durum ve başlık bunu söylemek zorunda.
  const truncatedClusters = podQs
    .map((q, i) => (q.data?.truncated ? (matched[i] ?? `cluster ${i + 1}`) : null))
    .filter((x): x is string => !!x);

  return {
    metaQ, ns, deploy, matched, rows, clustersWithPods,
    effNs, effDeploy, from, to, cFrom, cTo, clamped,
    sourcesPending, noClusters, podsPending, podsBlocking, podsSettled, podsTotal,
    sourcesError, podErrors,
    truncatedClusters,
  };
}
