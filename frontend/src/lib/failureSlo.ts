import type { Threshold } from '@/components/MultiLineChart';
import type { FailureSLOConfig } from '@/lib/types';

// v0.9.1036 — hata-oranı (%) eşiğinin TEK çözümleme noktası.
//
// Üç kaynak, bu SIRAYLA (en özelden en genele):
//
//   1. Gerçek bir *availability* SLO'su (/api/slos, sliType=="availability")
//      → çizgi zaten ondan geliyor (ServiceCharts, hata bütçesi %'si).
//      Bu blob HİÇ konuşmaz. Operatör o servis için bir SLO nesnesi
//      açtıysa cevabını yazmış demektir; üstüne ikinci bir "SLO %1"
//      çizgisi koymak grafiği çift eşikli ve okunmaz yapardı.
//   2. Servis başına override (failure_slo blob'u).
//   3. Filo geneli varsayılan (failure_slo blob'u, %1).
//
// Neden ayrı dosya: v0.9.1024 dersi — saf yardımcıyı ÇAĞRI NOKTASINDAN
// ayır ve kapıyı çağrıya da uygula. Bu fonksiyon test edilir, ama asıl
// kapı ServiceCharts'ın onu GERÇEKTEN çağırdığını da yokluyor.

// resolveFailurePct — override → varsayılan. null = çizgi çizilmez.
//
// 0 bilinçli olarak null'a düşer: "%0 hata hedefi" çizgisi ekseninin
// tabanına yapışır ve hiçbir şey söylemez; blob'da 0 yazmanın anlamı
// zaten "çizgi istemiyorum".
export function resolveFailurePct(
  cfg: FailureSLOConfig | undefined | null,
  service: string,
): number | null {
  if (!cfg) return null;
  const ov = cfg.overrides?.[service];
  // `?? ` DEĞİL `typeof` kontrolü: override 0 olabilir ve 0 ANLAMLI bir
  // değer ("bu servis için çizgi yok"), yani varsayılana düşmemeli.
  const pct = typeof ov === 'number' ? ov : cfg.defaultPct;
  if (typeof pct !== 'number' || !Number.isFinite(pct) || pct <= 0) return null;
  if (pct > 100) return 100;
  return pct;
}

// failureThresholds — hata-oranı panelinin `thresholds` prop'u.
//
// sloDerived: /api/slos'tan türeyen çizgiler (varsa). Doluysa aynen
// döner — üstüne varsayılan eklenmez (kural 1).
export function failureThresholds(
  sloDerived: Threshold[] | undefined,
  cfg: FailureSLOConfig | undefined | null,
  service: string,
): Threshold[] | undefined {
  if (sloDerived && sloDerived.length > 0) return sloDerived;
  const pct = resolveFailurePct(cfg, service);
  if (pct === null) return undefined;
  // Etiket "SLO %X": availability SLO'sunun "err ≤ X%" etiketinden
  // BİLEREK farklı — operatör grafiğe bakınca çizginin nereden geldiğini
  // (nesne mi, filo varsayılanı mı) ayırt edebilmeli.
  return [{ value: pct, label: `SLO %${trimPct(pct)}`, severity: 'err' }];
}

// trimPct — 1 → "1", 2.5 → "2.5", 1.50 → "1.5". Etiket dar bir grafiğin
// üstünde duruyor; "1.00%" gereksiz üç karakter.
function trimPct(p: number): string {
  return String(Math.round(p * 100) / 100);
}
