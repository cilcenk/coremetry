// chNodeWork — v0.9.543. Node iş dağılımı panelinin SAF çekirdeği.
//
// Sunucu HAM kümülatif sayaç döndürür (açılıştan beri); pencereyi burada
// açıyoruz: sayfa açılışında taban alınır, her yoklamada fark gösterilir.
// Neden: 5 günlük ortalama son 6 saatteki rejim değişimini seyreltir —
// canlı ölçümde ömür-boyu 0.30 çekirdek, son 6 saat 0.504 (1.7x fark).
//
// Bu dosya üç YANILTMA tuzağını kapatıyor. Üçü de "panel sayı basıyor
// ama yanlış sonuca götürüyor" sınıfı, yani sessiz:
//
//  1. RESTART → 0 DEĞİL. Sayaç sıfırlanınca fark negatif çıkar; 0'a
//     kelepçelemek node'u "iş yapmıyor" gösterir — mümkün en yanıltıcı
//     değer, üstelik ortalamayı düşürüp DİĞERLERİNİN dengesizliğini
//     şişirir. Restart eden node işaretlenir ve hesaptan ÇIKARILIR.
//  2. ÖLÇÜLEMEDİ ≠ DENGELİ. Tüm sayaçlar sıfırsa oran 1.0 dönmek
//     "kusursuz dağılım" demektir; doğrusu null (UI '—' basar).
//  3. SHARD'LAR ARASI KIYAS YANILTIR. 2 shard x 2 replika kurulumda
//     shard'lar TANIM GEREĞİ farklı veri tutar; dört node üzerinden
//     max/mean almak shard çarpıklığını "node suçu" gibi gösterir.
//     Anlamlı kıyas SHARD İÇİNDE: aynı shard'ın replikaları aynı
//     veriyi, aynı insert'i, aynı merge işini alır.

export interface NodeWorkRaw {
  host: string;
  shard: number;
  // v0.9.547 — makro bir AD döndürür ("chc-0"), sayı değil.
  replica: string;
  uptimeS: number;
  cpuMicros: number;
  mergeMillis: number;
  insertedRows: number;
  partFetches: number;
  selectedBytes: number;
  mergesLaunched: number;
}

export interface NodeWorkRow {
  host: string;
  shard: number;
  replica: string;
  /** Sayaç sıfırlandı → bu turda ölçüm yok, hesaptan çıkarıldı. */
  restarted: boolean;
  /** Ortalama çekirdek (Δ mikrosaniye / geçen süre). null = ölçülemedi. */
  cpuCores: number | null;
  /** Ortalama merge THREAD'i — CPU değil, meşgul-thread. null = ölçülemedi. */
  mergeThreads: number | null;
  insertedRows: number | null;
  partFetches: number | null;
}

export interface NodeWorkView {
  rows: NodeWorkRow[];
  /** Shard İÇİ en yüksek dengesizlik (max/mean). null = ölçülemedi. */
  cpuImbalance: number | null;
  insertImbalance: number | null;
  /** Farkın kapsadığı süre (ms). 0 = taban henüz yok. */
  elapsedMs: number;
  /** Hiçbir node ölçülemediyse false — UI hüküm vermemeli. */
  measurable: boolean;
}

export interface Baseline {
  /** Sunucunun generatedAt'i (ns) — İSTEMCİ saati DEĞİL. Gövde 30s TTL +
   *  staleFactor ile 90 saniyeye kadar bayat servis edilebiliyor; istemci
   *  duvar saati ilerlerken sayaçlar ilerlemezse hız kolonları çöker. */
  atNs: number;
  by: Record<string, NodeWorkRaw>;
}

export function makeBaseline(raw: NodeWorkRaw[], generatedAtNs: number): Baseline {
  return { atNs: generatedAtNs, by: Object.fromEntries(raw.map(n => [n.host, n])) };
}

/** max/mean; ölçülemez (boş küme, tek eleman, hepsi sıfır) → null. */
export function imbalanceOf(values: number[]): number | null {
  if (values.length < 2) return null;
  const sum = values.reduce((a, v) => a + v, 0);
  if (sum <= 0) return null;
  const mean = sum / values.length;
  return Math.round((Math.max(...values) / mean) * 100) / 100;
}

/**
 * Shard İÇİ dengesizliklerin en yükseği. Shard bilinmiyorsa (shard=0)
 * tüm node'lar tek grup sayılır — bilgi yokken kıyas yapmamaktansa
 * kaba kıyas yapmak yeğdir, ama UI shard'ın bilinmediğini SÖYLEMELİ.
 */
export function worstShardImbalance(
  rows: NodeWorkRow[],
  pick: (r: NodeWorkRow) => number | null,
): number | null {
  const byShard = new Map<number, number[]>();
  for (const r of rows) {
    if (r.restarted) continue;              // restart eden hesaba girmez
    const v = pick(r);
    if (v == null) continue;
    const arr = byShard.get(r.shard);
    if (arr) arr.push(v); else byShard.set(r.shard, [v]);
  }
  let worst: number | null = null;
  for (const vals of byShard.values()) {
    const imb = imbalanceOf(vals);
    if (imb != null && (worst == null || imb > worst)) worst = imb;
  }
  return worst;
}

export function nodeWorkView(
  raw: NodeWorkRaw[],
  baseline: Baseline | null,
  generatedAtNs: number,
): NodeWorkView {
  const elapsedMs = baseline ? Math.max(0, (generatedAtNs - baseline.atNs) / 1e6) : 0;
  const secs = elapsedMs / 1000;

  const rows: NodeWorkRow[] = raw.map(n => {
    const b = baseline?.by[n.host];
    // Taban yok (yeni node ya da ilk yükleme) VEYA süre geçmedi →
    // ölçüm yok. Sıfır basmak "boşta" demek olurdu.
    if (!b || secs <= 0) {
      return { host: n.host, shard: n.shard, replica: n.replica, restarted: false,
        cpuCores: null, mergeThreads: null, insertedRows: null, partFetches: null };
    }
    // Restart: uptime geriye gitti YA DA herhangi bir kümülatif sayaç
    // düştü. İkisini birden kontrol ediyoruz — uptime okunamayan bir
    // kurulumda sayaç düşüşü tek kanıt olarak kalır.
    const restarted = n.uptimeS < b.uptimeS || n.cpuMicros < b.cpuMicros;
    if (restarted) {
      return { host: n.host, shard: n.shard, replica: n.replica, restarted: true,
        cpuCores: null, mergeThreads: null, insertedRows: null, partFetches: null };
    }
    return {
      host: n.host, shard: n.shard, replica: n.replica, restarted: false,
      cpuCores: (n.cpuMicros - b.cpuMicros) / 1e6 / secs,
      mergeThreads: (n.mergeMillis - b.mergeMillis) / 1e3 / secs,
      insertedRows: n.insertedRows - b.insertedRows,
      partFetches: n.partFetches - b.partFetches,
    };
  });

  return {
    rows,
    cpuImbalance: worstShardImbalance(rows, r => r.cpuCores),
    insertImbalance: worstShardImbalance(rows, r => r.insertedRows),
    elapsedMs,
    measurable: rows.some(r => !r.restarted && r.cpuCores != null),
  };
}
