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

// ── Mutlak pencere (v0.9.969, UX denetimi Ö15) ───────────────────────────────
//
// v0.9.953 pencerenin UZUNLUĞUNU düzeltti; KONUMUNU düzeltemezdi, çünkü
// `since` yalnız "son N" diyebiliyor. Fırçalanmış (custom) bir pencere bu
// yüzden ERİŞİLEMEZDİ: dün öğlen 30 dakikalık bir ani yükselişe zoom'layan
// operatörün anahtar önerileri SON 30 dakikadan geliyordu. Daha dar bir
// cevap değil — BAŞKA bir cevap, ve sessizce: olayın her yerinde bulunan bir
// attribute hiç önerilmiyor, bu da "böyle bir attribute yok" diye okunuyor.
// Ö14c'nin uzunluk için düzelttiği yanlış okumanın aynısı.
//
// GÖRELİ preset'ler `since`te KALIYOR ve bu bilinçli: onlar gerçekten
// now-çapalı, ve `since` sayesinde aynı preset'e bakan bütün operatörler TEK
// bir 60 sn'lik cache girdisini paylaşıyor. Mutlak forma çevirmek her sekmeye
// kendi anahtarını verirdi.
//
// v0.8.270 disiplini mutlak formda da geçerli — pencere HAM geçmiyor: iki
// kenar da 5 dakikalık ızgaraya oturuyor (from AŞAĞI, to YUKARI, yani pencere
// asla DARALMAZ). Fırça 3 piksel oynadığında cache anahtarı değişmiyor;
// ızgara MV'nin 5m granülüyle de aynı, yani "daha ince" bir cevap zaten yok.
export const ATTR_KEY_SNAP_NS = 5 * 60 * 1e9;

/** Keşif penceresi: ya now-çapalı `since`, ya mutlak ns sınırları. */
export type AttrKeyWindow =
  | { since: GoDuration }
  | { fromNs: number; toNs: number };

const MAX_ABS_SPAN_NS = 30 * 24 * 3600 * 1e9; // RUNGS tavanıyla aynı

export function attrKeyWindowParams(range?: TimeRange | null): AttrKeyWindow {
  if (!range) return { since: '1h' };
  if (range.preset !== 'custom') return { since: attrKeySince(range) };
  // Bozuk/ters/eksik bir custom aralıkta uydurmak yerine göreli forma dön.
  // ⚠ fromMs/toMs BURADA kontrol ediliyor, timeRangeToNs'in çıktısında
  // değil: preset 'custom' ama sınırlar yoksa timeRangeToNs sessizce
  // "son 24 saat"e düşüyor (PRESET_SECONDS['custom'] yok → 86400 yedeği).
  // O çıktı > 0 ve artan olduğu için geçerli görünür ve bozuk bir aralığı
  // paylaşılamayan bir MUTLAK pencereye çevirirdi — yanlış olmayan ama
  // cache'i asla ısınmayan bir cevap.
  if (!(range.fromMs && range.toMs)) return { since: attrKeySince(range) };
  const { from, to } = timeRangeToNs(range);
  if (!(from > 0) || !(to > from)) return { since: attrKeySince(range) };
  const toNs = Math.ceil(to / ATTR_KEY_SNAP_NS) * ATTR_KEY_SNAP_NS;
  let fromNs = Math.floor(from / ATTR_KEY_SNAP_NS) * ATTR_KEY_SNAP_NS;
  // Tavan: 30 günden geniş bir keşif taraması öneri listesi için milyarlarca
  // satır demek. Kırpma SONdan değil BAŞtan — operatörün baktığı pencerenin
  // en yeni ucu her zaman kapsamda kalmalı.
  if (toNs - fromNs > MAX_ABS_SPAN_NS) fromNs = toNs - MAX_ABS_SPAN_NS;
  return { fromNs, toNs };
}
