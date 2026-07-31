// scopeTitle.ts (v0.9.483, operatör geri bildirimi: "· giriş eki kafa
// karıştırıyor") — Service Overview RED kartlarının başlık + kapsam
// tooltip'inin TEK kaynağı.
//
// Kural: VARSAYILAN (giriş span'i olan servis) durumda başlık TEMİZ
// ("Response time"), kapsam açıklaması yalnız title-tooltip'inde yaşar.
// FALLBACK (usingAllSpans — batch/producer servis, hiç server/consumer
// span'i yok) durumda "· tüm span'ler" eki GÖRÜNÜR kalır: popülasyon
// sessizce değiştirilemez (v0.9.240'ta konan dürüstlük kuralı). Yani
// ek artık "her zaman var" değil, "sürpriz olduğunda var".
//
// Türkçe tipografik apostrof (’) bilinçli — sayfanın geri kalanıyla aynı.

export const ALL_SPANS_SUFFIX = ' · tüm span’ler';

// scopedChartTitle — taban başlık + kapsam → ekranda görünen başlık.
export function scopedChartTitle(base: string, usingAllSpans: boolean): string {
  return usingAllSpans ? base + ALL_SPANS_SUFFIX : base;
}

// scopeTitleTip — başlığın (ve KPI karolarının / kapsam rozetinin) hover
// açıklaması. Giriş durumunda popülasyonu ANLATIR, fallback durumunda
// NEDEN düşüldüğünü söyler.
export function scopeTitleTip(usingAllSpans: boolean): string {
  return usingAllSpans
    ? 'Tüm span’ler — bu serviste giriş span’i (server/consumer) yok'
    : 'Giriş span’leri: server + consumer — servisin kendi istekleri';
}
