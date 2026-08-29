// codeAsk — v0.10.153 (operatör): "Kodu da incele" kutusu yerine CoSRE ilk
// (kodsuz) açıklamanın SONUNDA sorar: "Kodu da inceleyeyim mi?" Evet →
// ikinci explain çağrısı (includeCode=true) aynı çekmecede "Kod incelemesi"
// ayracı altına akar, ilk cevap KORUNUR. Hayır → soru kapanır (yalnız o
// çekmece örneği için; yeniden açınca yine sorar). URL `ai=…code`
// parametresi (deep link / eski davranış) doğrudan kodlu koşar → soru yok.
// Saf: bileşen bu kararı buradan alır, vitest piner.
export type CodeAskState = 'idle' | 'accepted' | 'declined';

export interface CodeAskInput {
  /** trace | exception — kod bağlamı taşıyabilen türler. */
  codeCapable: boolean;
  /** URL'den gelen ai=code — ilk çağrı zaten kodlu, soru gereksiz. */
  includeCode: boolean;
  /** İlk cevap ekranda mı (text !== null). */
  hasText: boolean;
  /** İlk akış hâlâ sürüyor mu. */
  busy: boolean;
  hasError: boolean;
  state: CodeAskState;
}

/** Soru satırı çizilsin mi — yalnız ilk cevap BİTTİKTEN sonra ve karar verilmemişken. */
export function shouldAskForCode(a: CodeAskInput): boolean {
  return a.codeCapable && !a.includeCode && a.hasText && !a.busy && !a.hasError && a.state === 'idle';
}
