import type { K8sCoverageRow } from '@/lib/types';

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
 * görülmediyse "alan yok" DEMEK YANLIŞ olur — ölçüm yapılmadı. Bu ayrım
 * kartın en önemli sözleşmesi: kart, collector değişikliğinin kabul testi
 * olacak ve "ölçmedim"i "yok" diye okumak o testi çürütür.
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
export const COVERAGE_FIELDS = [
  { key: 'cluster', label: 'cluster' },
  { key: 'namespace', label: 'namespace' },
  { key: 'deployment', label: 'deployment' },
  { key: 'pod', label: 'pod' },
  { key: 'podUid', label: 'pod.uid' },
  { key: 'node', label: 'node' },
  { key: 'container', label: 'container' },
] as const;

export type CoverageFieldKey = (typeof COVERAGE_FIELDS)[number]['key'];

/** Satırdan bir alanın ham sayımı. */
export function fieldSeen(r: K8sCoverageRow, k: CoverageFieldKey): number {
  switch (k) {
    case 'cluster': return r.cluster;
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
    return { field: f.key, label: f.label, full, partial, none };
  });
}
