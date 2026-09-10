// formNumbers — v0.10.604. Sayı/liste kutusu ↔ tel çevirileri. influxForm'dan
// TAŞINDI (oracleForm oradan re-export ediyordu): Influx sökülünce (operatör
// 2026-09-10) Oracle formu şablonunu kaybetmesin — ortak yardımcı tarafsız
// dosyada yaşar, iki form da buradan alır. Davranış BİREBİR aynı:
//   • numFromForm: '' | çöp → undefined (unset); sayı → sayı. Negatifi sunucu reddeder.
//   • numToForm: 0/undefined/null → '' — 0 "sunucu varsayılanı" demek, kutu boş.
//   • parseList / listToText: virgül ya da yeni satır ayırır, kırpar, boşu atar.

export function parseList(text: string): string[] {
  return text.split(/[,\n]/).map(s => s.trim()).filter(Boolean);
}

export function listToText(l: string[] | undefined | null): string {
  return (l ?? []).join(', ');
}

export function numFromForm(text: string): number | undefined {
  const t = text.trim();
  if (t === '') return undefined;
  const n = Number(t);
  return Number.isFinite(n) ? n : undefined;
}

export function numToForm(v: number | undefined | null): string {
  return v === undefined || v === null || v === 0 ? '' : String(v);
}
