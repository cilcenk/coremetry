// v0.9.677 — metrik gecikme panelinin BİRİM ETİKETİ.
//
// Ayrı bir saf fonksiyon, çünkü bu bir DEĞER+BİRİM şablonu ve bu kod
// tabanının kayıtlı dersi şu: her dalı ship anında test et, yoksa
// eksen-dışı dal sessizce bozulur (feedback-unit-mixing-needs-both-
// branches).
//
// Burada eksen-dışı dal GERÇEKTEN çalışmıyor: yerel veride birim `ms`
// ve tanınıyor, yani "bilinmeyen birim" yolu hiç icra edilmiyor. Tam
// olarak bu yüzden testi var.
//
// Sözleşme:
//   tanınan birim   → değerler ms'ye çevrildi, etiket " ms"
//   BİLİNMEYEN birim → çevrilmedi, etiket HAM birim (ya da "?")
//
// Yanlış ölçekli bir grafik ölçeksiz olandan kötüdür: operatör ona
// güvenir. Bu yüzden bilinmeyen birimde ms YAZILMIYOR.
export function metricLatencyUnitLabel(known: boolean | undefined, unit: string | undefined): string {
  if (known === false) return ` ${unit && unit.trim() !== '' ? unit : '?'}`;
  return ' ms';
}

// Üstteki span türevli panelle DOĞRUDAN kıyaslanabilir mi?
// Yalnız ms'ye çevrilmiş seriler kıyaslanabilir.
export function metricLatencyComparable(known: boolean | undefined): boolean {
  return known !== false;
}
