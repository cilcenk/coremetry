// podTerm (v0.9.1276, Dynatrace-parite #5) — pod'un SON SONLANMA
// sebebini (kube_pod_container_status_last_terminated_reason) rozet
// tonuna çevirir. Pod listelerinde restart SAYISI hep vardı ama
// SEBEBİ yoktu: operatör OOMKilled'ı yalnız kubectl'de görüyordu.
//
// podPhaseBadge'in (pages/clusters/thresholds.ts) kardeşi — aynı
// "alan → .badge b-* sınıfı" işi. Sebep ekseni ayrı bir dosyada
// çünkü backend'deki worseTermReason ile TEK bir sınıflandırmayı
// paylaşır ve ikisi birlikte değişir.
//
// Sınıflandırma backend'in termReasonRank'ının aynası:
//   OOMKilled → err   (operatörün aradığı sinyal)
//   Completed → gray  (normal çıkış, hata değil)
//   diğer HER ŞEY → warn
//
// Son satır bilerek whitelist DEĞİL: kube-state-metrics yeni bir
// sebep adı basmaya başlarsa (Error, ContainerCannotRun, StartError,
// DeadlineExceeded, Evicted… ve henüz görmediklerimiz) bilinmeyen ad
// "gray"e düşüp gözden kaybolmaz — dikkat çeken tonda kalır.
export type TermTone = 'err' | 'warn' | 'gray';

export function termReasonTone(reason: string): TermTone {
  switch (reason) {
    case 'OOMKilled': return 'err';
    // Boş = sebep yok / KSM yok. Çağıran zaten rozet çizmez; toplam
    // fonksiyon olsun diye nötr ton.
    case '':
    case 'Completed': return 'gray';
    default: return 'warn';
  }
}
