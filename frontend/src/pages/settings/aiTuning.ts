import type { AISettings } from '@/lib/types';

// aiTuning (v0.9.1120 / Faz 0.5) — AiTab'in üç ayar kutusu ile telin
// arasındaki çeviri. SAF ve TABLO-TESTLİ, çünkü buradaki tek bir yanlış
// dal SESSİZ AYAR DONMASI demek.
//
// SÖZLEŞME: backend bu üç alanı EFEKTİF DEĞER olarak değil, ÜSTÜNE YAZMA
// (override) olarak döndürür. maxTokens 0 / temperature null / timeoutS 0
// hepsi "üstüne yazma yok — yerleşik varsayılan çalışıyor" demek
// (4096 / 0.2 / 180s).
//
// KUSUR SINIFI (bu modülün var olma sebebi): formun boş kutuya yerleşik
// varsayılanı YAZMASI. Görsel olarak masum — kutuda "4096" görünür ve
// doğru görünür. Ama PUT gövdesi TÜM BLOB'u değiştiriyor, yani operatör
// tamamen ilgisiz bir sebeple (anahtar döndürmek, CoSRE'yi kapatmak)
// Kaydet'e bastığında o varsayılan blob'a YAZILIR. O andan sonra ayar
// artık "varsayılanı takip et" değil, "4096'da sabitlen"dir; Coremetry
// varsayılanı yarın 8192 yapsa bu kurulum onu hiç görmez ve kimse
// nedenini bilmez — çünkü ekranda hiçbir şey değişmemişti.
//
// Bu yüzden form state'i SAYI değil DİZİ tutar: '' tek dürüst "unset"
// gösterimidir, varsayılanlar placeholder'da yaşar ve boş kutu tele
// 0 / null (= sıfırla) olarak gider.

export interface AITuningForm {
  maxTokens: string;
  temperature: string;
  timeoutS: string;
}

export interface AITuningWire {
  maxTokens: number;
  temperature: number | null;
  timeoutS: number;
}

// toForm — sunucudan gelen override → kutu metni.
//
// İki "unset" yazılışı var ve İKİSİ de '' olmalı: int'lerde 0, işaretçide
// null. Ama temperature'da 0 GEÇERLİ BİR DEĞER — orada falsy'ye değil
// yalnız null/undefined'a bakılır. (Bu ayrım tek satır ama ters yazılırsa
// operatörün bilerek seçtiği 0.0 sıcaklık her sayfa açılışında sessizce
// kaybolur.)
export function tuningToForm(s: Partial<AISettings> | null | undefined): AITuningForm {
  const int = (v: number | null | undefined) =>
    v === null || v === undefined || v === 0 ? '' : String(v);
  const temp = (v: number | null | undefined) =>
    v === null || v === undefined ? '' : String(v);
  return {
    maxTokens: int(s?.maxTokens),
    temperature: temp(s?.temperature),
    timeoutS: int(s?.timeoutS),
  };
}

// toWire — kutu metni → PUT gövdesi.
//
// Boş VE ayrıştırılamaz girdi aynı yere gider: sıfırlama işareti. Burada
// "anlaşılmayanı varsayılana çevir" bilinçli — alternatif NaN göndermek
// (backend 400 verir, operatör neden anlamaz) ya da eski değeri korumak
// (kutu boş görünürken ayar duruyor = kutu YALAN söylüyor) olurdu.
export function tuningToWire(f: AITuningForm): AITuningWire {
  const int = (v: string) => {
    const n = Number(v.trim());
    return v.trim() === '' || !Number.isFinite(n) ? 0 : Math.round(n);
  };
  const float = (v: string): number | null => {
    const n = Number(v.trim());
    return v.trim() === '' || !Number.isFinite(n) ? null : n;
  };
  return {
    maxTokens: int(f.maxTokens),
    temperature: float(f.temperature),
    timeoutS: int(f.timeoutS),
  };
}
