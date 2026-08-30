import type { K8sCoverageRow, PodRow } from '@/lib/types';

// coverageRows — K8s bağlam kapsama kartının SAF çekirdeği
// (v0.10.36, entity katmanı Faz 0).
//
// ── NEDEN SAF ───────────────────────────────────────────────────────────
//
// Kartın tek işi bir YARGI vermek: "bu servis bu alanı yayıyor mu". Yargı
// bir örneklem üzerinden veriliyor ve yanlış verildiğinde sonraki fazın
// kabul testi bozulur — yani düzeltmek için var olduğu şeyi bozar. O
// yüzden yargı bileşende değil, testlenebilir bir fonksiyonda.

/** Bir alanın kapsama durumu. */
export type FieldState = 'full' | 'partial' | 'none' | 'unknown';

/**
 * fieldState — bir alanın kapsaması.
 *
 * ⚠ `unknown` ile `none` AYRI. Örneklemde o servisten hiç satır
 * görülmediyse "alan yok" DEMEK YANLIŞ olur — ölçüm yapılmadı.
 *
 * ⚠⚠ AMA BU DAL BUGÜNKÜ ARKA UÇTA ULAŞILAMAZ (v0.10.62). Sorgu
 * `GROUP BY service_name` yapıyor ve SIFIR satırlı bir grup HİÇ SATIR
 * ÜRETMEZ — yani `sampled` asla 0 gelmiyor. Örnekleme girmeyen servis
 * "unknown" olarak değil, tabloda HİÇ görünmüyordu; kartın "en önemli
 * sözleşmesi" diye yazılan ayrım hiç çalışmıyordu.
 * ([[feedback-empty-set-vanishes-not-zero]] — bu deponun tekrar eden sınıfı.)
 *
 * Ölçülmemişliğin GERÇEK işareti artık başka yerde ve zarfta taşınıyor:
 *   • v0.10.56 — örnekleme SERVİS BAŞINA kotalı, yani her servis kendi
 *     kotasıyla temsil ediliyor (öncesinde alfabetik ilk 5 servis).
 *   • v0.10.62 — `capped`: dış tavan ısırdıysa bazı servisler örnekleme
 *     hiç girmemiş olabilir ve kart EKSİK bir filo üzerinden konuşur.
 *
 * Dal SİLİNMİYOR: sözleşme doğru, üreteni yok. Bir gün satır-üreten
 * başka bir kaynak (ör. servis listesiyle diff) eklenirse burası hazır —
 * ve o güne kadar burada yazılı olan şey, iddianın nerede karşılandığı.
 */
export function fieldState(seen: number, sampled: number): FieldState {
  if (!sampled || sampled <= 0) return 'unknown';
  if (seen <= 0) return 'none';
  if (seen >= sampled) return 'full';
  return 'partial';
}

/** Kapsama yüzdesi (0-100). sampled=0 → null (bölme değil, BİLGİ yok). */
export function fieldPct(seen: number, sampled: number): number | null {
  if (!sampled || sampled <= 0) return null;
  return Math.round((seen / sampled) * 1000) / 10;
}

/** Kartta gösterilen alanlar — sıra ENTITY hiyerarşisini izliyor. */
// v0.10.192 (rollouts audit ön koşul): attr = TAM anahtar (kart bunu basar;
// eskiden `k8s.` önekini elle ekliyordu ve container.image.* o öneke
// sığmıyor). cluster üçe ayrıldı: birleşik (geriye uyum) + hangi anahtar.
// v0.10.196 (Operator-reported: servis tablosunda "CLU… CLU… NA… DE…"):
// `short` = servis tablosunun BAŞLIĞI. 11 alan + servis + örneklem 1366 px
// laptop'ta ~40 px'lik kolonlara sıkışıyor ve "cluster (openshift)" ile
// "cluster (k8s)" aynı "CLU…"ya kırpılıyordu (CLAUDE.md tuzak kuralı:
// fixed + nowrap + dar kolon sessizce kırpar). Kısa etiket 40 px'e sığar;
// tam anahtar başlığın title'ında (AdminK8sCoverage renderLabel).
export const COVERAGE_FIELDS = [
  { key: 'cluster', label: 'cluster', short: 'clu', attr: 'k8s.cluster.name | openshift.cluster.name' },
  { key: 'clusterK8s', label: 'cluster (k8s)', short: 'k8s', attr: 'k8s.cluster.name' },
  { key: 'clusterOpenshift', label: 'cluster (openshift)', short: 'ocp', attr: 'openshift.cluster.name' },
  { key: 'namespace', label: 'namespace', short: 'ns', attr: 'k8s.namespace.name' },
  { key: 'deployment', label: 'deployment', short: 'dep', attr: 'k8s.deployment.name' },
  { key: 'replicaset', label: 'replicaset', short: 'rs', attr: 'k8s.replicaset.name' },
  { key: 'pod', label: 'pod', short: 'pod', attr: 'k8s.pod.name' },
  { key: 'podUid', label: 'pod.uid', short: 'uid', attr: 'k8s.pod.uid' },
  { key: 'node', label: 'node', short: 'node', attr: 'k8s.node.name' },
  { key: 'container', label: 'container', short: 'ctr', attr: 'k8s.container.name' },
  { key: 'image', label: 'image', short: 'img', attr: 'container.image.name' },
] as const;

/** Başlık ipucu: tam anahtar + okunur ad (servis tablosu başlığı `short`). */
export function coverageHeaderTitle(key: string): string | undefined {
  const f = COVERAGE_FIELDS.find(x => x.key === key);
  return f ? `${f.attr} — ${f.label}` : undefined;
}

export type CoverageFieldKey = (typeof COVERAGE_FIELDS)[number]['key'];

/** Satırdan bir alanın ham sayımı. */
export function fieldSeen(r: K8sCoverageRow, k: CoverageFieldKey): number {
  switch (k) {
    case 'cluster': return r.cluster;
    case 'clusterK8s': return r.clusterK8s ?? 0;
    case 'clusterOpenshift': return r.clusterOpenshift ?? 0;
    case 'replicaset': return r.replicaset ?? 0;
    case 'image': return r.image ?? 0;
    case 'namespace': return r.namespace;
    case 'deployment': return r.deployment;
    case 'pod': return r.pod;
    case 'podUid': return r.podUid;
    case 'node': return r.node;
    case 'container': return r.container;
  }
}

/** Filo geneli özet — bir alanı KAÇ servis yayıyor. */
export interface FleetSummary {
  field: CoverageFieldKey;
  label: string;
  /** tam attribute anahtarı (v0.10.192) */
  attr: string;
  /** Alanı TAM yayan servis sayısı. */
  full: number;
  /** Kısmi yayan (bazı span'lerinde var). */
  partial: number;
  /** Hiç yaymayan. */
  none: number;
}

/**
 * fleetSummary — kartın ÜST şeridi: "filonun ne kadarı bu alanı yayıyor".
 *
 * Operatörün asıl sorusu bu. Servis servis tablo ikincil; önce filo
 * resmi gelmeli, yoksa 200 satırda kaybolur.
 *
 * `unknown` servisler HİÇBİR kovaya girmiyor — ölçülmemiş bir servisi
 * "yaymıyor" saymak, kartın kendi sözleşmesini bozardı.
 */
export function fleetSummary(rows: K8sCoverageRow[] | undefined): FleetSummary[] {
  return COVERAGE_FIELDS.map(f => {
    let full = 0, partial = 0, none = 0;
    for (const r of rows ?? []) {
      switch (fieldState(fieldSeen(r, f.key), r.sampled)) {
        case 'full': full++; break;
        case 'partial': partial++; break;
        case 'none': none++; break;
        // 'unknown' bilerek sayılmıyor.
      }
    }
    return { field: f.key, label: f.label, attr: f.attr, full, partial, none };
  });
}

// ── POD ENVANTERİ (v0.10.41, Faz 1 okuma yarısı) ────────────────────────


/**
 * podSeenWindow — pod'un ÖRNEKLEMDE görüldüğü aralık, okunur biçimde.
 *
 * ⚠ Bu pod ÖMRÜ DEĞİL. Örneklem penceresi içindeki ilk/son span; pod
 * ondan önce de sonra da yaşıyor olabilir. Adlandırma bu yüzden
 * "görüldü", "ayakta" değil — arayüzün ürettiği cümle, ölçtüğü şeyden
 * fazlasını iddia etmemeli.
 */
export function podSeenWindow(r: PodRow): string {
  const ms = (r.lastSeen - r.firstSeen) / 1e6;
  if (!Number.isFinite(ms) || ms < 0) return '—';
  if (ms < 60_000) return `${Math.round(ms / 1000)} sn boyunca görüldü`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)} dk boyunca görüldü`;
  return `${Math.round(ms / 3_600_000)} sa boyunca görüldü`;
}

/**
 * podStabilityWarning — birleşmiş ömür uyarısı.
 *
 * ⚠ KİMLİĞİN TEK ZAYIF NOKTASI. Kimlik (namespace, pod adı) ve
 * StatefulSet pod adları restart'ta DEĞİŞMİYOR (`svc-0` hep `svc-0`).
 * Yani aynı ad iki ayrı pod ömrünü taşıyor olabilir ve görülme aralığı
 * ikisini birden kapsar.
 *
 * null = uyarı yok (Deployment pod'u; rastgele sonek ömürleri ayırıyor).
 */
export function podStabilityWarning(r: PodRow): string | null {
  if (!r.nameStable) return null;
  return 'Pod adı sabit desende (StatefulSet). Aynı ad restart\'tan sonra ' +
    'geri döndüğü için bu aralık İKİ ayrı pod ömrünü kapsıyor olabilir — ' +
    'kimlikte pod.uid olmadığı için ayrıştırılamıyor.';
}
