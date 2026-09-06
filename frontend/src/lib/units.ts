// units — v0.10.506 (dış skill denetimi D9): dashboard panellerinin birim
// alanı serbest metindi ve karo (formatStatValue) ile eksen (DashChart →
// fmtSmart) iki ayrı yoldan okuyordu: "msec" yazan operatörün karosu çıplak
// sayı basıyor, "percent" ekseni yüzde bilmiyordu. Tek sözlük: fmtSmart'ın
// tanıdığı kanonik yazımlar + sık takma adlar; bilinmeyen değer OLDUĞU GİBİ
// kalır (kayıtlı dashboard'lardaki özel ek — "req/dk" gibi — kaybolmaz,
// fmtSmart onu düz ek olarak basar).

export const PANEL_UNITS: ReadonlyArray<{ value: string; label: string }> = [
  { value: '', label: '— (yok)' },
  { value: 'ms', label: 'ms (milisaniye)' },
  { value: 's', label: 's (saniye)' },
  { value: '%', label: '% (yüzde)' },
  { value: 'bytes', label: 'bytes (B/KB/MB…)' },
  { value: 'rps', label: 'rps (istek/sn)' },
];

const ALIASES: Record<string, string> = {
  ms: 'ms', msec: 'ms', millis: 'ms', millisecond: 'ms', milliseconds: 'ms', milisaniye: 'ms',
  s: 's', sec: 's', secs: 's', second: 's', seconds: 's', saniye: 's',
  '%': '%', pct: '%', percent: '%', yüzde: '%', yuzde: '%',
  b: 'bytes', byte: 'bytes', bytes: 'bytes', bayt: 'bytes',
  rps: 'rps', 'req/s': 'rps', 'r/s': 'rps', 'requests/s': 'rps', 'istek/sn': 'rps',
};

// normalizeUnit — SAF: kanonik yazım ya da (bilinmeyen) kırpılmış ham değer.
export function normalizeUnit(raw: string | undefined | null): string {
  const t = (raw ?? '').trim();
  if (!t) return '';
  return ALIASES[t.toLowerCase()] ?? t;
}

// isKnownUnit — seçici listede var mı (özel değer → "diğer" metin kutusu).
export function isKnownUnit(u: string): boolean {
  return PANEL_UNITS.some(p => p.value === u);
}
