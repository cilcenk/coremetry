// attrKeyWindow — attribute-key keşfinin penceresi (v0.9.953, UX denetimi
// F3 / Ö14c).
//
// SORUN: anahtar keşfi SABİT '1h' penceresinde koşuyordu — sayfa aralığı
// ne olursa olsun. 7 günlük bir pencereye bakan operatör, son bir saatte
// hiç görülmemiş bir attribute'u öneri listesinde BULAMIYORDU; kutuya
// doğru adı yazsa bile "yok" sanıyordu. Yanlış yazımın bedeli de sessiz:
// sorgu koşuyor, boş dönüyor ve "bu pencerede hiçbir span'de yok" cümlesi
// "böyle veri yok" diye okunuyor.
//
// AMA PENCEREYİ HAM GEÇİRMEK YASAK (v0.8.270 disiplini). Sunucu cache
// anahtarı `attr-keys:since=<ham dize>` — serbest bir pencere her
// dokunuşta YENİ bir anahtar üretir, yani 60 sn'lik cache hiç ısınmaz ve
// her keystroke bir CH taraması ödetir. O yüzden pencere SAF bir
// yuvarlamayla BASAMAKLARA oturuyor: en fazla beş farklı anahtar.
//
// ⚠ BASAMAKLAR YALNIZ SAAT — 'd' YOK. Go'nun time.ParseDuration'ı 'd'
// birimini TANIMAZ (`internal/api/api.go` parseDuration); '7d' gönderseydik
// sunucu sessizce VARSAYILANA (1 saat) düşerdi ve düzeltme hiç
// uygulanmamış gibi görünürdü — v0.6.36 birim-karışımı sınıfının ta
// kendisi. Test her basamağı Go'nun kabul ettiği biçime karşı çiviliyor.
//
// SAF — tablo testleri attrKeyWindow.test.ts.

import type { GoDuration } from './utils';
import { timeRangeToNs } from './utils';
import type { TimeRange } from './types';

// Basamaklar, saniye → GoDuration. YUKARI yuvarlanır: keşif penceresi
// operatörün baktığı pencereyi KAPSAMALI, yoksa "bu aralıkta var ama
// öneride yok" hâline geri döneriz.
const RUNGS: readonly { maxSec: number; since: GoDuration }[] = [
  { maxSec: 3600, since: '1h' },
  { maxSec: 6 * 3600, since: '6h' },
  { maxSec: 24 * 3600, since: '24h' },
  { maxSec: 7 * 24 * 3600, since: '168h' },   // 7 gün — 'd' YOK (yukarı bak)
  { maxSec: 30 * 24 * 3600, since: '720h' },  // 30 gün
] as const;

/** ATTR_KEY_RUNGS — testin ve teşhisin okuduğu basamak listesi. */
export const ATTR_KEY_RUNGS = RUNGS;

/**
 * snapSince — saniye cinsinden pencereyi bir basamağa oturtur.
 *
 * Tavan 30 gün: ötesi retention'ın kendisi ve keşif sorgusunu oraya
 * açmak, öneri listesi için milyarlarca satır taramak demek.
 */
export function snapSince(seconds: number): GoDuration {
  if (!isFinite(seconds) || seconds <= 0) return '1h';
  for (const r of RUNGS) {
    if (seconds <= r.maxSec) return r.since;
  }
  return RUNGS[RUNGS.length - 1].since;
}

/**
 * attrKeySince — sayfanın TimeRange'inden basamaklanmış keşif penceresi.
 *
 * range verilmezse '1h' (eski davranış, bayt-bayt): pencereyi bilmeyen
 * bir çağıran için varsayım üretmek, yanlış pencereden daha kötü.
 */
export function attrKeySince(range?: TimeRange | null): GoDuration {
  if (!range) return '1h';
  const { from, to } = timeRangeToNs(range);
  return snapSince(Math.round((to - from) / 1e9));
}
