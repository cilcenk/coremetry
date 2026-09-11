// paletteIdentity — v0.10.674 (operatör, prod: "aynı function_id global
// search'ten bulunmuyor ama Traces sayfasında girince buluyor").
//
// Komut paleti eşleştirme için sorguyu küçük harfe çevirir (servis/sayfa
// puanlaması harf-duyarsız olsun diye) ve v0.10.350'nin "Kimlikle trace
// ara" önerisi de o küçük harfli değeri /traces?traceId= ile gönderiyordu.
// Sunucunun kimlik-önce yolu (v0.10.342-344) attr_function_id = ? ile
// harf-DUYARLI eşitlik yapar: "…vzXA…" → "…vzxa…" sıfır satır.
//
// Sözleşme: kimlik değeri HAM sorgudan türer — kırpılır, harfi korunur.
// Trace id dalı (hex) ayrı; orada küçük harf doğru.

// IDENTITY_RE — tek parça, ≥8, [A-Za-z0-9._:-] (sunucu identityToken ile
// aynı sınıf). v0.10.350'de CommandPalette'te yaşıyordu; tek yazım burada.
export const IDENTITY_RE = /^[A-Za-z0-9._:-]{8,}$/;

export function paletteIdentityQuery(rawQuery: string): string | null {
  const v = rawQuery.trim();
  if (!IDENTITY_RE.test(v) || !/\d/.test(v)) return null;
  return v;
}

export function identityTracesHref(v: string): string {
  return `/traces?traceId=${encodeURIComponent(v)}`;
}
