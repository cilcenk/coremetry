// heatmapRamp — v0.10.505 (dış skill denetimi D8): LatencyHeatmap'in
// yoğunluk rampası TEMA TOKENLARINDAN türer. Eskiden altı rgba sabiti
// (mavi → amber → kırmızı) her temada aynıydı: açık temada soluk, Red Hat
// temasında vurgu maviyle çakışıyordu; canvas tokenı çözemez, o yüzden
// çizim anında getComputedStyle + useThemeTick ile yeniden çözülür.
// Görsel dil korunur: boş hücre görünmez, az → vurgu (üç alfa basamağı),
// çok → uyarı, tepe → hata rengi. SAF; heatmapRamp.test.ts.

export function hexToRgb(hex: string): [number, number, number] | null {
  const h = hex.trim().replace(/^#/, '');
  const m = h.length === 3 ? h.split('').map(c => c + c).join('') : h;
  if (!/^[0-9a-fA-F]{6}$/.test(m)) return null;
  return [parseInt(m.slice(0, 2), 16), parseInt(m.slice(2, 4), 16), parseInt(m.slice(4, 6), 16)];
}

export interface RampTokens { accent: string; warn: string; err: string }

// DEFAULT_RAMP_TOKENS — token çözülemezse (test ortamı, çok erken çizim)
// koyu temanın değerleri; sessiz siyah rampa yerine okunur bir rampa.
export const DEFAULT_RAMP_TOKENS: RampTokens = { accent: '#388bfd', warn: '#d29922', err: '#ff5252' };

const rgba = (hex: string, fallback: string, a: number) => {
  const c = hexToRgb(hex) ?? hexToRgb(fallback)!;
  return `rgba(${c[0]},${c[1]},${c[2]},${a})`;
};

// densityRamp — 6 durak: 0 = hücre yok (şeffaf), 1-3 vurgu (artan alfa),
// 4 uyarı, 5 hata (tepe). Alfa tek yönlü artar; lejant da bunu çizer.
export function densityRamp(t: RampTokens): string[] {
  return [
    'rgba(0,0,0,0)',
    rgba(t.accent, DEFAULT_RAMP_TOKENS.accent, 0.18),
    rgba(t.accent, DEFAULT_RAMP_TOKENS.accent, 0.40),
    rgba(t.accent, DEFAULT_RAMP_TOKENS.accent, 0.65),
    rgba(t.warn, DEFAULT_RAMP_TOKENS.warn, 0.80),
    rgba(t.err, DEFAULT_RAMP_TOKENS.err, 0.90),
  ];
}

// heatmapTimeLabel — x ekseni etiketi: pencere 24 saati aşıyorsa
// "DD.MM HH:MM" (çok günlü eksende yalnız saat okunmuyordu), yoksa "HH:MM".
export function heatmapTimeLabel(ns: number, spanNs: number): string {
  const d = new Date(ns / 1e6);
  const hm = `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`;
  if (spanNs > 24 * 3600 * 1e9) {
    return `${d.getDate().toString().padStart(2, '0')}.${(d.getMonth() + 1).toString().padStart(2, '0')} ${hm}`;
  }
  return hm;
}
