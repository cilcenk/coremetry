// shiftWindow.ts — v0.9.1322 (§3.1 K3).
//
// /shift'in üç servis linki penceresiz gidiyordu: 24 saatlik bir vardiya
// özetinden tıklanan servis, operatörün sticky 30dk penceresinde
// açılıyordu — yani vardiyada olan biten ekranda YOKTU.
//
// Problem ve exception satırlarının KENDİ olay pencereleri var
// (eventLifespanWindow / exceptionGroupWindow, lib/serviceHref.ts).
// "En çok kötüleşen servisler" satırının ise hiç zaman damgası yok
// (ChangedService = iki pencerenin kıyası); onun dürüst penceresi
// sayfanın baktığı aralıktır ve o da burada üretilir.
//
// NEDEN `range=8h` GEÇMEK YANLIŞ OLURDU (ölçüldü): sayfanın üç
// penceresinden biri, `8h`, lib/utils.ts PRESET_SECONDS'ta YOK. Preset
// olarak yazılsa timeRangeToNs onu tanımaz ve sessizce 86400'e (24h)
// düşerdi — "8 saat" yazan bir düğmeden 24 saatlik bir sayfa açılırdı.
// Bu yüzden mutlak (custom) pencere üretiyoruz.
//
// `nowMs` argüman, çağrı değil: fonksiyon saf ve test edilebilir kalsın
// (eventLifespanWindow'un `nowNs` emsali).

export const SHIFT_WINDOWS = ['8h', '12h', '24h'] as const;
export type ShiftWindow = (typeof SHIFT_WINDOWS)[number];

/** Sayfanın varsayılan penceresi — `?w=` yokken ve tanınmayan değerde. */
export const SHIFT_DEFAULT: ShiftWindow = '12h';

const SHIFT_HOURS: Record<ShiftWindow, number> = { '8h': 8, '12h': 12, '24h': 24 };

/** `?w=` değerini bilinen bir pencereye indirger. */
export function normalizeShiftWindow(raw: string | null | undefined): ShiftWindow {
  return (SHIFT_WINDOWS as readonly string[]).includes(raw ?? '')
    ? (raw as ShiftWindow)
    : SHIFT_DEFAULT;
}

/** Sayfanın baktığı mutlak aralık, serviceHref'in aldığı şekilde. */
export function shiftWindowNs(
  raw: string | null | undefined,
  nowMs: number = Date.now(),
): { fromNs: number; toNs: number } {
  const hours = SHIFT_HOURS[normalizeShiftWindow(raw)];
  const toNs = nowMs * 1e6;
  return { fromNs: toNs - hours * 3600 * 1e9, toNs };
}
